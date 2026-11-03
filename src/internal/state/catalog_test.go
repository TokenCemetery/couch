package state

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCatalog(t *testing.T) {
	s, dir := testStore(t)
	if index, err := s.Catalog(t.Context()); err != nil || len(index.Folders) != 0 || len(index.Entries) != 0 {
		t.Fatalf("new database: %v %+v", err, index)
	}
	at := time.Date(2026, 11, 3, 10, 0, 0, 123456789, time.UTC)
	a := CatalogEntry{Folder: "/media", Path: "A.mkv", Size: 1, Modified: at, Added: at, Probe: `{"RunTimeTicks":1}`}
	b := CatalogEntry{Folder: "/media", Path: "B.mkv", Size: 2, Modified: at, Added: at.Add(time.Hour), Reprobe: true, Probe: `{"RunTimeTicks":2}`}
	c := CatalogEntry{Folder: "/music", Path: "C.flac", Size: 3, Modified: at, Added: at, Probe: `{}`}
	// changes counts the rows a change writes or deletes.
	changes := func(change CatalogChange) int {
		t.Helper()
		var before, after int
		if err := s.db.QueryRowContext(t.Context(), "SELECT total_changes()").Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err := s.ChangeCatalog(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRowContext(t.Context(), "SELECT total_changes()").Scan(&after); err != nil {
			t.Fatal(err)
		}
		return after - before
	}
	saved := func(s *Store) CatalogIndex {
		t.Helper()
		index, err := s.Catalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(index.Folders)
		slices.SortFunc(index.Entries, func(a, b CatalogEntry) int { return strings.Compare(a.Folder+a.Path, b.Folder+b.Path) })
		for n, e := range index.Entries {
			if !e.Modified.Equal(at) {
				t.Fatalf("modified %v, want %v", e.Modified, at)
			}
			index.Entries[n].Modified, index.Entries[n].Added = at, e.Added.UTC()
		}
		return index
	}
	want := func(folders []string, entries ...CatalogEntry) CatalogIndex {
		return CatalogIndex{Folders: folders, Entries: entries}
	}

	// A folder without files is recorded.
	if n := changes(CatalogChange{Folders: []string{"/empty", "/media", "/music"}, Write: []CatalogEntry{a, b, c}}); n != 6 {
		t.Fatalf("first save changed %d rows", n)
	}
	if got := saved(s); !reflect.DeepEqual(got, want([]string{"/empty", "/media", "/music"}, a, b, c)) {
		t.Fatalf("saved %+v", got)
	}
	b.Probe = `{"RunTimeTicks":20}`
	if n := changes(CatalogChange{Folders: []string{"/media"}, Write: []CatalogEntry{b}, Delete: []CatalogEntry{{Folder: "/media", Path: "A.mkv"}}}); n != 2 {
		t.Fatalf("one changed and one removed file changed %d rows", n)
	}
	// Dropping a folder removes its entries; others are left alone.
	if n := changes(CatalogChange{Drop: []string{"/music"}}); n != 2 {
		t.Fatalf("dropping a folder changed %d rows", n)
	}
	if got := saved(s); !reflect.DeepEqual(got, want([]string{"/empty", "/media"}, b)) {
		t.Fatalf("saved %+v", got)
	}

	// A backup holds the index.
	dest := filepath.Join(t.TempDir(), "backup")
	if err := Backup(dir, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if got := saved(restored); !reflect.DeepEqual(got, want([]string{"/empty", "/media"}, b)) {
		t.Fatalf("backup holds %+v", got)
	}

	// A database written before the index was stored is migrated on open.
	if _, err := s.db.ExecContext(t.Context(), "ALTER TABLE collections DROP COLUMN image_revision; ALTER TABLE collections DROP COLUMN image_mime; ALTER TABLE collections DROP COLUMN image_width; ALTER TABLE collections DROP COLUMN image_height; ALTER TABLE playlists DROP COLUMN image_revision; ALTER TABLE playlists DROP COLUMN image_mime; ALTER TABLE playlists DROP COLUMN image_width; ALTER TABLE playlists DROP COLUMN image_height; ALTER TABLE collections DROP COLUMN set_key; ALTER TABLE collections DROP COLUMN set_items; ALTER TABLE collections DROP COLUMN declined; DROP TABLE dismissed_sets; ALTER TABLE libraries DROP COLUMN realtime_monitor; DROP TABLE catalog; DROP TABLE catalog_folders; ALTER TABLE server DROP COLUMN vacuum_on_startup; ALTER TABLE server DROP COLUMN optimize_on_shutdown; PRAGMA user_version = 10"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := saved(s); len(got.Folders) != 0 || len(got.Entries) != 0 {
		t.Fatalf("migrated database holds %+v", got)
	}
}
