package state

import (
	"errors"
	"os"
	"testing"
)

func TestUserManagement(t *testing.T) {
	s, dir := testStore(t)
	owner := viewer(s.Snapshot())
	ownerToken, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	guest, err := s.CreateUser("  Guest ", Policy{EnableAllFolders: true}, nil)
	if err != nil || guest.Name != "Guest" || guest.HasPassword() || guest.Policy.IsAdministrator || !guest.Policy.EnableAllFolders {
		t.Fatalf("create: %+v, %v", guest, err)
	}
	if _, err := s.CreateUser("GUEST", Policy{}, nil); !errors.Is(err, ErrUserNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := s.RenameUser(guest.ID, "viewer"); !errors.Is(err, ErrUserNameTaken) {
		t.Fatalf("rename onto a taken name: %v", err)
	}
	// Without a password the user cannot sign in, not even with an empty one.
	if _, _, err := s.Login("guest", "", Session{}); !errors.Is(err, ErrCredentials) {
		t.Fatalf("passwordless sign-in: %v", err)
	}
	if _, err := s.SetPassword(ownerToken, guest.ID, "short"); !errors.Is(err, ErrPassword) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := s.SetPassword(ownerToken, guest.ID, "guest-password-1"); err != nil {
		t.Fatal(err)
	}
	guestToken, session, err := s.Login("Guest", "guest-password-1", Session{})
	if err != nil || session.UserID != guest.ID {
		t.Fatalf("guest sign-in: %+v, %v", session, err)
	}
	// Item state belongs to the user of the session.
	if err := s.SetItem(guestToken, "a", func(st *ItemState) { st.Played = true }); err != nil {
		t.Fatal(err)
	}
	if d := s.Snapshot(); !d.Users[guest.ID].Items["a"].Played || d.Users[owner.ID].Items["a"].Played {
		t.Fatal("item state was not kept per user")
	}
	if err := s.SetUserImage(guestToken, []byte("guest avatar"), "image/png"); err != nil {
		t.Fatal(err)
	}
	// Disabling revokes the user's sessions and refuses sign-in.
	revoked, err := s.SetPolicy(guest.ID, Policy{IsDisabled: true, IsHidden: true, EnabledFolders: []string{"movies"}})
	if err != nil || len(revoked) != 1 || revoked[0] != session.ID {
		t.Fatalf("disable: %v, %v", revoked, err)
	}
	if _, err := s.Authenticate(guestToken); !errors.Is(err, ErrSession) {
		t.Fatal("a disabled user's session still works")
	}
	if _, _, err := s.Login("guest", "guest-password-1", Session{}); !errors.Is(err, ErrCredentials) {
		t.Fatalf("disabled sign-in: %v", err)
	}
	// One enabled administrator always remains.
	if _, err := s.SetPolicy(owner.ID, Policy{EnableAllFolders: true}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("demote the last administrator: %v", err)
	}
	if _, err := s.SetPolicy(owner.ID, Policy{IsAdministrator: true, IsDisabled: true}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("disable the last administrator: %v", err)
	}
	if _, err := s.DeleteUser(owner.ID); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("delete the last administrator: %v", err)
	}
	if _, err := s.Authenticate(ownerToken); err != nil {
		t.Fatal("a refused change revoked the administrator's session")
	}
	// Users and policies survive a reopen and a backup.
	backup := t.TempDir()
	if err := Backup(dir, backup); err != nil {
		t.Fatal(err)
	}
	copied, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	got := copied.Snapshot().Users[guest.ID]
	file, _, err := copied.OpenUserImage(guest.ID)
	_ = copied.Close()
	if err != nil {
		t.Fatalf("backup avatar: %v", err)
	}
	_ = file.Close()
	if p := got.Policy; got.Name != "Guest" || !got.HasPassword() || !p.IsDisabled || !p.IsHidden || p.EnableAllFolders || len(p.EnabledFolders) != 1 || !got.Items["a"].Played {
		t.Fatalf("backup user %+v", got)
	}
	image := s.Snapshot().Users[guest.ID].Image
	if _, err := s.DeleteUser(guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Users[guest.ID]; ok {
		t.Fatal("the user was not deleted")
	}
	if _, err := os.Stat(s.userImagePath(image.Revision)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the deleted user's avatar file remains")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var rows int
	if err := reopened.db.QueryRowContext(t.Context(), "SELECT count(*) FROM item_state WHERE user_id = ?", guest.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("deleted user's item rows: %d, %v", rows, err)
	}
	if d := reopened.Snapshot(); len(d.Users) != 1 || d.Users[owner.ID].Name != "viewer" {
		t.Fatalf("reopened users %+v", d.Users)
	}
}

// TestSetPasswordKeepsCallerSession checks that an administrator setting
// their own password through the reset path stays signed in, while a
// removed password ends the user's other sessions.
func TestSetPasswordKeepsCallerSession(t *testing.T) {
	s, _ := testStore(t)
	owner := viewer(s.Snapshot())
	first, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Login("viewer", "test-only-password", Session{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPassword(first, owner.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(first); err != nil {
		t.Fatal("the caller was signed out")
	}
	if _, err := s.Authenticate(second); !errors.Is(err, ErrSession) {
		t.Fatal("another session survived the password removal")
	}
	if viewer(s.Snapshot()).HasPassword() {
		t.Fatal("the password was not removed")
	}
}
