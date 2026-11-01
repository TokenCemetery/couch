package state

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBackupWhileOpen(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserImage(token, []byte("avatar"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem(token, "a", func(st *ItemState) { st.IsFavorite = true }); err != nil {
		t.Fatal(err)
	}
	// Progress reports keep committing while the backup runs.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := int64(1); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.SetItem(token, "b", func(st *ItemState) { st.PositionTicks = i }); err != nil {
				t.Error(err)
				return
			}
		}
	})
	dest := filepath.Join(t.TempDir(), "backup")
	err = Backup(dir, dest)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// Later changes are not in the copy.
	if err := s.DeleteUserImage(token); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if _, err := restored.Authenticate(token); err != nil {
		t.Fatal("backup lost the session:", err)
	}
	if !viewer(restored.Snapshot()).Items["a"].IsFavorite {
		t.Fatal("backup lost the favorite")
	}
	file, _, err := restored.OpenUserImage(viewer(restored.Snapshot()).ID)
	if err != nil {
		t.Fatal("backup lost the avatar:", err)
	}
	defer func() { _ = file.Close() }()
	if content, err := io.ReadAll(file); err != nil || !bytes.Equal(content, []byte("avatar")) {
		t.Fatalf("avatar %q, %v", content, err)
	}
	info, err := os.Stat(filepath.Join(dest, "couch.db"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup database: %v, %v", info, err)
	}
}

func TestBackupRefusesBadInput(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Change(token, func(u *User, _ *Session) { u.Settings["theme"] = json.RawMessage(`"dark"`) }); err != nil {
		t.Fatal(err)
	}
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "kept"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Backup(dir, full); err == nil {
		t.Fatal("backup into a non-empty directory")
	}
	if err := Backup(t.TempDir(), filepath.Join(t.TempDir(), "backup")); err == nil {
		t.Fatal("backup of a directory without a database")
	}
	uninitialized := t.TempDir()
	empty, err := Open(uninitialized)
	if err != nil {
		t.Fatal(err)
	}
	_ = empty.Close()
	if err := Backup(uninitialized, filepath.Join(t.TempDir(), "backup")); err == nil {
		t.Fatal("backup of an uninitialized database")
	}
}
