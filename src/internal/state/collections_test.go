package state

import (
	"errors"
	"reflect"
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
	if _, err := s.db.ExecContext(t.Context(), "ALTER TABLE collections DROP COLUMN set_key; ALTER TABLE collections DROP COLUMN set_items; ALTER TABLE collections DROP COLUMN declined; DROP TABLE dismissed_sets; ALTER TABLE libraries DROP COLUMN realtime_monitor; DROP TABLE catalog; DROP TABLE catalog_folders; ALTER TABLE server DROP COLUMN vacuum_on_startup; ALTER TABLE server DROP COLUMN optimize_on_shutdown; DROP TABLE collections; DROP TABLE playlists; ALTER TABLE users DROP COLUMN home_sections; PRAGMA user_version = 7"); err != nil {
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

// TestSyncSets follows movie sets through the collections they create:
// linking a .nfo name with a TMDb ID, a renamed set, new, leaving and
// pending members, an administrator's own changes, reopening, and deleting.
func TestSyncSets(t *testing.T) {
	s, dir := testStore(t)
	sync := func(sets map[string]MovieSet) []Collection {
		t.Helper()
		created, err := s.SyncSets(sets)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	named := func(name string) MovieSet { return MovieSet{Name: name} }
	if created := sync(map[string]MovieSet{"a": named("Saga"), "x": named("Single"), "s": {}}); created != nil || len(s.Snapshot().Collections) != 0 {
		t.Fatalf("one movie per set: %v %+v", created, s.Snapshot().Collections)
	}
	// b names the set by another spelling with its TMDb ID, which c shares.
	created := sync(map[string]MovieSet{"a": named("Saga"), "b": {Name: "saga ", TmdbID: "10"}, "c": {Name: "Saga Collection", TmdbID: "10"}})
	if len(created) != 1 || created[0].Name != "Saga" || created[0].Set != "tmdb:10" {
		t.Fatalf("created %+v", created)
	}
	c := s.Snapshot().Collections[0]
	if !slices.Equal(c.Items, []string{"a", "b", "c"}) || !slices.Equal(c.SetItems, c.Items) {
		t.Fatalf("collection %+v", c)
	}
	// The administrator renames it, adds m by hand and removes a.
	if err := s.RenameCollection(c.ID, "My Saga"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddToCollection(c.ID, []string{"m"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFromCollection(c.ID, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	// In another language the TMDb name changes, but not the ID. m joins
	// the set; d joins; b leaves; c waits for its lookup; e is unknown.
	sets := map[string]MovieSet{"a": {Name: "Сага", TmdbID: "10"}, "b": {}, "c": {Pending: true}, "d": {Name: "Сага", TmdbID: "10"}, "m": {Name: "Сага", TmdbID: "10"}}
	if created := sync(sets); created != nil {
		t.Fatalf("created %+v", created)
	}
	c = s.Snapshot().Collections[0]
	if c.Name != "My Saga" || c.Set != "tmdb:10" || !slices.Equal(c.Items, []string{"c", "m", "d"}) ||
		!slices.Equal(c.SetItems, []string{"c", "d"}) || !slices.Equal(c.Declined, []string{"a"}) {
		t.Fatalf("synced %+v", c)
	}
	before := s.Snapshot().Collections
	if created := sync(sets); created != nil || !reflect.DeepEqual(s.Snapshot().Collections, before) {
		t.Fatalf("unchanged sync: %v %+v", created, s.Snapshot().Collections)
	}
	// m, added by hand, stays when it leaves the set; a, declined, is
	// forgotten once it leaves.
	if created := sync(map[string]MovieSet{"a": {}, "c": {}, "d": named("Сага"), "m": {}}); created != nil {
		t.Fatalf("created %+v", created)
	}
	c = s.Snapshot().Collections[0]
	if !slices.Equal(c.Items, []string{"m"}) || len(c.SetItems) != 0 || len(c.Declined) != 0 || c.Set != "tmdb:10" {
		t.Fatalf("after leaving %+v", c)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := s.Snapshot().Collections; len(got) != 1 || got[0].Set != "tmdb:10" || !slices.Equal(got[0].Items, []string{"m"}) {
		t.Fatalf("reopened %+v", got)
	}
	// A deleted set collection is not created again, even after reopening.
	if err := s.DeleteCollections([]string{c.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if d := s.Snapshot(); !slices.Equal(d.DismissedSets, []string{"tmdb:10"}) {
		t.Fatalf("dismissed %v", d.DismissedSets)
	}
	if created := sync(map[string]MovieSet{"a": named("Saga"), "c": {Name: "Saga", TmdbID: "10"}, "e": named("saga")}); created != nil || len(s.Snapshot().Collections) != 0 {
		t.Fatalf("dismissed set recreated: %+v", created)
	}
}

// TestDeclinedPersists checks that a declined member is stored.
func TestDeclinedPersists(t *testing.T) {
	s, dir := testStore(t)
	if _, err := s.SyncSets(map[string]MovieSet{"a": {Name: "Saga"}, "b": {Name: "Saga"}}); err != nil {
		t.Fatal(err)
	}
	id := s.Snapshot().Collections[0].ID
	if err := s.RemoveFromCollection(id, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if c := s.Snapshot().Collections[0]; !slices.Equal(c.Declined, []string{"a"}) || !slices.Equal(c.SetItems, []string{"b"}) {
		t.Fatalf("reopened %+v", c)
	}
	if _, err := s.SyncSets(map[string]MovieSet{"a": {Name: "Saga"}, "b": {Name: "Saga"}}); err != nil {
		t.Fatal(err)
	}
	if c := s.Snapshot().Collections[0]; !slices.Equal(c.Items, []string{"b"}) {
		t.Fatalf("declined member added again: %+v", c)
	}
}
