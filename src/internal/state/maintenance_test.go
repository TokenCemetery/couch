package state

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMaintenance(t *testing.T) {
	s, dir := testStore(t)
	on, off := true, false
	if d := s.Snapshot(); d.VacuumOnStartup || !d.OptimizeOnShutdown {
		t.Fatalf("defaults: vacuum %v, optimize %v; want Emby's false, true", d.VacuumOnStartup, d.OptimizeOnShutdown)
	}
	file := filepath.Join(dir, "couch.db")
	size := func() int64 {
		t.Helper()
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	free := func(s *Store) int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(t.Context(), "PRAGMA freelist_count").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Deleting a large folder of the media index leaves free pages.
	at := time.Now().UTC()
	entries := make([]CatalogEntry, 2000)
	for n := range entries {
		entries[n] = CatalogEntry{Folder: "/media", Path: strconv.Itoa(n) + ".mkv", Modified: at, Added: at, Probe: strings.Repeat("x", 1000)}
	}
	if err := s.ChangeCatalog(t.Context(), CatalogChange{Folders: []string{"/media"}, Write: entries}); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeCatalog(t.Context(), CatalogChange{Drop: []string{"/media"}}); err != nil {
		t.Fatal(err)
	}
	if free(s) == 0 {
		t.Fatal("no free pages to reclaim")
	}

	// Without the setting nothing runs.
	before := size()
	if ran, _, _, err := s.Vacuum(t.Context()); ran || err != nil || size() != before {
		t.Fatalf("vacuum without the setting: ran %v, %v, size %d → %d", ran, err, before, size())
	}

	// With it, one vacuum empties the free list, shrinks the file and clears
	// the request.
	if err := s.SetMaintenance(&on, &on); err != nil {
		t.Fatal(err)
	}
	ran, reported, after, err := s.Vacuum(t.Context())
	if !ran || err != nil || free(s) != 0 || after >= reported || after != size() {
		t.Fatalf("vacuum: ran %v, %v, free %d, size %d → %d", ran, err, free(s), reported, after)
	}
	if d := s.Snapshot(); d.VacuumOnStartup || !d.OptimizeOnShutdown {
		t.Fatalf("after the vacuum: vacuum %v, optimize %v", d.VacuumOnStartup, d.OptimizeOnShutdown)
	}

	// A vacuum that fails was cleared before it ran, so it is not repeated,
	// and the state is kept.
	if err := s.SetMaintenance(&on, &on); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if ran, _, _, err := s.Vacuum(cancelled); !ran || err == nil {
		t.Fatalf("failed vacuum: ran %v, %v", ran, err)
	}
	if d := s.Snapshot(); d.VacuumOnStartup || len(d.Users) != 1 {
		t.Fatalf("after a failed vacuum: vacuum %v, %d users", d.VacuumOnStartup, len(d.Users))
	}

	if ran, err := s.Optimize(t.Context()); !ran || err != nil {
		t.Fatalf("optimize: ran %v, %v", ran, err)
	}
	if err := s.SetMaintenance(nil, &off); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.Optimize(t.Context()); ran || err != nil {
		t.Fatalf("optimize turned off: ran %v, %v", ran, err)
	}
	// A setting left out is kept.
	if err := s.SetMaintenance(&on, nil); err != nil {
		t.Fatal(err)
	}
	if d := s.Snapshot(); !d.VacuumOnStartup || d.OptimizeOnShutdown {
		t.Fatalf("one setting changed: vacuum %v, optimize %v", d.VacuumOnStartup, d.OptimizeOnShutdown)
	}

	// The settings survive a backup and a restart.
	dest := filepath.Join(t.TempDir(), "backup")
	if err := Backup(dir, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if d := restored.Snapshot(); !d.VacuumOnStartup || d.OptimizeOnShutdown {
		t.Fatalf("backup: vacuum %v, optimize %v", d.VacuumOnStartup, d.OptimizeOnShutdown)
	}

	// A database from before the settings gets Emby's defaults.
	if _, err := s.db.ExecContext(t.Context(), "ALTER TABLE server DROP COLUMN vacuum_on_startup; ALTER TABLE server DROP COLUMN optimize_on_shutdown; PRAGMA user_version = 11"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if d := s.Snapshot(); d.VacuumOnStartup || !d.OptimizeOnShutdown {
		t.Fatalf("migrated: vacuum %v, optimize %v", d.VacuumOnStartup, d.OptimizeOnShutdown)
	}
}
