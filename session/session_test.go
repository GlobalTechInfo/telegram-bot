package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A configured path like /bot/data/bot.db must work on a host that has never had
// that directory: bbolt creates the file, not the tree above it.
func TestNewStoreCreatesParentDirectories(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "bot", "data", "nested", "bot.db")

	store := NewStore(dbPath)
	defer store.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dbPath)); err != nil {
		t.Fatalf("parent directory was not created: %v", err)
	}

	// The store has to be usable, not just present.
	store.SetState(42, "awaiting_yt_url")
	if got := store.GetOrCreate(42).State; got != "awaiting_yt_url" {
		t.Errorf("state = %q", got)
	}
}

func TestNewStoreReopensExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "a", "b", "bot.db")

	first := NewStore(dbPath)
	first.SetLanguage(7, "hi")
	first.Close()

	second := NewStore(dbPath)
	defer second.Close()
	if got := second.GetOrCreate(7).Language; got != "hi" {
		t.Errorf("data did not survive a reopen: %q", got)
	}
}

// A bare filename needs no directory work.
func TestNewStoreAcceptsBareFilename(t *testing.T) {
	t.Chdir(t.TempDir())

	store := NewStore("bot.db")
	defer store.Close()

	if _, err := os.Stat("bot.db"); err != nil {
		t.Errorf("database file was not created: %v", err)
	}
}

// A path that cannot be created must degrade to a working database rather than
// taking the service down with it.
func TestNewStoreFallsBackWhenDirectoryIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permissions do not apply")
	}

	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	work := t.TempDir()
	t.Chdir(work)

	store := NewStore(filepath.Join(locked, "sub", "bot.db"))
	defer store.Close()

	if _, err := os.Stat(filepath.Join(work, "bot.db")); err != nil {
		t.Errorf("expected the fallback database in the working directory: %v", err)
	}
}

func TestResolveDBPathLeavesRelativePathsAlone(t *testing.T) {
	t.Chdir(t.TempDir())

	if got := resolveDBPath("data/bot.db"); got != "data/bot.db" {
		t.Errorf("got %q", got)
	}
	if got := resolveDBPath("bot.db"); got != "bot.db" {
		t.Errorf("got %q", got)
	}
	if got := resolveDBPath("./bot.db"); got != "./bot.db" {
		t.Errorf("got %q", got)
	}
}

// Two updates for the same new user can both observe the session as missing. The
// second one must not overwrite state the first already stored.
func TestGetOrCreateDoesNotClobberConcurrentCreation(t *testing.T) {
	for i := 0; i < 25; i++ {
		store := NewStore(filepath.Join(t.TempDir(), "race.db"))
		id := int64(9000 + i)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			sess := store.GetOrCreate(id)
			sess.Data["state_saved"] = true
			store.saveSession(id, sess)
		}()
		go func() {
			defer wg.Done()
			store.GetOrCreate(id) // the default-session write that used to win
		}()
		wg.Wait()

		sess := store.GetOrCreate(id)
		if sess.Data["state_saved"] != true {
			t.Fatalf("iteration %d: the default session write erased stored state", i)
		}
		store.Close()
	}
}
