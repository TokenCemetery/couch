package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// dbSchemaVersion is stored in PRAGMA user_version; 0 means no schema yet.
const dbSchemaVersion = 4

const dbSchema = `
CREATE TABLE server (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	server_id TEXT NOT NULL
) STRICT;
CREATE TABLE users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	salt BLOB NOT NULL,
	password_hash BLOB NOT NULL,
	iterations INTEGER NOT NULL,
	configuration TEXT NOT NULL,
	settings TEXT NOT NULL,
	image_revision TEXT,
	image_mime TEXT,
	is_administrator INTEGER NOT NULL,
	is_disabled INTEGER NOT NULL,
	is_hidden INTEGER NOT NULL,
	enable_all_folders INTEGER NOT NULL,
	enabled_folders TEXT NOT NULL
) STRICT;
CREATE TABLE item_state (
	user_id TEXT NOT NULL REFERENCES users (id),
	item_id TEXT NOT NULL,
	position_ticks INTEGER NOT NULL,
	play_count INTEGER NOT NULL,
	played INTEGER NOT NULL,
	is_favorite INTEGER NOT NULL,
	last_played TEXT NOT NULL,
	last_searched TEXT NOT NULL,
	hidden_from_resume INTEGER NOT NULL,
	PRIMARY KEY (user_id, item_id)
) STRICT, WITHOUT ROWID;
CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	body TEXT NOT NULL
) STRICT, WITHOUT ROWID;
` + metadataSchema + `
PRAGMA user_version = 4;
`

// metadataSchema was added in version 2; migrate adds it to a version 1 file.
const metadataSchema = `
CREATE TABLE metadata (
	item_id TEXT PRIMARY KEY,
	nfo_revision TEXT NOT NULL,
	nfo TEXT NOT NULL,
	online_key TEXT NOT NULL,
	online TEXT NOT NULL,
	fetched_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;
`

// migrations upgrade a database written by an older release from the version
// in the key to the next one.
var migrations = map[int]string{
	1: metadataSchema + "PRAGMA user_version = 2;",
	// Version 3 adds the administrator role. Earlier releases had only the
	// owner, created by -init, who becomes the administrator.
	2: "ALTER TABLE users ADD COLUMN is_administrator INTEGER NOT NULL DEFAULT 0; UPDATE users SET is_administrator = 1; PRAGMA user_version = 3;",
	// Version 4 stores the rest of the policy Couch enforces; existing users
	// stay enabled, listed and see every library.
	3: `ALTER TABLE users ADD COLUMN is_disabled INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE users ADD COLUMN is_hidden INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE users ADD COLUMN enable_all_folders INTEGER NOT NULL DEFAULT 1;
		ALTER TABLE users ADD COLUMN enabled_folders TEXT NOT NULL DEFAULT '[]';
		PRAGMA user_version = 4;`,
}

// openDB opens the database file, creating it private to the owner. Reads are
// served from memory, so the rollback journal is used instead of WAL: every
// commit is synced, and a data directory that turns read-only fails writes
// instead of accepting them into an existing WAL file.
func openDB(path string) (*sql.DB, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // path is in the operator-selected data directory
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// loadDB reads the committed state. An empty database returns zero Data.
func loadDB(ctx context.Context, db *sql.DB) (Data, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return Data{}, err
	}
	if version == 0 {
		return Data{}, nil
	}
	for migrations[version] != "" {
		if _, err := db.ExecContext(ctx, "BEGIN;"+migrations[version]+"COMMIT;"); err != nil {
			_, _ = db.ExecContext(ctx, "ROLLBACK")
			return Data{}, err
		}
		version++
	}
	if version != dbSchemaVersion {
		return Data{}, fmt.Errorf("unsupported database schema version %d", version)
	}
	d := Data{SchemaVersion: 1, Sessions: map[string]Session{}}
	if err := db.QueryRowContext(ctx, "SELECT server_id FROM server").Scan(&d.ServerID); err != nil {
		return Data{}, err
	}
	users, err := loadUsers(ctx, db)
	if err != nil {
		return Data{}, err
	}
	d.Users = users
	rows, err := db.QueryContext(ctx, "SELECT token_hash, body FROM sessions")
	if err != nil {
		return Data{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, body string
		var session Session
		if err := rows.Scan(&key, &body); err != nil {
			return Data{}, err
		}
		if err := json.Unmarshal([]byte(body), &session); err != nil {
			return Data{}, err
		}
		d.Sessions[key] = session
	}
	return d, rows.Err()
}

func loadUsers(ctx context.Context, db *sql.DB) (map[string]User, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, salt, password_hash, iterations, configuration, settings, image_revision, image_mime,
		is_administrator, is_disabled, is_hidden, enable_all_folders, enabled_folders FROM users`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	users := map[string]User{}
	for rows.Next() {
		var u User
		var configuration, settings, folders string
		var revision, mime sql.NullString
		p := &u.Policy
		if err := rows.Scan(&u.ID, &u.Name, &u.Salt, &u.PasswordHash, &u.Iterations, &configuration, &settings, &revision, &mime,
			&p.IsAdministrator, &p.IsDisabled, &p.IsHidden, &p.EnableAllFolders, &folders); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(configuration), &u.Configuration); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(settings), &u.Settings); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(folders), &p.EnabledFolders); err != nil {
			return nil, err
		}
		if revision.Valid {
			u.Image = &UserImage{Revision: revision.String, MIME: mime.String}
		}
		users[u.ID] = u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id, u := range users {
		if err := loadItems(ctx, db, &u); err != nil {
			return nil, err
		}
		users[id] = u
	}
	return users, nil
}

func loadItems(ctx context.Context, db *sql.DB, u *User) error {
	rows, err := db.QueryContext(ctx, "SELECT item_id, position_ticks, play_count, played, is_favorite, last_played, last_searched, hidden_from_resume FROM item_state WHERE user_id = ?", u.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, lastPlayed, lastSearched string
		var item ItemState
		if err := rows.Scan(&id, &item.PositionTicks, &item.PlayCount, &item.Played, &item.IsFavorite, &lastPlayed, &lastSearched, &item.HiddenFromResume); err != nil {
			return err
		}
		if err := item.LastPlayed.UnmarshalText([]byte(lastPlayed)); err != nil {
			return err
		}
		if err := item.LastSearched.UnmarshalText([]byte(lastSearched)); err != nil {
			return err
		}
		if u.Items == nil {
			u.Items = map[string]ItemState{}
		}
		u.Items[id] = item
	}
	return rows.Err()
}

// commit writes the rows that differ between prev and next in one
// transaction: the rows of the users userIDs with, of the first one's items,
// only those named in items; the users next adds or drops; and the changed
// sessions. Committing from empty Data creates the schema and writes
// everything.
func commit(ctx context.Context, db *sql.DB, prev, next Data, userIDs, items []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, prev, next, userIDs, items); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func write(ctx context.Context, tx *sql.Tx, prev, next Data, userIDs, items []string) error {
	if prev.SchemaVersion == 0 {
		if _, err := tx.ExecContext(ctx, dbSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO server (id, server_id) VALUES (1, ?)", next.ServerID); err != nil {
			return err
		}
		for _, u := range next.Users {
			if err := writeUser(ctx, tx, User{}, u, slices.Collect(maps.Keys(u.Items))); err != nil {
				return err
			}
		}
		return writeSessions(ctx, tx, prev.Sessions, next.Sessions)
	}
	for id := range prev.Users {
		if _, ok := next.Users[id]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM item_state WHERE user_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id); err != nil {
			return err
		}
	}
	for i, id := range userIDs {
		u, ok := next.Users[id]
		if !ok {
			continue
		}
		var changed []string
		if i == 0 {
			changed = items
		}
		if err := writeUser(ctx, tx, prev.Users[id], u, changed); err != nil {
			return err
		}
	}
	return writeSessions(ctx, tx, prev.Sessions, next.Sessions)
}

// writeUser upserts the user's row when it differs from prev, then the named
// items.
func writeUser(ctx context.Context, tx *sql.Tx, prev, next User, items []string) error {
	prevRow, err := userRow(prev)
	if err != nil {
		return err
	}
	nextRow, err := userRow(next)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(prevRow, nextRow) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, name, salt, password_hash, iterations, configuration, settings, image_revision, image_mime,
				is_administrator, is_disabled, is_hidden, enable_all_folders, enabled_folders)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, salt = excluded.salt, password_hash = excluded.password_hash,
				iterations = excluded.iterations, configuration = excluded.configuration, settings = excluded.settings,
				image_revision = excluded.image_revision, image_mime = excluded.image_mime, is_administrator = excluded.is_administrator,
				is_disabled = excluded.is_disabled, is_hidden = excluded.is_hidden, enable_all_folders = excluded.enable_all_folders,
				enabled_folders = excluded.enabled_folders`, nextRow...); err != nil {
			return err
		}
	}
	return writeItems(ctx, tx, next.ID, prev.Items, next.Items, items)
}

func userRow(u User) ([]any, error) {
	if u.ID == "" {
		return nil, nil
	}
	configuration, err := json.Marshal(u.Configuration)
	if err != nil {
		return nil, err
	}
	settings, err := json.Marshal(u.Settings)
	if err != nil {
		return nil, err
	}
	folders, err := json.Marshal(u.Policy.EnabledFolders)
	if err != nil {
		return nil, err
	}
	var revision, mime any
	if u.Image != nil {
		revision, mime = u.Image.Revision, u.Image.MIME
	}
	// A user without a password stores empty blobs, which the columns require.
	salt, hash := u.Salt, u.PasswordHash
	if salt == nil {
		salt = []byte{}
	}
	if hash == nil {
		hash = []byte{}
	}
	p := u.Policy
	return []any{u.ID, u.Name, salt, hash, u.Iterations, string(configuration), string(settings), revision, mime,
		p.IsAdministrator, p.IsDisabled, p.IsHidden, p.EnableAllFolders, string(folders)}, nil
}

// writeItems upserts or deletes the rows of ids whose state differs.
func writeItems(ctx context.Context, tx *sql.Tx, userID string, prev, next map[string]ItemState, ids []string) error {
	var upsert *sql.Stmt
	for _, id := range ids {
		item, ok := next[id]
		old, existed := prev[id]
		switch {
		case !ok && existed:
			if _, err := tx.ExecContext(ctx, "DELETE FROM item_state WHERE user_id = ? AND item_id = ?", userID, id); err != nil {
				return err
			}
		case ok && (!existed || old != item):
			if upsert == nil {
				var err error
				if upsert, err = prepareUpsertItem(ctx, tx); err != nil {
					return err
				}
				defer func() { _ = upsert.Close() }()
			}
			if err := upsertItem(ctx, upsert, userID, id, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareUpsertItem(ctx context.Context, tx *sql.Tx) (*sql.Stmt, error) {
	return tx.PrepareContext(ctx, `INSERT INTO item_state (user_id, item_id, position_ticks, play_count, played, is_favorite, last_played, last_searched, hidden_from_resume)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, item_id) DO UPDATE SET position_ticks = excluded.position_ticks, play_count = excluded.play_count,
			played = excluded.played, is_favorite = excluded.is_favorite, last_played = excluded.last_played,
			last_searched = excluded.last_searched, hidden_from_resume = excluded.hidden_from_resume`)
}

func upsertItem(ctx context.Context, upsert *sql.Stmt, userID, id string, item ItemState) error {
	lastPlayed, err := timeText(item.LastPlayed)
	if err != nil {
		return err
	}
	lastSearched, err := timeText(item.LastSearched)
	if err != nil {
		return err
	}
	_, err = upsert.ExecContext(ctx, userID, id, item.PositionTicks, item.PlayCount, item.Played, item.IsFavorite, lastPlayed, lastSearched, item.HiddenFromResume)
	return err
}

func timeText(t time.Time) (string, error) {
	b, err := t.MarshalText()
	return string(b), err
}

func writeSessions(ctx context.Context, tx *sql.Tx, prev, next map[string]Session) error {
	for key, session := range next {
		body, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if old, ok := prev[key]; ok {
			oldBody, err := json.Marshal(old)
			if err != nil {
				return err
			}
			if string(oldBody) == string(body) {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (token_hash, body) VALUES (?, ?) ON CONFLICT (token_hash) DO UPDATE SET body = excluded.body", key, string(body)); err != nil {
			return err
		}
	}
	for key := range prev {
		if _, ok := next[key]; !ok {
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", key); err != nil {
				return err
			}
		}
	}
	return nil
}

var errInvalidSchema = errors.New("invalid or unsupported state schema")

// validate rejects state that does not satisfy the schema's invariants: at
// least one user, and each user either with a password or without one.
func validate(d Data) error {
	if d.SchemaVersion != 1 || d.ServerID == "" || len(d.Users) == 0 || d.Sessions == nil {
		return errInvalidSchema
	}
	for id, u := range d.Users {
		password := len(u.Salt) == 16 && len(u.PasswordHash) == 32 && u.Iterations == passwordIterations
		none := len(u.Salt) == 0 && len(u.PasswordHash) == 0 && u.Iterations == 0
		if u.ID != id || u.Name == "" || (!password && !none) || u.Settings == nil || u.Configuration == nil || u.Policy.EnabledFolders == nil {
			return errInvalidSchema
		}
	}
	return nil
}
