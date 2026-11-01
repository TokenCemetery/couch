package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// dbSchemaVersion is stored in PRAGMA user_version; 0 means no schema yet.
const dbSchemaVersion = 1

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
	image_mime TEXT
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
PRAGMA user_version = 1;
`

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
	if version != dbSchemaVersion {
		return Data{}, fmt.Errorf("unsupported database schema version %d", version)
	}
	d := Data{SchemaVersion: 1, Sessions: map[string]Session{}}
	if err := db.QueryRowContext(ctx, "SELECT server_id FROM server").Scan(&d.ServerID); err != nil {
		return Data{}, err
	}
	var configuration, settings string
	var revision, mime sql.NullString
	u := &d.User
	err := db.QueryRowContext(ctx, "SELECT id, name, salt, password_hash, iterations, configuration, settings, image_revision, image_mime FROM users").
		Scan(&u.ID, &u.Name, &u.Salt, &u.PasswordHash, &u.Iterations, &configuration, &settings, &revision, &mime)
	if err != nil {
		return Data{}, err
	}
	if err := json.Unmarshal([]byte(configuration), &u.Configuration); err != nil {
		return Data{}, err
	}
	if err := json.Unmarshal([]byte(settings), &u.Settings); err != nil {
		return Data{}, err
	}
	if revision.Valid {
		u.Image = &UserImage{Revision: revision.String, MIME: mime.String}
	}
	if err := loadItems(ctx, db, u); err != nil {
		return Data{}, err
	}
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
// transaction. Committing from empty Data creates the schema.
func commit(ctx context.Context, db *sql.DB, prev, next Data) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := write(ctx, tx, prev, next); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func write(ctx context.Context, tx *sql.Tx, prev, next Data) error {
	if prev.SchemaVersion == 0 {
		if _, err := tx.ExecContext(ctx, dbSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO server (id, server_id) VALUES (1, ?)", next.ServerID); err != nil {
			return err
		}
	}
	prevUser, err := userRow(prev.User)
	if err != nil {
		return err
	}
	nextUser, err := userRow(next.User)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(prevUser, nextUser) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, name, salt, password_hash, iterations, configuration, settings, image_revision, image_mime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, salt = excluded.salt, password_hash = excluded.password_hash,
				iterations = excluded.iterations, configuration = excluded.configuration, settings = excluded.settings,
				image_revision = excluded.image_revision, image_mime = excluded.image_mime`, nextUser...); err != nil {
			return err
		}
	}
	if err := writeItems(ctx, tx, next.User.ID, prev.User.Items, next.User.Items); err != nil {
		return err
	}
	return writeSessions(ctx, tx, prev.Sessions, next.Sessions)
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
	var revision, mime any
	if u.Image != nil {
		revision, mime = u.Image.Revision, u.Image.MIME
	}
	return []any{u.ID, u.Name, u.Salt, u.PasswordHash, u.Iterations, string(configuration), string(settings), revision, mime}, nil
}

func writeItems(ctx context.Context, tx *sql.Tx, userID string, prev, next map[string]ItemState) error {
	changed := []string{}
	for id, item := range next {
		if old, ok := prev[id]; !ok || old != item {
			changed = append(changed, id)
		}
	}
	if len(changed) > 0 {
		upsert, err := tx.PrepareContext(ctx, `INSERT INTO item_state (user_id, item_id, position_ticks, play_count, played, is_favorite, last_played, last_searched, hidden_from_resume)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, item_id) DO UPDATE SET position_ticks = excluded.position_ticks, play_count = excluded.play_count,
				played = excluded.played, is_favorite = excluded.is_favorite, last_played = excluded.last_played,
				last_searched = excluded.last_searched, hidden_from_resume = excluded.hidden_from_resume`)
		if err != nil {
			return err
		}
		defer func() { _ = upsert.Close() }()
		for _, id := range changed {
			if err := upsertItem(ctx, upsert, userID, id, next[id]); err != nil {
				return err
			}
		}
	}
	for id := range prev {
		if _, ok := next[id]; !ok {
			if _, err := tx.ExecContext(ctx, "DELETE FROM item_state WHERE user_id = ? AND item_id = ?", userID, id); err != nil {
				return err
			}
		}
	}
	return nil
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

// validate rejects state that does not satisfy schema v1's invariants.
func validate(d Data) error {
	if d.SchemaVersion != 1 || d.ServerID == "" || d.User.ID == "" || d.User.Name == "" || len(d.User.Salt) != 16 || len(d.User.PasswordHash) != 32 || d.User.Iterations != passwordIterations || d.User.Settings == nil || d.User.Configuration == nil || d.Sessions == nil {
		return errInvalidSchema
	}
	return nil
}
