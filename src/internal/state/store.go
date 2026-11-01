// Package state persists the local users, sessions and playback state in SQLite.
package state

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const passwordIterations = 600000

// maxSettingsBytes bounds a user's settings and configuration, which
// clients replace whole or merge key by key; items and sessions have counts.
const maxSettingsBytes = 1 << 20

// SessionLifetime is how long a sign-in stays valid.
const SessionLifetime = 30 * 24 * time.Hour

// maxUsers bounds the number of local accounts.
const maxUsers = 100

// Errors returned by Store.
var (
	ErrCredentials       = errors.New("invalid credentials")
	ErrSession           = errors.New("invalid or expired session")
	ErrSessionLimit      = errors.New("session limit reached")
	ErrStateLimit        = errors.New("state size limit reached")
	ErrPassword          = errors.New("password must contain 12–1024 bytes")
	ErrUserName          = errors.New("user name must contain 1–128 bytes")
	ErrUserNameTaken     = errors.New("user name is already taken")
	ErrUserNotFound      = errors.New("user not found")
	ErrLastAdministrator = errors.New("the last administrator cannot be removed, disabled or demoted")
)

// Policy is the part of a user's Emby policy that Couch stores and enforces.
type Policy struct {
	// IsAdministrator allows the server's administrator-only operations.
	IsAdministrator bool
	// IsDisabled refuses sign-in.
	IsDisabled bool
	// IsHidden leaves the user off the sign-in screen.
	IsHidden bool
	// EnableAllFolders shows every library; otherwise only the libraries
	// whose IDs EnabledFolders lists.
	EnableAllFolders bool
	EnabledFolders   []string
}

// User is a local account with its settings and playback state. A user
// created from the dashboard has no password until one is set, and cannot
// sign in until then.
type User struct {
	ID            string
	Name          string
	Salt          []byte
	PasswordHash  []byte
	Iterations    int
	Configuration map[string]json.RawMessage
	Settings      map[string]json.RawMessage
	// Items holds per-item playback state keyed by item ID. It is absent in
	// state written before playback existed and is created on first write.
	Items map[string]ItemState
	// Image is the uploaded avatar, absent until one is uploaded.
	Image  *UserImage `json:",omitempty"`
	Policy Policy
}

// HasPassword reports whether the user can sign in with a password.
func (u User) HasPassword() bool { return len(u.PasswordHash) > 0 }

// ItemState is the durable part of an item's UserData.
type ItemState struct {
	PositionTicks int64
	PlayCount     int
	Played        bool
	IsFavorite    bool
	LastPlayed    time.Time
	// LastSearched records when the user opened the item from search results.
	LastSearched time.Time
	// HiddenFromResume keeps a next-up episode out of Continue Watching after
	// "Remove from Continue Watching", until the item is played again.
	HiddenFromResume bool `json:",omitempty"`
}

// maxTrackedItems bounds each user's state independently of its byte limit.
const maxTrackedItems = 20000

// Session is an authenticated client. Stored keyed by token hash, never by token.
type Session struct {
	ID           string
	UserID       string
	Client       string
	DeviceID     string
	DeviceName   string
	Version      string
	ExpiresAt    time.Time
	Capabilities map[string]json.RawMessage
	RecentPlays  []PlaybackRecord `json:",omitempty"`
}

// PlaybackRecord retains a bounded retry window per authenticated client.
// Switched holds the play session IDs the play took on when the client
// switched quality or audio track; reports may carry any of them.
type PlaybackRecord struct {
	ID       string
	ItemID   string
	Stopped  bool
	Switched []string `json:",omitempty"`
}

// Is reports whether playID names this play.
func (p PlaybackRecord) Is(playID string) bool {
	return p.ID == playID || slices.Contains(p.Switched, playID)
}

// Data is the persisted state, loaded whole into memory.
type Data struct {
	SchemaVersion int
	ServerID      string
	// Users holds every local account keyed by user ID.
	Users    map[string]User
	Sessions map[string]Session
}

// UserByName finds a user by name, ignoring case.
func (d Data) UserByName(name string) (User, bool) {
	for _, u := range d.Users {
		if strings.EqualFold(u.Name, name) {
			return u, true
		}
	}
	return User{}, false
}

// SortedUsers returns the users ordered by name.
func (d Data) SortedUsers() []User {
	users := slices.Collect(maps.Values(d.Users))
	slices.SortFunc(users, func(a, b User) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return users
}

// Store holds the state in memory and commits every change to the database
// in one transaction before publishing it.
type Store struct {
	mu   sync.RWMutex
	dir  string
	lock *os.File
	db   *sql.DB
	data Data
	// imageMu serializes avatar changes, so a file is removed only once the
	// committed state no longer references it.
	imageMu sync.Mutex
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Open locks a data directory for this process. Missing state requires explicit
// initialization; a corrupt or newer schema is never silently replaced.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // dir is the operator-selected data directory|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("data directory is already in use: %w", err)
	}
	removeInterruptedWrites(dir)
	db, err := openDB(filepath.Join(dir, "couch.db"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	s := &Store{dir: dir, lock: lock, db: db}
	if err := s.load(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	d, err := loadDB(context.Background(), s.db)
	if err != nil || d.SchemaVersion == 0 {
		return err
	}
	if err := validate(d); err != nil {
		return err
	}
	s.data = d
	return nil
}

// removeInterruptedWrites deletes the temp files of avatar writes cut short
// by a crash, which skip their deferred removal. The caller holds the directory
// lock, so no write is in progress.
func removeInterruptedWrites(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasPrefix(name, ".user-image-") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// Close closes the database and releases the data directory lock.
func (s *Store) Close() error { return errors.Join(s.db.Close(), s.lock.Close()) }

// Initialized reports whether a user has been created.
func (s *Store) Initialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.SchemaVersion != 0
}

// Identity returns the server ID and each user's ID, name and policy, without
// the deep copy Snapshot makes; anonymous endpoints need nothing else. The
// password hash is replaced by a marker, so HasPassword still answers.
func (s *Store) Identity() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make(map[string]User, len(s.data.Users))
	for id, u := range s.data.Users {
		public := User{ID: u.ID, Name: u.Name, Policy: u.Policy}
		if u.HasPassword() {
			public.PasswordHash = []byte{0}
		}
		users[id] = public
	}
	return Data{ServerID: s.data.ServerID, Users: users}
}

// Snapshot returns the current state. It is shared with other readers and
// must not be modified: writers publish a new state instead of changing it.
func (s *Store) Snapshot() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// update commits a change to the database, then publishes it. fn works on a
// copy that shares the published state except the users and sessions maps
// and, for the user userID, the maps it may change: the settings,
// configuration and enabled folders, and the items map only when items names
// the IDs fn may change. Only that user's row and those item rows are
// written, besides users fn adds or removes and changed sessions.
func (s *Store) update(userID string, items []string, fn func(*Data) error) error {
	return s.updateUsers([]string{userID}, items, fn)
}

// updateUsers is update for changes to several users, whose rows are all
// written; items belong to the first.
func (s *Store) updateUsers(userIDs, items []string, fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	next.Users = maps.Clone(s.data.Users)
	next.Sessions = maps.Clone(s.data.Sessions)
	for i, id := range userIDs {
		u, ok := next.Users[id]
		if !ok {
			continue
		}
		u.Settings = maps.Clone(u.Settings)
		u.Configuration = maps.Clone(u.Configuration)
		u.Policy.EnabledFolders = slices.Clone(u.Policy.EnabledFolders)
		if i == 0 && len(items) > 0 {
			u.Items = maps.Clone(u.Items)
			if u.Items == nil {
				u.Items = map[string]ItemState{}
			}
		}
		next.Users[id] = u
	}
	if err := fn(&next); err != nil {
		return err
	}
	for _, id := range userIDs {
		if settingsBytes(next.Users[id]) > maxSettingsBytes {
			return ErrStateLimit
		}
	}
	if err := commit(context.Background(), s.db, s.data, next, userIDs, items); err != nil {
		return err
	}
	s.data = next
	return nil
}

func settingsBytes(u User) int {
	n := 0
	for k, v := range u.Settings {
		n += len(k) + len(v)
	}
	for k, v := range u.Configuration {
		n += len(k) + len(v)
	}
	return n
}

// cloneSession copies what callers of Change and SetItemSession may modify
// in place, so the published session stays unchanged until the commit.
func cloneSession(session Session) Session {
	session.Capabilities = maps.Clone(session.Capabilities)
	session.RecentPlays = slices.Clone(session.RecentPlays)
	for i := range session.RecentPlays {
		session.RecentPlays[i].Switched = slices.Clone(session.RecentPlays[i].Switched)
	}
	return session
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", ErrUserName
	}
	return name, nil
}

func hashPassword(password string) (salt, hash []byte, err error) {
	if len(password) < 12 || len(password) > 1024 {
		return nil, nil, ErrPassword
	}
	salt = make([]byte, 16)
	_, _ = rand.Read(salt)
	hash, err = pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return salt, hash, err
}

// Initialize creates the first local user as the administrator. It fails if
// a user already exists.
func (s *Store) Initialize(name, password string) error {
	name, err := validName(name)
	if err != nil || len(password) < 12 || len(password) > 1024 {
		return errors.New("username must contain 1–128 bytes; password 12–1024 bytes")
	}
	salt, hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return s.update("", nil, func(d *Data) error {
		if d.SchemaVersion != 0 {
			return errors.New("already initialized")
		}
		u := User{ID: randomID(), Name: name, Salt: salt, PasswordHash: hash, Iterations: passwordIterations,
			Configuration: map[string]json.RawMessage{}, Settings: map[string]json.RawMessage{},
			Policy: Policy{IsAdministrator: true, EnableAllFolders: true, EnabledFolders: []string{}}}
		*d = Data{SchemaVersion: 1, ServerID: randomID(), Users: map[string]User{u.ID: u}, Sessions: map[string]Session{}}
		return nil
	})
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// dummySalt makes a sign-in for an unknown user cost as much as a real one.
var dummySalt = make([]byte, 16)

// Login verifies the credentials and stores a new 30-day session. It returns the
// session token, which is not persisted.
func (s *Store) Login(name, password string, client Session) (string, Session, error) {
	s.mu.RLock()
	u, found := s.data.UserByName(name)
	s.mu.RUnlock()
	salt, iterations := u.Salt, u.Iterations
	if !found || !u.HasPassword() {
		salt, iterations = dummySalt, passwordIterations
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", Session{}, err
	}
	if !found || !u.HasPassword() || u.Policy.IsDisabled || subtle.ConstantTimeCompare(hash, u.PasswordHash) != 1 {
		return "", Session{}, ErrCredentials
	}
	token := randomID() + randomID()
	client.ID, client.UserID = randomID(), u.ID
	client.ExpiresAt = time.Now().UTC().Add(SessionLifetime)
	err = s.update("", nil, func(d *Data) error {
		// The password or the policy may have changed while it was verified
		// without the lock; a session must not outlive the revocation that
		// change performs.
		current, ok := d.Users[u.ID]
		if !ok || current.Policy.IsDisabled || subtle.ConstantTimeCompare(current.PasswordHash, u.PasswordHash) != 1 {
			return ErrCredentials
		}
		for key, session := range d.Sessions {
			if !session.ExpiresAt.After(time.Now()) {
				delete(d.Sessions, key)
			}
		}
		if len(d.Sessions) >= 64 {
			return ErrSessionLimit
		}
		d.Sessions[tokenHash(token)] = client
		return nil
	})
	return token, client, err
}

// revokeSessions deletes the sessions of userID except the one keyed keep,
// and returns their IDs so open connections can be closed.
func revokeSessions(d *Data, userID, keep string) []string {
	revoked := []string{}
	for k, session := range d.Sessions {
		if session.UserID == userID && k != keep {
			revoked = append(revoked, session.ID)
			delete(d.Sessions, k)
		}
	}
	return revoked
}

// ChangePassword replaces the caller's password after verifying the current
// one. The calling session stays signed in; the user's other sessions are
// revoked, and their IDs are returned so open connections can be closed.
func (s *Store) ChangePassword(token, current, next string) ([]string, error) {
	salt, nextHash, err := hashPassword(next)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	session, ok := s.data.Sessions[tokenHash(token)]
	u := s.data.Users[session.UserID]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrSession
	}
	if !u.HasPassword() {
		return nil, ErrCredentials
	}
	hash, err := pbkdf2.Key(sha256.New, current, u.Salt, u.Iterations, 32)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(hash, u.PasswordHash) != 1 {
		return nil, ErrCredentials
	}
	var revoked []string
	err = s.update(u.ID, nil, func(d *Data) error {
		key := tokenHash(token)
		if session, ok := d.Sessions[key]; !ok || !session.ExpiresAt.After(time.Now()) {
			return ErrSession
		}
		user, ok := d.Users[u.ID]
		// A concurrent change must not be overwritten with a stale verification.
		if !ok || subtle.ConstantTimeCompare(user.PasswordHash, u.PasswordHash) != 1 {
			return ErrCredentials
		}
		user.Salt, user.PasswordHash, user.Iterations = salt, nextHash, passwordIterations
		d.Users[u.ID] = user
		revoked = revokeSessions(d, u.ID, key)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}

// Authenticate returns the unexpired session for token.
func (s *Store) Authenticate(token string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if token == "" {
		return Session{}, ErrSession
	}
	v, ok := s.data.Sessions[tokenHash(token)]
	if !ok || !v.ExpiresAt.After(time.Now()) {
		return Session{}, ErrSession
	}
	return v, nil
}

// Logout revokes the session for token.
func (s *Store) Logout(token string) error {
	return s.update("", nil, func(d *Data) error { delete(d.Sessions, tokenHash(token)); return nil })
}

// sessionUser returns the user ID of token's session, empty without one.
// Writers check the session again under the lock.
func (s *Store) sessionUser(token string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Sessions[tokenHash(token)].UserID
}

// Change checks the session again under the write lock to prevent a concurrent
// logout from being followed by a successful settings mutation. fn changes the
// session's user and the session; it must not change items: SetItem and
// SetItems do, writing only those rows.
func (s *Store) Change(token string, fn func(*User, *Session)) error {
	userID := s.sessionUser(token)
	return s.update(userID, nil, func(d *Data) error {
		key := tokenHash(token)
		session, ok := d.Sessions[key]
		u, exists := d.Users[userID]
		if !ok || !exists || !session.ExpiresAt.After(time.Now()) || session.UserID != userID {
			return ErrSession
		}
		items := u.Items
		u.Items = nil
		session = cloneSession(session)
		fn(&u, &session)
		if u.Items != nil {
			return errors.New("state: Change must not modify items")
		}
		u.Items = items
		d.Users[userID] = u
		d.Sessions[key] = session
		return nil
	})
}

// SetItem updates one item's playback state under the write lock, re-checking
// the session so a concurrent logout cannot be followed by a successful write.
func (s *Store) SetItem(token, itemID string, fn func(*ItemState)) error {
	return s.SetItemSession(token, itemID, func(item *ItemState, _ *Session) { fn(item) })
}

// SetItemSession saves playback lifecycle and item state in one transaction.
func (s *Store) SetItemSession(token, itemID string, fn func(*ItemState, *Session)) error {
	if itemID == "" {
		return errors.New("item id is required")
	}
	userID := s.sessionUser(token)
	return s.update(userID, []string{itemID}, func(d *Data) error {
		key := tokenHash(token)
		session, ok := d.Sessions[key]
		u, exists := d.Users[userID]
		if !ok || !exists || !session.ExpiresAt.After(time.Now()) || session.UserID != userID {
			return ErrSession
		}
		session = cloneSession(session)
		current, tracked := u.Items[itemID]
		if !tracked && len(u.Items) >= maxTrackedItems {
			return ErrStateLimit
		}
		fn(&current, &session)
		d.Sessions[key] = session
		if current == (ItemState{}) {
			delete(u.Items, itemID)
			return nil
		}
		u.Items[itemID] = current
		return nil
	})
}

// SetItems applies fn to several items in one transaction: either every item
// is saved, or none is when the session is invalid or the tracked-item limit
// would be exceeded. Items fn leaves empty are dropped and never count toward
// the limit. It returns the resulting state of each item.
func (s *Store) SetItems(token string, itemIDs []string, fn func(id string, item *ItemState)) (map[string]ItemState, error) {
	saved := map[string]ItemState{}
	userID := s.sessionUser(token)
	err := s.update(userID, itemIDs, func(d *Data) error {
		session, ok := d.Sessions[tokenHash(token)]
		u, exists := d.Users[userID]
		if !ok || !exists || !session.ExpiresAt.After(time.Now()) || session.UserID != userID {
			return ErrSession
		}
		added := 0
		for _, id := range itemIDs {
			current, tracked := u.Items[id]
			fn(id, &current)
			saved[id] = current
			if !tracked && current != (ItemState{}) {
				added++
			}
		}
		if added > 0 && len(u.Items)+added > maxTrackedItems {
			return ErrStateLimit
		}
		for id, current := range saved {
			if current == (ItemState{}) {
				delete(u.Items, id)
			} else {
				u.Items[id] = current
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// administrators counts the enabled administrators.
func administrators(d *Data) int {
	n := 0
	for _, u := range d.Users {
		if u.Policy.IsAdministrator && !u.Policy.IsDisabled {
			n++
		}
	}
	return n
}

// CreateUser adds a user without a password, with the given policy and
// client configuration, which may be copied from another user.
func (s *Store) CreateUser(name string, policy Policy, configuration map[string]json.RawMessage) (User, error) {
	name, err := validName(name)
	if err != nil {
		return User{}, err
	}
	policy.EnabledFolders = slices.Clone(policy.EnabledFolders)
	if policy.EnabledFolders == nil {
		policy.EnabledFolders = []string{}
	}
	configuration = maps.Clone(configuration)
	if configuration == nil {
		configuration = map[string]json.RawMessage{}
	}
	u := User{ID: randomID(), Name: name, Configuration: configuration, Settings: map[string]json.RawMessage{}, Policy: policy}
	err = s.update(u.ID, nil, func(d *Data) error {
		if _, taken := d.UserByName(name); taken {
			return ErrUserNameTaken
		}
		if len(d.Users) >= maxUsers {
			return ErrStateLimit
		}
		d.Users[u.ID] = u
		return nil
	})
	return u, err
}

// RenameUser changes the user's name, which stays unique ignoring case.
func (s *Store) RenameUser(id, name string) error {
	name, err := validName(name)
	if err != nil {
		return err
	}
	return s.update(id, nil, func(d *Data) error {
		u, ok := d.Users[id]
		if !ok {
			return ErrUserNotFound
		}
		if other, taken := d.UserByName(name); taken && other.ID != id {
			return ErrUserNameTaken
		}
		u.Name = name
		d.Users[id] = u
		return nil
	})
}

// SetPolicy replaces the user's policy. Disabling a user revokes their
// sessions, whose IDs are returned. At least one enabled administrator
// always remains.
func (s *Store) SetPolicy(id string, policy Policy) ([]string, error) {
	policy.EnabledFolders = slices.Clone(policy.EnabledFolders)
	if policy.EnabledFolders == nil {
		policy.EnabledFolders = []string{}
	}
	var revoked []string
	err := s.update(id, nil, func(d *Data) error {
		u, ok := d.Users[id]
		if !ok {
			return ErrUserNotFound
		}
		u.Policy = policy
		d.Users[id] = u
		if administrators(d) == 0 {
			return ErrLastAdministrator
		}
		if policy.IsDisabled {
			revoked = revokeSessions(d, id, "")
		}
		return nil
	})
	return revoked, err
}

// SetPassword sets the user's password without the current one, as an
// administrator does; an empty password removes it, so the user cannot sign
// in until a new one is set. The user's sessions are revoked, except the one
// of token, and their IDs are returned.
func (s *Store) SetPassword(token, id, password string) ([]string, error) {
	var salt, hash []byte
	iterations := 0
	if password != "" {
		var err error
		if salt, hash, err = hashPassword(password); err != nil {
			return nil, err
		}
		iterations = passwordIterations
	}
	var revoked []string
	err := s.update(id, nil, func(d *Data) error {
		u, ok := d.Users[id]
		if !ok {
			return ErrUserNotFound
		}
		u.Salt, u.PasswordHash, u.Iterations = salt, hash, iterations
		d.Users[id] = u
		revoked = revokeSessions(d, id, tokenHash(token))
		return nil
	})
	return revoked, err
}

// CopyUserData copies the policy, the client configuration or both from the
// user from to the users to, in one transaction. Copying the policy revokes
// the sessions of a user it disables, whose IDs are returned.
func (s *Store) CopyUserData(from string, to []string, policy, configuration bool) ([]string, error) {
	var revoked []string
	err := s.updateUsers(to, nil, func(d *Data) error {
		source, ok := d.Users[from]
		if !ok {
			return ErrUserNotFound
		}
		for _, id := range to {
			u, ok := d.Users[id]
			if !ok {
				return ErrUserNotFound
			}
			if policy {
				u.Policy = source.Policy
				u.Policy.EnabledFolders = slices.Clone(source.Policy.EnabledFolders)
				if u.Policy.IsDisabled {
					revoked = append(revoked, revokeSessions(d, id, "")...)
				}
			}
			if configuration {
				u.Configuration = maps.Clone(source.Configuration)
			}
			d.Users[id] = u
		}
		if administrators(d) == 0 {
			return ErrLastAdministrator
		}
		return nil
	})
	return revoked, err
}

// DeleteUser removes the user with their playback state, avatar and
// sessions, whose IDs are returned. The last administrator cannot be deleted.
func (s *Store) DeleteUser(id string) ([]string, error) {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	var revoked []string
	var image *UserImage
	err := s.update("", nil, func(d *Data) error {
		u, ok := d.Users[id]
		if !ok {
			return ErrUserNotFound
		}
		image = u.Image
		delete(d.Users, id)
		if administrators(d) == 0 {
			return ErrLastAdministrator
		}
		revoked = revokeSessions(d, id, "")
		return nil
	})
	s.removeUnreferencedImages(image)
	return revoked, err
}

// ReadPassword reads a bounded password and strips one trailing newline.
// Initialize enforces the length limits.
func ReadPassword(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 1027))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
}
