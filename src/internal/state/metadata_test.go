package state

import (
	"testing"
	"time"
)

func TestMetadataRecords(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Initialize("viewer", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	// A database written before metadata existed is migrated on open.
	if _, err := s.db.ExecContext(t.Context(), "DROP TABLE catalog; DROP TABLE catalog_folders; DROP TABLE metadata;"+dropPolicyColumns+"PRAGMA user_version = 1"); err != nil {
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
	var version int
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != dbSchemaVersion {
		t.Fatalf("version %d, %v", version, err)
	}
	fetched := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	a := MetadataRecord{ItemID: "a", NFORevision: "r1", NFO: []byte(`{"Name":"A"}`), OnlineKey: "k", Online: []byte(`{}`), FetchedAt: fetched}
	b := MetadataRecord{ItemID: "b", NFO: []byte{}, Online: []byte{}}
	if err := s.SaveMetadata(t.Context(), []MetadataRecord{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	a.NFO, a.Identified = []byte(`{"Name":"A2"}`), []byte(`{"Tmdb":"1"}`)
	if err := s.SaveMetadata(t.Context(), []MetadataRecord{a}, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	records, err := s.Metadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := records["a"]
	if len(records) != 1 || string(got.NFO) != `{"Name":"A2"}` || got.OnlineKey != "k" || !got.FetchedAt.Equal(fetched) || got.NFORevision != "r1" || string(got.Identified) != `{"Tmdb":"1"}` {
		t.Fatalf("%+v", records)
	}
	// The backup copies metadata with the rest of the database.
	backup := t.TempDir()
	if err := Backup(dir, backup); err != nil {
		t.Fatal(err)
	}
	copied, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copied.Close() }()
	if records, err := copied.Metadata(t.Context()); err != nil || string(records["a"].NFO) != `{"Name":"A2"}` {
		t.Fatalf("backup: %+v, %v", records, err)
	}
}
