package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// viewer returns the user testStore creates.
func viewer(d Data) User {
	u, _ := d.UserByName("viewer")
	return u
}

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Initialize("viewer", "test-only-password"); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestStateProtectionAndExpiry(t *testing.T) {
	s, dir := testStore(t)
	if second, err := Open(dir); err == nil {
		_ = second.Close()
		t.Fatal("second process could open state")
	}
	if s.Initialize("replacement", "another-test-password") == nil {
		t.Fatal("initialized twice")
	}
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "couch.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(token)) || bytes.Contains(b, []byte("test-only-password")) {
		t.Fatal("plaintext secret persisted")
	}
	info, err := os.Stat(filepath.Join(dir, "couch.db"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe state permissions")
	}
	if err := s.Change(token, func(u *User, session *Session) { session.ExpiresAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); !errors.Is(err, ErrSession) {
		t.Fatal("expired session accepted")
	}
	if err := s.Change(token, func(u *User, session *Session) { t.Fatal("expired session mutated state") }); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
}

func TestFailedWriteDoesNotPublishState(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	// A directory in place of the rollback journal fails the transaction.
	journal := filepath.Join(dir, "couch.db-journal")
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	err = s.Change(token, func(u *User, session *Session) { u.Settings["theme"] = json.RawMessage(`"changed"`) })
	if err == nil {
		t.Fatal("write should fail")
	}
	if len(viewer(s.Snapshot()).Settings) != 0 {
		t.Fatal("failed write changed memory")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(token, func(u *User, session *Session) { u.Settings["theme"] = json.RawMessage(`"changed"`) }); err != nil {
		t.Fatal("write after recovery:", err)
	}
}

func TestCorruptAndFutureStateAreNotReset(t *testing.T) {
	cases := []string{
		"not a database, but long enough to be read as one",
		"", // replaced below by a database from a newer release
	}
	for _, content := range cases {
		dir := t.TempDir()
		file := filepath.Join(dir, "couch.db")
		if content == "" {
			db, err := openDB(file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 99"); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
		} else if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if s, err := Open(dir); err == nil {
			_ = s.Close()
			t.Fatal("invalid database accepted")
		}
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("invalid database overwritten")
		}
	}
}

func TestConcurrentSettingsAndLogout(t *testing.T) {
	s, _ := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Change(token, func(u *User, session *Session) { u.Settings["theme"] = json.RawMessage(`"dark"`) })
			_, _ = s.Authenticate(token)
			_ = s.Snapshot()
		}()
	}
	if err := s.Logout(token); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := s.Authenticate(token); err == nil {
		t.Fatal("session resurrected")
	}
}

func TestStateSizeLimitDoesNotChangeCommittedData(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "couch.db"))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Change(token, func(u *User, session *Session) {
		u.Settings["oversized"] = json.RawMessage(`"` + strings.Repeat("a", maxSettingsBytes) + `"`)
	})
	if !errors.Is(err, ErrStateLimit) {
		t.Fatal("expected state size limit")
	}
	after, err := os.ReadFile(filepath.Join(dir, "couch.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(viewer(s.Snapshot()).Settings) != 0 {
		t.Fatal("rejected update changed state")
	}
}

func TestOpenRemovesInterruptedWrites(t *testing.T) {
	s, dir := testStore(t)
	_ = s.Close()
	for _, name := range []string{".user-image-456", "user-image-kept", ".other"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if !reopened.Initialized() {
		t.Fatal("committed state was not loaded")
	}
	for name, kept := range map[string]bool{".user-image-456": false, "user-image-kept": true, ".other": true, "couch.db": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != kept {
			t.Fatalf("%s: kept=%v, want %v", name, err == nil, kept)
		}
	}
}

func TestSetItemsIsAllOrNothing(t *testing.T) {
	s, _ := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	old := make([]string, maxTrackedItems-2)
	for i := range old {
		old[i] = fmt.Sprintf("old-%d", i)
	}
	if _, err := s.SetItems(token, old, func(_ string, st *ItemState) { st.IsFavorite = true }); err != nil {
		t.Fatal(err)
	}
	played := func(_ string, st *ItemState) { st.Played = true }
	// Three new items do not fit into the two free places: none is saved.
	if _, err := s.SetItems(token, []string{"a", "b", "c"}, played); !errors.Is(err, ErrStateLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	if items := viewer(s.Snapshot()).Items; items["a"].Played || items["b"].Played {
		t.Fatal("a failed batch saved some items")
	}
	if _, err := s.SetItems(token, []string{"a", "b", "old-1"}, played); err != nil {
		t.Fatal(err)
	}
	// At the limit, unmarking items that have no state adds nothing.
	saved, err := s.SetItems(token, []string{"a", "never-seen"}, func(_ string, st *ItemState) { st.Played = false })
	if err != nil || saved["a"].Played || len(viewer(s.Snapshot()).Items) != maxTrackedItems-1 {
		t.Fatalf("unmark at the limit: %v, %d items", err, len(viewer(s.Snapshot()).Items))
	}
}

func TestIdentityCarriesOnlyPublicFields(t *testing.T) {
	s, _ := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem(token, "a", func(st *ItemState) { st.Played = true }); err != nil {
		t.Fatal(err)
	}
	full, id := s.Snapshot(), s.Identity()
	public := viewer(id)
	if id.ServerID != full.ServerID || public.ID != viewer(full).ID || !public.HasPassword() || !public.Policy.IsAdministrator {
		t.Fatalf("identity %+v", id)
	}
	if public.Items != nil || id.Sessions != nil || public.Salt != nil || len(public.PasswordHash) > 1 || public.Settings != nil {
		t.Fatal("identity carries private state")
	}
}

// TestReopenRestoresAllState writes every kind of state, reopens the store
// and expects an identical snapshot.
func TestReopenRestoresAllState(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{Client: "test", DeviceID: "d", Capabilities: map[string]json.RawMessage{"PlayableMediaTypes": json.RawMessage(`["Video"]`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login("viewer", "test-only-password", Session{Client: "second"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.Change(token, func(u *User, session *Session) {
		u.Settings["theme"] = json.RawMessage(`"dark"`)
		u.Configuration["SubtitleMode"] = json.RawMessage(`"Smart"`)
		session.RecentPlays = []PlaybackRecord{{ID: "p", ItemID: "a", Switched: []string{"q"}}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserImage(token, []byte("png"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItems(token, []string{"a", "b", "c"}, func(_ string, st *ItemState) { st.Played, st.PlayCount, st.LastPlayed = true, 1, now }); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem(token, "b", func(st *ItemState) {
		st.PositionTicks, st.IsFavorite, st.HiddenFromResume, st.LastSearched = 42, true, true, now
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem(token, "c", func(st *ItemState) { *st = ItemState{} }); err != nil {
		t.Fatal(err)
	}
	want := s.Snapshot()
	_ = s.Close()
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got := reopened.Snapshot()
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("reopened state differs:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if _, ok := viewer(got).Items["c"]; ok || len(got.Sessions) != 2 || viewer(got).Image == nil {
		t.Fatalf("items %v, sessions %d, image %v", viewer(got).Items, len(got.Sessions), viewer(got).Image)
	}
}

// TestPublishedStateIsNotModified checks that later writes, including one
// whose commit fails, leave a state already handed to readers unchanged.
func TestPublishedStateIsNotModified(t *testing.T) {
	s, dir := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{Capabilities: map[string]json.RawMessage{"a": json.RawMessage(`1`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemSession(token, "a", func(st *ItemState, session *Session) {
		st.PositionTicks = 1
		session.RecentPlays = append(session.RecentPlays, PlaybackRecord{ID: "p", ItemID: "a", Switched: []string{"q"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(token, func(u *User, _ *Session) { u.Settings["theme"] = json.RawMessage(`"dark"`) }); err != nil {
		t.Fatal(err)
	}
	published := s.Snapshot()
	session, err := s.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(published)
	sessionBefore, _ := json.Marshal(session)

	edit := func(st *ItemState, session *Session) {
		st.PositionTicks = 2
		session.RecentPlays[0].Stopped = true
		session.RecentPlays[0].Switched[0] = "changed"
		session.Capabilities["a"] = json.RawMessage(`2`)
	}
	journal := filepath.Join(dir, "couch.db-journal")
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemSession(token, "a", edit); err == nil {
		t.Fatal("commit without its journal succeeded")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemSession(token, "a", edit); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(token, func(u *User, _ *Session) { u.Settings["theme"] = json.RawMessage(`"light"`) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItems(token, []string{"a", "b"}, func(_ string, st *ItemState) { st.Played = true }); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(published)
	sessionAfter, _ := json.Marshal(session)
	if !bytes.Equal(before, after) || !bytes.Equal(sessionBefore, sessionAfter) {
		t.Fatalf("published state changed:\n%s\n%s", before, after)
	}
	if got := viewer(s.Snapshot()).Items["a"]; got.PositionTicks != 2 || !got.Played {
		t.Fatalf("current state %+v", got)
	}
}

func TestChangeMustNotModifyItems(t *testing.T) {
	s, _ := testStore(t)
	token, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItem(token, "a", func(st *ItemState) { st.Played = true }); err != nil {
		t.Fatal(err)
	}
	err = s.Change(token, func(u *User, _ *Session) {
		u.Items = map[string]ItemState{"b": {Played: true}}
		u.Settings["theme"] = json.RawMessage(`"dark"`)
	})
	if err == nil {
		t.Fatal("Change replaced the items")
	}
	if u := viewer(s.Snapshot()); len(u.Items) != 1 || !u.Items["a"].Played || len(u.Settings) != 0 {
		t.Fatalf("rejected change was applied: %+v", u)
	}
}

// TestOwnerBecomesAdministrator opens a database written before the
// administrator role existed: its only user, the owner, gets the role.
func TestOwnerBecomesAdministrator(t *testing.T) {
	s, dir := testStore(t)
	if !viewer(s.Snapshot()).Policy.IsAdministrator {
		t.Fatal("-init did not create an administrator")
	}
	if _, err := s.db.ExecContext(t.Context(), dropPolicyColumns+"PRAGMA user_version = 2"); err != nil {
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
	var version int
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != dbSchemaVersion {
		t.Fatalf("version %d, %v", version, err)
	}
	if p := viewer(s.Snapshot()).Policy; !p.IsAdministrator || p.IsDisabled || p.IsHidden || !p.EnableAllFolders || p.EnabledFolders == nil {
		t.Fatalf("migrated owner policy %+v", p)
	}
}

// dropPolicyColumns turns the users table back into its version 2 shape.
const dropPolicyColumns = `ALTER TABLE users DROP COLUMN is_administrator; ALTER TABLE users DROP COLUMN is_disabled;
	ALTER TABLE users DROP COLUMN is_hidden; ALTER TABLE users DROP COLUMN enable_all_folders;
	ALTER TABLE users DROP COLUMN enabled_folders;`
