package state

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A failed change must not remove the file the committed state references,
// even when the upload has the same content as the current avatar.
func TestUserImageFailedChangeKeepsCommittedFile(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("not decoded by the store")
	if err := s.SetUserImage(token, content, "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(token); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserImage(token, content, "image/png"); !errors.Is(err, ErrSession) {
		t.Fatalf("re-upload after logout: %v", err)
	}
	if err := s.SetUserImage(token, []byte("other"), "image/png"); !errors.Is(err, ErrSession) {
		t.Fatalf("new upload after logout: %v", err)
	}
	if err := s.DeleteUserImage(token); !errors.Is(err, ErrSession) {
		t.Fatalf("delete after logout: %v", err)
	}
	file, _, err := s.OpenUserImage(viewer(s.Snapshot()).ID)
	if err != nil {
		t.Fatal("committed avatar file was removed:", err)
	}
	_ = file.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "user-image-*"))
	if len(files) != 1 {
		t.Fatalf("avatar files = %d, want 1 (rejected upload must be cleaned up)", len(files))
	}
	if _, err := os.Stat(filepath.Join(dir, "couch.db")); err != nil {
		t.Fatal(err)
	}
}

func imageFiles(t *testing.T, dir string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "user-image-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func readImage(t *testing.T, s *Store, image *UserImage) string {
	t.Helper()
	file, _, err := s.OpenImage(image)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestCollectionAndPlaylistImages stores, replaces, shares and removes the
// images of a collection and a playlist, keeps them across a reopen and a
// backup, and removes a file once nothing references it: on delete, on the
// deletion of the collection or playlist, and on the deletion of its user.
func TestCollectionAndPlaylistImages(t *testing.T) {
	s, dir := testStore(t)
	owner, _ := s.Snapshot().UserByName("viewer")
	other, err := s.CreateUser("other", Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateCollection("Saga", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePlaylist(owner.ID, "Evening", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.CreatePlaylist(other.ID, "Theirs", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollectionImage(c.ID, []byte("poster"), UserImage{MIME: "image/jpeg", Width: 2, Height: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlaylistImage(owner.ID, p.ID, []byte("poster"), UserImage{MIME: "image/jpeg", Width: 2, Height: 3}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Snapshot().Playlist(p.ID); !got.Modified.Equal(p.Modified) {
		t.Fatalf("image upload changed the playlist's modification time: %v, was %v", got.Modified, p.Modified)
	}
	if err := s.SetPlaylistImage(owner.ID, theirs.ID, []byte("mine"), UserImage{MIME: "image/png", Width: 2, Height: 3}); !errors.Is(err, ErrPlaylistNotFound) {
		t.Fatalf("image of another user's playlist: %v", err)
	}
	if err := s.SetCollectionImage("missing", []byte("lost"), UserImage{MIME: "image/png", Width: 2, Height: 3}); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("image of a missing collection: %v", err)
	}
	if n := imageFiles(t, dir); n != 1 {
		t.Fatalf("files after the same upload twice and two refused ones = %d, want 1", n)
	}
	if err := s.SetPlaylistImage(other.ID, theirs.ID, []byte("theirs"), UserImage{MIME: "image/png", Width: 2, Height: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d := s.Snapshot()
	c, _ = d.Collection(c.ID)
	p, _ = d.Playlist(p.ID)
	if c.Image == nil || c.Image.MIME != "image/jpeg" || c.Image.Width != 2 || c.Image.Height != 3 || readImage(t, s, c.Image) != "poster" || p.Image == nil || *p.Image != *c.Image {
		t.Fatalf("reopened: collection %+v, playlist %+v", c.Image, p.Image)
	}
	dest := filepath.Join(t.TempDir(), "backup")
	if err := Backup(dir, dest); err != nil {
		t.Fatal(err)
	}
	if n := imageFiles(t, dest); n != 2 {
		t.Fatalf("backed up image files = %d, want 2", n)
	}
	// The playlist keeps the shared file after the collection's changes.
	if err := s.SetCollectionImage(c.ID, []byte("new poster"), UserImage{MIME: "image/png", Width: 2, Height: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollectionImage(c.ID, nil, UserImage{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Snapshot().Collection(c.ID); c.Image != nil {
		t.Fatalf("deleted collection image: %+v", c.Image)
	}
	if readImage(t, s, p.Image) != "poster" || imageFiles(t, dir) != 2 {
		t.Fatalf("files after replacing and deleting the collection image = %d, want 2", imageFiles(t, dir))
	}
	if err := s.SetCollectionImage(c.ID, []byte("again"), UserImage{MIME: "image/png", Width: 2, Height: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCollections([]string{c.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlaylists(owner.ID, []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	if n := imageFiles(t, dir); n != 1 {
		t.Fatalf("files after deleting the collection and the playlist = %d, want 1", n)
	}
	if _, err := s.DeleteUser(other.ID); err != nil {
		t.Fatal(err)
	}
	if n := imageFiles(t, dir); n != 0 {
		t.Fatalf("files after deleting the last playlist's user = %d, want 0", n)
	}
}
