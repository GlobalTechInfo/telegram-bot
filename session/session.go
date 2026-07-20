package session

import (
	"encoding/json"
	"fmt"
	"log"
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

type Store struct {
	db *bbolt.DB
}

func NewStore(dbPath string) *Store {
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatalf("❌ Failed to open database %s: %v", dbPath, err)
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{"users", "sessions", "feedbacks"} {
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

func (s *Store) GetOrCreate(userID int64) *SessionData {
	sess := &SessionData{
		Language: "en",
		State:    "idle",
		Data:     make(map[string]interface{}),
		JoinedAt: time.Now().Format(time.RFC3339),
	}

	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("sessions"))
		data := b.Get(itob(userID))
		if data == nil {
			encoded, err := json.Marshal(sess)
			if err != nil {
				return err
			}
			return b.Put(itob(userID), encoded)
		}
		return json.Unmarshal(data, sess)
	})
	if err != nil {
		log.Printf("session GetOrCreate error: %v", err)
		sess.Language = "en"
		sess.State = "idle"
		sess.Data = make(map[string]interface{})
	}
	return sess
}

func (s *Store) saveSession(userID int64, sess *SessionData) {
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("sessions"))
		encoded, err := json.Marshal(sess)
		if err != nil {
			return err
		}
		return b.Put(itob(userID), encoded)
	})
	if err != nil {
		log.Printf("session save error: %v", err)
	}
}

func (s *Store) SetLanguage(userID int64, lang string) {
	sess := s.GetOrCreate(userID)
	sess.Language = lang
	s.saveSession(userID, sess)
}

func (s *Store) SetState(userID int64, state string) {
	sess := s.GetOrCreate(userID)
	sess.State = state
	s.saveSession(userID, sess)
}

func (s *Store) SetSessionData(userID int64, data map[string]interface{}) {
	sess := s.GetOrCreate(userID)
	sess.Data = data
	s.saveSession(userID, sess)
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

		return nil
	})
}

func itob(v int64) []byte {
	return []byte(fmt.Sprintf("%020d", v))
}
