package state

import (
	"errors"
	"slices"
	"testing"
)

func entryItems(p Playlist) []string {
	items := []string{}
	for _, e := range p.Entries {
		items = append(items, e.ItemID)
	}
	return items
}

// TestPlaylists edits a user's playlist, checks that other users cannot touch
// it, reopens the store, and checks that a version 8 database gains the
// playlists table and that deleting a user deletes their playlists.
func TestPlaylists(t *testing.T) {
	s, dir := testStore(t)
	owner, _ := s.Snapshot().UserByName("viewer")
	other, err := s.CreateUser("other", Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePlaylist(owner.ID, "  Evening ", []string{"a", "b", "a"})
	if err != nil || p.Name != "Evening" || !slices.Equal(entryItems(p), []string{"a", "b", "a"}) || p.Entries[0].ID == p.Entries[2].ID {
		t.Fatalf("create: %+v %v", p, err)
	}
	if _, err := s.CreatePlaylist(owner.ID, " ", nil); !errors.Is(err, ErrPlaylistName) {
		t.Fatalf("empty name: %v", err)
	}
	if added, err := s.AddToPlaylist(owner.ID, p.ID, []string{"b", "c"}, true); err != nil || added != 1 {
		t.Fatalf("add skipping duplicates: %d %v", added, err)
	}
	if added, err := s.AddToPlaylist(owner.ID, p.ID, []string{"b"}, false); err != nil || added != 1 {
		t.Fatalf("add duplicate: %d %v", added, err)
	}
	p, _ = s.Snapshot().Playlist(p.ID)
	if !slices.Equal(entryItems(p), []string{"a", "b", "a", "c", "b"}) {
		t.Fatalf("after add: %v", entryItems(p))
	}
	// The second "a" moves to the front, the first "b" past the end.
	if err := s.MovePlaylistEntry(owner.ID, p.ID, p.Entries[2].ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.MovePlaylistEntry(owner.ID, p.ID, p.Entries[1].ID, 99); err != nil {
		t.Fatal(err)
	}
	if err := s.MovePlaylistEntry(owner.ID, p.ID, "missing", 0); !errors.Is(err, ErrPlaylistNotFound) {
		t.Fatalf("move missing entry: %v", err)
	}
	if err := s.RemoveFromPlaylist(owner.ID, p.ID, []string{p.Entries[0].ID, "missing"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenamePlaylist(owner.ID, p.ID, "Night"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"add":    func() error { _, err := s.AddToPlaylist(other.ID, p.ID, []string{"x"}, false); return err }(),
		"remove": s.RemoveFromPlaylist(other.ID, p.ID, nil),
		"rename": s.RenamePlaylist(other.ID, p.ID, "Mine"),
		"delete": s.DeletePlaylists(other.ID, []string{p.ID}),
	} {
		if !errors.Is(err, ErrPlaylistNotFound) {
			t.Fatalf("%s by another user: %v", name, err)
		}
	}
	theirs, err := s.CreatePlaylist(other.ID, "Theirs", []string{"z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d := s.Snapshot()
	got, ok := d.UserPlaylist(owner.ID, p.ID)
	if !ok || got.Name != "Night" || !slices.Equal(entryItems(got), []string{"a", "c", "b", "b"}) || !got.Added.Equal(p.Added) || got.Modified.Before(p.Modified) {
		t.Fatalf("reopened: %+v", got)
	}
	if _, ok := d.UserPlaylist(owner.ID, theirs.ID); ok || len(d.Playlists) != 2 {
		t.Fatalf("ownership after reopen: %+v", d.Playlists)
	}
	if _, err := s.DeleteUser(other.ID); err != nil {
		t.Fatal(err)
	}
	if d := s.Snapshot(); len(d.Playlists) != 1 || d.Playlists[0].ID != p.ID {
		t.Fatalf("after deleting the other user: %+v", d.Playlists)
	}
	if err := s.DeletePlaylists(owner.ID, []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "DROP TABLE playlists; PRAGMA user_version = 8"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Snapshot(); len(d.Playlists) != 0 {
		t.Fatalf("migrated: %+v", d.Playlists)
	}
	if _, err := s.CreatePlaylist(owner.ID, "After", []string{"a"}); err != nil {
		t.Fatal(err)
	}
}
