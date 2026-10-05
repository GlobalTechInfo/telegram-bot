package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

type UserData struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

type SessionData struct {
	Language string                 `json:"language"`
	State    string                 `json:"state"`
	Data     map[string]interface{} `json:"data"`
	JoinedAt string                 `json:"joinedAt"`
}

type Feedback struct {
	UserID    int64  `json:"userId"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

type Reminder struct {
	ID        int64  `json:"id"`
	ChatID    int64  `json:"chatId"`
	UserID    int64  `json:"userId"`
	Text      string `json:"text"`
	DueAt     int64  `json:"dueAt"`
	CreatedAt string `json:"createdAt"`
}

type QueryLog struct {
	UserID    int64  `json:"userId"`
	Command   string `json:"command"`
	Input     string `json:"input"`
	Timestamp string `json:"timestamp"`
}

type GroupConfig struct {
	ChatID     int64  `json:"chatId"`
	Title      string `json:"title"`
	Welcome    string `json:"welcome"`
	WelcomeOn  bool   `json:"welcomeOn"`
	Lockdown   bool   `json:"lockdown"`
	AntiLinks  bool   `json:"antiLinks"`
	AntiCaps   bool   `json:"antiCaps"`
	MsgCount   int    `json:"msgCount"`
	LastActive string `json:"lastActive"`
	StreamURL  string `json:"streamUrl"`
	WarnCount  int    `json:"warnCount"`
}

type Store struct {
	db *bbolt.DB
}

// resolveDBPath makes sure the directory holding the database exists. bbolt
// creates the file but never the directories above it, so a configured path like
// /bot/data/bot.db fails on any host that has not had that directory before.
//
// If the configured directory cannot be used the database falls back to the first
// candidate that is genuinely writable, chosen by writing a probe file rather than
// by guessing from the error: a Docker path like /bot/data does not exist outside
// Docker, and /bot cannot be created without root. Falling back rather than
// crashing keeps the bot running, with a loud warning because the data then lives
// on whatever filesystem the host gives us.
func resolveDBPath(dbPath string) string {
	dir := filepath.Dir(dbPath)

	// A bare or "./" prefixed name already points at the working directory, so
	// there is nothing to create and the path is returned untouched.
	if dir == "" || dir == "." {
		if writableDir(".") {
			return dbPath
		}
		log.Printf("⚠️ Working directory is not writable")
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("⚠️ Cannot create database directory %s: %v", dir, err)
	} else if !writableDir(dir) {
		log.Printf("⚠️ Database directory %s is not writable", dir)
	} else {
		log.Printf("🗄️ Database directory ready: %s", dir)
		return dbPath
	}

	// The working directory comes first because that is where the file already
	// lives on most hosts, so the fallback keeps the existing sessions instead of
	// silently starting over somewhere else.
	name := filepath.Base(dbPath)
	for _, cand := range []string{".", "./data", os.TempDir()} {
		if !writableDir(cand) {
			continue
		}
		fallback := filepath.Join(cand, name)
		log.Printf("⚠️ Using %s instead — sessions will not persist if this directory is cleared", fallback)
		return fallback
	}

	log.Fatalf("❌ No writable directory for the database (tried %s, ., ./data, %s)", dir, os.TempDir())
	return dbPath
}

// writableDir proves a directory accepts new files. The permission bits are not
// enough to trust: a read-only mount or a full disk reports no error on MkdirAll
// and then fails on the open that matters.
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func NewStore(dbPath string) *Store {
	dbPath = resolveDBPath(dbPath)

	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{
		Timeout: 2 * time.Second,
		// This DB is small and rewritten constantly; the map freelist keeps
		// lookups cheap and NoFreelistSync skips a freelist write per commit.
		FreelistType:    bbolt.FreelistMapType,
		NoFreelistSync:  true,
		InitialMmapSize: 32 << 20,
		NoSync:          false,
	})
	if err != nil {
		// A lock timeout is not a permissions problem, and saying so sent people
		// off to chmod a directory that was fine while a second copy of the bot
		// held the file.
		if errors.Is(err, bbolt.ErrTimeout) {
			// Deliberately no advice to delete the file: it holds every session,
			// and a stale lock is cleared by stopping the process holding it, not
			// by removing the data.
			log.Fatalf("❌ Database %s is locked by another running instance. "+
				"Stop that instance and start this one again. Deleting the file will not help: "+
				"it discards every stored session.", dbPath)
		}
		log.Fatalf("❌ Failed to open database %s (is the directory %s writable?): %v", dbPath, filepath.Dir(dbPath), err)
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{"users", "sessions", "feedbacks", "groups", "reminders", "queries"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("❌ Failed to create buckets: %v", err)
	}

	return &Store{db: db}
}

func (s *Store) Close() {
	s.db.Close()
}

// GetOrCreate reads a session without taking a write lock. It used to open a
// write transaction for every read, which meant an fsync on nearly every update
// (this is called several times per message) and let the DB mmap creep upward.
func (s *Store) GetOrCreate(userID int64) *SessionData {
	sess := &SessionData{
		Language: "en",
		State:    "idle",
		Data:     make(map[string]interface{}),
		JoinedAt: time.Now().Format(time.RFC3339),
	}

	var found bool
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte("sessions")).Get(itob(userID))
		if data == nil {
			return nil
		}
		found = true
		return json.Unmarshal(data, sess)
	})
	if err != nil {
		log.Printf("session GetOrCreate error: %v", err)
		return &SessionData{Language: "en", State: "idle", Data: make(map[string]interface{}), JoinedAt: sess.JoinedAt}
	}
	if found {
		if sess.Data == nil {
			sess.Data = make(map[string]interface{})
		}
		return sess
	}

	// Two updates for the same brand new user can both read "missing" above, and
	// the one that finishes last would overwrite whatever the other had already
	// stored, losing the state it had set. The existence check is repeated inside
	// the write transaction, so only the first of them creates the session.
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("sessions"))
		if b.Get(itob(userID)) != nil {
			return nil
		}
		encoded, err := json.Marshal(sess)
		if err != nil {
			return err
		}
		return b.Put(itob(userID), encoded)
	}); err != nil {
		log.Printf("session create error: %v", err)
	}
	return sess
}

func (s *Store) saveSession(userID int64, sess *SessionData) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("sessions"))
		encoded, err := json.Marshal(sess)
		if err != nil {
			return err
		}
		return b.Put(itob(userID), encoded)
	})
}

func (s *Store) SetLanguage(userID int64, lang string) {
	sess := s.GetOrCreate(userID)
	sess.Language = lang
	if err := s.saveSession(userID, sess); err != nil {
		log.Printf("session save error: %v", err)
	}
}

func (s *Store) SetState(userID int64, state string) {
	sess := s.GetOrCreate(userID)
	sess.State = state
	if err := s.saveSession(userID, sess); err != nil {
		log.Printf("session save error: %v", err)
	}
}

func (s *Store) SetSessionData(userID int64, data map[string]interface{}) {
	sess := s.GetOrCreate(userID)
	sess.Data = data
	if err := s.saveSession(userID, sess); err != nil {
		log.Printf("session save error: %v", err)
	}
}

// ClearSessionData drops transient payloads (API blobs, cached URLs) that would
// otherwise sit in the DB until the session expires.
func (s *Store) ClearSessionData(userID int64) {
	sess := s.GetOrCreate(userID)
	sess.Data = make(map[string]interface{})
	if err := s.saveSession(userID, sess); err != nil {
		log.Printf("session save error: %v", err)
	}
}

func (s *Store) TrackUser(id int64, firstName, lastName, username string) {
	now := time.Now().Format(time.RFC3339)
	name := firstName
	if lastName != "" {
		name = firstName + " " + lastName
	}

	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("users"))
		data := b.Get(itob(id))
		if data == nil {
			u := &UserData{
				ID:        id,
				Name:      name,
				Username:  username,
				FirstSeen: now,
				LastSeen:  now,
			}
			encoded, err := json.Marshal(u)
			if err != nil {
				return err
			}
			return b.Put(itob(id), encoded)
		}
		var u UserData
		if err := json.Unmarshal(data, &u); err != nil {
			return err
		}
		u.LastSeen = now
		u.Name = name
		u.Username = username
		encoded, err := json.Marshal(&u)
		if err != nil {
			return err
		}
		return b.Put(itob(id), encoded)
	})
	if err != nil {
		log.Printf("TrackUser error: %v", err)
	}
}

func (s *Store) AddFeedback(userID int64, message string) {
	f := Feedback{
		UserID:    userID,
		Message:   message,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		log.Printf("AddFeedback marshal error: %v", err)
		return
	}

	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("feedbacks"))
		id, _ := b.NextSequence()
		return b.Put(itob(int64(id)), encoded)
	})
	if err != nil {
		log.Printf("AddFeedback error: %v", err)
	}
}

func (s *Store) GetStats() (users, feedbacks int) {
	s.db.View(func(tx *bbolt.Tx) error {
		users = tx.Bucket([]byte("users")).Stats().KeyN
		feedbacks = tx.Bucket([]byte("feedbacks")).Stats().KeyN
		return nil
	})
	return
}

func (s *Store) GetFeedbacks() []Feedback {
	var feedbacks []Feedback
	s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("feedbacks"))
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var f Feedback
			if err := json.Unmarshal(v, &f); err != nil {
				continue
			}
			feedbacks = append(feedbacks, f)
		}
		return nil
	})
	return feedbacks
}

func (s *Store) GetGroup(chatID int64) *GroupConfig {
	g := &GroupConfig{ChatID: chatID}
	s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte("groups")).Get(itob(chatID))
		if data == nil {
			return nil
		}
		return json.Unmarshal(data, g)
	})
	return g
}

func (s *Store) SetGroup(g *GroupConfig) {
	encoded, err := json.Marshal(g)
	if err != nil {
		return
	}
	s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("groups")).Put(itob(g.ChatID), encoded)
	})
}

func (s *Store) ListGroups() []*GroupConfig {
	var out []*GroupConfig
	s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte("groups")).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var g GroupConfig
			if err := json.Unmarshal(v, &g); err == nil {
				out = append(out, &g)
			}
		}
		return nil
	})
	return out
}

func (s *Store) AddReminder(r *Reminder) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("reminders"))
		id, err := b.NextSequence()
		if err != nil {
			return err
		}
		r.ID = int64(id)
		encoded, err = json.Marshal(r)
		if err != nil {
			return err
		}
		return b.Put(itob(r.ID), encoded)
	})
}

func (s *Store) ListReminders(userID int64) []Reminder {
	var out []Reminder
	s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte("reminders")).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var r Reminder
			if err := json.Unmarshal(v, &r); err != nil {
				continue
			}
			if r.UserID == userID {
				out = append(out, r)
			}
		}
		return nil
	})
	return out
}

func (s *Store) DueReminders(now int64) []Reminder {
	var out []Reminder
	s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte("reminders")).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var r Reminder
			if err := json.Unmarshal(v, &r); err != nil {
				continue
			}
			if r.DueAt <= now {
				out = append(out, r)
			}
		}
		return nil
	})
	return out
}

func (s *Store) DeleteReminder(id int64) {
	s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("reminders")).Delete(itob(id))
	})
}

func (s *Store) LogQuery(userID int64, command, input string) {
	q := QueryLog{
		UserID:    userID,
		Command:   command,
		Input:     truncate(input, 120),
		Timestamp: time.Now().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(q)
	if err != nil {
		return
	}
	s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("queries"))
		id, err := b.NextSequence()
		if err != nil {
			return err
		}
		if b.Stats().KeyN > 200 {
			c := b.Cursor()
			if k, _ := c.First(); k != nil {
				b.Delete(k)
			}
		}
		return b.Put(itob(int64(id)), encoded)
	})
}

func (s *Store) RecentQueries(userID int64, limit int) []QueryLog {
	var out []QueryLog
	s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte("queries")).Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var q QueryLog
			if err := json.Unmarshal(v, &q); err != nil {
				continue
			}
			if q.UserID == userID {
				out = append(out, q)
				if len(out) >= limit {
					break
				}
			}
		}
		return nil
	})
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func (s *Store) Cleanup() {
	now := time.Now()
	s.db.Update(func(tx *bbolt.Tx) error {
		feedbackBucket := tx.Bucket([]byte("feedbacks"))
		feedbackCount := feedbackBucket.Stats().KeyN
		if feedbackCount > 500 {
			c := feedbackBucket.Cursor()
			toDelete := feedbackCount - 500
			for k, _ := c.First(); k != nil && toDelete > 0; k, _ = c.First() {
				feedbackBucket.Delete(k)
				toDelete--
			}
			log.Printf("Cleaned up %d old feedbacks", feedbackCount-500)
		}

		sessionBucket := tx.Bucket([]byte("sessions"))
		c := sessionBucket.Cursor()
		deleted := 0
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var sess SessionData
			if err := json.Unmarshal(v, &sess); err != nil {
				continue
			}
			joined, err := time.Parse(time.RFC3339, sess.JoinedAt)
			if err != nil {
				continue
			}
			if now.Sub(joined) > 90*24*time.Hour {
				sessionBucket.Delete(k)
				deleted++
			}
		}
		if deleted > 0 {
			log.Printf("Cleaned up %d old sessions", deleted)
		}

		reminderBucket := tx.Bucket([]byte("reminders"))
		rc := reminderBucket.Cursor()
		nowUnix := time.Now().Unix()
		rdeleted := 0
		for k, v := rc.First(); k != nil; k, v = rc.Next() {
			var r Reminder
			if err := json.Unmarshal(v, &r); err != nil {
				continue
			}
			if r.DueAt < nowUnix-86400 {
				reminderBucket.Delete(k)
				rdeleted++
			}
		}
		if rdeleted > 0 {
			log.Printf("Cleaned up %d fired reminders", rdeleted)
		}

		return nil
	})
}

func itob(v int64) []byte {
	return []byte(fmt.Sprintf("%020d", v))
}
