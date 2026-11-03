package state

import (
	"errors"
	"slices"
	"testing"
)

// TestHomeSections stores a home screen, checks that it and an emptied one
// survive a reopen, that a version 9 database gains the column with no home
// screen stored, and that copying the configuration copies the home screen.
func TestHomeSections(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	if viewer(s.Snapshot()).HomeSections != nil {
		t.Fatal("a new user has a stored home screen")
	}
	played := false
	err = s.ChangeHomeSections(token, func(current []HomeSection) ([]HomeSection, error) {
		if current != nil {
			t.Fatalf("current: %+v", current)
		}
		return []HomeSection{
			{ID: "resume", SectionType: "resume", IncludeNextUpInResume: true},
			{SectionType: "items", ItemTypes: []string{"Movie"}, Query: HomeSectionQuery{IsPlayed: &played, GenreIDs: []string{"7"}}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sections := viewer(s.Snapshot()).HomeSections
	if len(sections) != 2 || sections[0].ID != "resume" || sections[1].ID == "" {
		t.Fatalf("stored: %+v", sections)
	}
	refused := errors.New("refused")
	if err := s.ChangeHomeSections(token, func([]HomeSection) ([]HomeSection, error) { return nil, refused }); !errors.Is(err, refused) {
		t.Fatalf("refused change: %v", err)
	}
	if err := s.ChangeHomeSections(token, func([]HomeSection) ([]HomeSection, error) {
		return make([]HomeSection, maxHomeSections+1), nil
	}); !errors.Is(err, ErrHomeSectionLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	reopened := viewer(s.Snapshot()).HomeSections
	if len(reopened) != 2 || reopened[1].ID != sections[1].ID || *reopened[1].Query.IsPlayed || !slices.Equal(reopened[1].Query.GenreIDs, []string{"7"}) {
		t.Fatalf("reopened: %+v", reopened)
	}

	other, err := s.CreateUser("other", Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CopyUserData(viewer(s.Snapshot()).ID, []string{other.ID}, false, true); err != nil {
		t.Fatal(err)
	}
	if copied := s.Snapshot().Users[other.ID].HomeSections; len(copied) != 2 || copied[1].ID != sections[1].ID {
		t.Fatalf("copied: %+v", copied)
	}

	// Removing every section leaves an empty home screen, not the default.
	if err := s.ChangeHomeSections(token, func([]HomeSection) ([]HomeSection, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	if emptied := viewer(s.Snapshot()).HomeSections; emptied == nil || len(emptied) != 0 {
		t.Fatalf("emptied: %#v", emptied)
	}

	if _, err := s.db.ExecContext(t.Context(), "ALTER TABLE collections DROP COLUMN image_revision; ALTER TABLE collections DROP COLUMN image_mime; ALTER TABLE collections DROP COLUMN image_width; ALTER TABLE collections DROP COLUMN image_height; ALTER TABLE playlists DROP COLUMN image_revision; ALTER TABLE playlists DROP COLUMN image_mime; ALTER TABLE playlists DROP COLUMN image_width; ALTER TABLE playlists DROP COLUMN image_height; ALTER TABLE collections DROP COLUMN set_key; ALTER TABLE collections DROP COLUMN set_items; ALTER TABLE collections DROP COLUMN declined; DROP TABLE dismissed_sets; ALTER TABLE libraries DROP COLUMN realtime_monitor; DROP TABLE catalog; DROP TABLE catalog_folders; ALTER TABLE server DROP COLUMN vacuum_on_startup; ALTER TABLE server DROP COLUMN optimize_on_shutdown; ALTER TABLE users DROP COLUMN home_sections; PRAGMA user_version = 9"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if migrated := viewer(s.Snapshot()).HomeSections; migrated != nil {
		t.Fatalf("migrated: %+v", migrated)
	}
}
