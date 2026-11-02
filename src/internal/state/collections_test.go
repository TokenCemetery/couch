package state

import (
	"errors"
	"slices"
	"testing"
)

// TestCollections edits a collection, reopens the store and checks that a
// version 7 database gains the collections table.
func TestCollections(t *testing.T) {
	s, dir := testStore(t)
	c, err := s.CreateCollection("  Trilogy ", []string{"a", "b", "a"})
	if err != nil || c.Name != "Trilogy" || !slices.Equal(c.Items, []string{"a", "b"}) {
		t.Fatalf("create: %+v %v", c, err)
	}
	if _, err := s.CreateCollection(" ", nil); !errors.Is(err, ErrCollectionName) {
		t.Fatalf("empty name: %v", err)
	}
	if added, err := s.AddToCollection(c.ID, []string{"b", "c", "d"}); err != nil || added != 2 {
		t.Fatalf("add: %d %v", added, err)
	}
	if err := s.RemoveFromCollection(c.ID, []string{"a", "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameCollection(c.ID, "Saga"); err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateCollection("Other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddToCollection("missing", []string{"a"}); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("add to missing: %v", err)
	}
	if err := s.DeleteCollections([]string{other.ID, "missing"}); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("delete with missing: %v", err)
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
	if len(d.Collections) != 2 || d.Collections[0].Name != "Saga" || !slices.Equal(d.Collections[0].Items, []string{"b", "c", "d"}) ||
		!d.Collections[0].Added.Equal(c.Added) || d.Collections[1].Items == nil {
		t.Fatalf("reopened: %+v", d.Collections)
	}
	if err := s.DeleteCollections([]string{other.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "DROP TABLE collections; DROP TABLE playlists; PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Snapshot(); len(d.Collections) != 0 {
		t.Fatalf("migrated: %+v", d.Collections)
	}
	if _, err := s.CreateCollection("After", []string{"a"}); err != nil {
		t.Fatal(err)
	}
}
