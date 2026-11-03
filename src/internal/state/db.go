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
const dbSchemaVersion = 16

const dbSchema = `
CREATE TABLE server (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	server_id TEXT NOT NULL,
	libraries_configured INTEGER NOT NULL,
	server_name TEXT NOT NULL
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
` + metadataSchema + librariesSchema + activitySchema + identifiedColumn + collectionsSchema + playlistsSchema + homeSectionsColumn + catalogSchema + maintenanceColumns + monitorColumn + setColumns + imageColumns + `
PRAGMA user_version = 16;
`

// lyricsReprobe was run in version 16: tracks, the files whose probe has an
// audio stream and no video stream, are probed again for their lyrics tags,
// which earlier probes did not read.
const lyricsReprobe = `
UPDATE catalog SET reprobe = 1 WHERE CASE WHEN json_valid(probe) THEN
	EXISTS (SELECT 1 FROM json_each(probe, '$.Streams') WHERE value ->> 'Type' = 'Audio')
	AND NOT EXISTS (SELECT 1 FROM json_each(probe, '$.Streams') WHERE value ->> 'Type' = 'Video')
END;
`

// imageColumns were added in version 15: the Primary image uploaded for a
// collection or playlist and its size, NULL when there is none.
const imageColumns = `
ALTER TABLE collections ADD COLUMN image_revision TEXT;
ALTER TABLE collections ADD COLUMN image_mime TEXT;
ALTER TABLE collections ADD COLUMN image_width INTEGER;
ALTER TABLE collections ADD COLUMN image_height INTEGER;
ALTER TABLE playlists ADD COLUMN image_revision TEXT;
ALTER TABLE playlists ADD COLUMN image_mime TEXT;
ALTER TABLE playlists ADD COLUMN image_width INTEGER;
ALTER TABLE playlists ADD COLUMN image_height INTEGER;
`

// setColumns were added in version 14: the movie set an automatic
// collection follows, the members it added and those an administrator
// declined as JSON arrays, and the sets whose automatic collection an
// administrator deleted.
const setColumns = `
ALTER TABLE collections ADD COLUMN set_key TEXT NOT NULL DEFAULT '';
ALTER TABLE collections ADD COLUMN set_items TEXT NOT NULL DEFAULT '[]';
ALTER TABLE collections ADD COLUMN declined TEXT NOT NULL DEFAULT '[]';
CREATE TABLE dismissed_sets (set_key TEXT PRIMARY KEY) STRICT, WITHOUT ROWID;
`

// monitorColumn was added in version 13: real-time monitoring, on for
// libraries stored before, as Emby turns it on for a new library.
const monitorColumn = "ALTER TABLE libraries ADD COLUMN realtime_monitor INTEGER NOT NULL DEFAULT 1;"

// maintenanceColumns were added in version 12: Emby's database settings.
// vacuum_on_startup asks for one VACUUM at the next start, which clears it;
// optimize_on_shutdown runs PRAGMA optimize when Couch stops.
const maintenanceColumns = `
ALTER TABLE server ADD COLUMN vacuum_on_startup INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server ADD COLUMN optimize_on_shutdown INTEGER NOT NULL DEFAULT 1;
`

// catalogSchema was added in version 11: the media index, which until then
// was catalog.json. catalog_folders lists the library folders it describes,
// also those without files, and catalog holds one row per file. probe holds
// the probe results as JSON; a release whose probe records a new field sets
// reprobe on the rows it should probe again.
const catalogSchema = `
CREATE TABLE catalog_folders (
	folder TEXT PRIMARY KEY
) STRICT, WITHOUT ROWID;
CREATE TABLE catalog (
	folder TEXT NOT NULL,
	path TEXT NOT NULL,
	size INTEGER NOT NULL,
	modified TEXT NOT NULL,
	added TEXT NOT NULL,
	reprobe INTEGER NOT NULL,
	probe TEXT NOT NULL,
	PRIMARY KEY (folder, path)
) STRICT, WITHOUT ROWID;
`

// homeSectionsColumn was added in version 10: the user's home screen as a
// JSON array of HomeSection, NULL until the user first changes it.
const homeSectionsColumn = "ALTER TABLE users ADD COLUMN home_sections TEXT;"

// playlistsSchema was added in version 9; entries is a JSON array of
// {ID, ItemID} objects.
const playlistsSchema = `
CREATE TABLE playlists (
	id TEXT PRIMARY KEY,
	position INTEGER NOT NULL,
	user_id TEXT NOT NULL,
	name TEXT NOT NULL,
	added TEXT NOT NULL,
	modified TEXT NOT NULL,
	entries TEXT NOT NULL
) STRICT;
`

// collectionsSchema was added in version 8; items is a JSON array of item IDs.
const collectionsSchema = `
CREATE TABLE collections (
	id TEXT PRIMARY KEY,
	position INTEGER NOT NULL,
	name TEXT NOT NULL,
	added TEXT NOT NULL,
	items TEXT NOT NULL
) STRICT;
`

// identifiedColumn was added in version 7: the provider IDs an administrator
// chose with Identify, as JSON; empty when none.
const identifiedColumn = "ALTER TABLE metadata ADD COLUMN identified TEXT NOT NULL DEFAULT '';"

// activitySchema was added in version 6.
const activitySchema = `
CREATE TABLE activity (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	date TEXT NOT NULL,
	type TEXT NOT NULL,
	name TEXT NOT NULL,
	overview TEXT NOT NULL,
	severity TEXT NOT NULL,
	user_id TEXT NOT NULL,
	item_id TEXT NOT NULL
) STRICT;
`

// librariesSchema was added in version 5.
const librariesSchema = `
CREATE TABLE libraries (
	id TEXT PRIMARY KEY,
	position INTEGER NOT NULL,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	paths TEXT NOT NULL
) STRICT;
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
	// Version 5 stores the libraries; until one is configured, -media-dir
	// creates them.
	4: "ALTER TABLE server ADD COLUMN libraries_configured INTEGER NOT NULL DEFAULT 0;" + librariesSchema + "PRAGMA user_version = 5;",
	// Version 6 records the activity log and the server name set in Emby
	// Web; until one is set, -name names the server.
	5: "ALTER TABLE server ADD COLUMN server_name TEXT NOT NULL DEFAULT '';" + activitySchema + "PRAGMA user_version = 6;",
	// Version 7 stores the identification an administrator chose.
	6: identifiedColumn + "PRAGMA user_version = 7;",
	// Version 8 stores collections.
	7: collectionsSchema + "PRAGMA user_version = 8;",
	// Version 9 stores playlists.
	8: playlistsSchema + "PRAGMA user_version = 9;",
	// Version 10 stores home screens arranged in Emby Web.
	9: homeSectionsColumn + "PRAGMA user_version = 10;",
	// Version 11 stores the media index; the first start imports
	// catalog.json.
	10: catalogSchema + "PRAGMA user_version = 11;",
	// Version 12 stores the database maintenance settings.
	11: maintenanceColumns + "PRAGMA user_version = 12;",
	// Version 13 stores real-time monitoring per library.
	12: monitorColumn + "PRAGMA user_version = 13;",
	// Version 14 stores collections created from movie sets.
	13: setColumns + "PRAGMA user_version = 14;",
	// Version 15 stores images uploaded for collections and playlists.
	14: imageColumns + "PRAGMA user_version = 15;",
	// Version 16 probes audio files again for their lyrics.
	15: lyricsReprobe + "PRAGMA user_version = 16;",
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
	if err := db.QueryRowContext(ctx, "SELECT server_id, libraries_configured, server_name, vacuum_on_startup, optimize_on_shutdown FROM server").Scan(
		&d.ServerID, &d.LibrariesConfigured, &d.ServerName, &d.VacuumOnStartup, &d.OptimizeOnShutdown); err != nil {
		return Data{}, err
	}
	libraries, err := loadLibraries(ctx, db)
	if err != nil {
		return Data{}, err
	}
	d.Libraries = libraries
	if d.Collections, d.DismissedSets, err = loadCollections(ctx, db); err != nil {
		return Data{}, err
	}
	if d.Playlists, err = loadPlaylists(ctx, db); err != nil {
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
		is_administrator, is_disabled, is_hidden, enable_all_folders, enabled_folders, home_sections FROM users`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	users := map[string]User{}
	for rows.Next() {
		var u User
		var configuration, settings, folders string
		var revision, mime, sections sql.NullString
		p := &u.Policy
		if err := rows.Scan(&u.ID, &u.Name, &u.Salt, &u.PasswordHash, &u.Iterations, &configuration, &settings, &revision, &mime,
			&p.IsAdministrator, &p.IsDisabled, &p.IsHidden, &p.EnableAllFolders, &folders, &sections); err != nil {
			return nil, err
		}
		if sections.Valid {
			if err := json.Unmarshal([]byte(sections.String), &u.HomeSections); err != nil {
				return nil, err
			}
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
		u.Image = nullImage(revision, mime, sql.NullInt64{}, sql.NullInt64{})
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

// nullImage is the uploaded image stored in the image_revision, image_mime,
// image_width and image_height columns, or nil when they are NULL.
func nullImage(revision, mime sql.NullString, width, height sql.NullInt64) *UserImage {
	if !revision.Valid {
		return nil
	}
	return &UserImage{Revision: revision.String, MIME: mime.String, Width: int(width.Int64), Height: int(height.Int64)}
}

// imageColumnValues are the image_revision, image_mime, image_width and
// image_height values of image, NULL for none.
func imageColumnValues(image *UserImage) (revision, mime, width, height any) {
	if image == nil {
		return nil, nil, nil, nil
	}
	return image.Revision, image.MIME, image.Width, image.Height
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
		if _, err := tx.ExecContext(ctx, "INSERT INTO server (id, server_id, libraries_configured, server_name, vacuum_on_startup, optimize_on_shutdown) VALUES (1, ?, 0, '', ?, ?)",
			next.ServerID, next.VacuumOnStartup, next.OptimizeOnShutdown); err != nil {
			return err
		}
		for _, u := range next.Users {
			if err := writeUser(ctx, tx, User{}, u, slices.Collect(maps.Keys(u.Items))); err != nil {
				return err
			}
		}
		if err := writeLibraries(ctx, tx, Data{}, next); err != nil {
			return err
		}
		if err := writeCollections(ctx, tx, Data{}, next); err != nil {
			return err
		}
		if err := writePlaylists(ctx, tx, Data{}, next); err != nil {
			return err
		}
		return writeSessions(ctx, tx, prev.Sessions, next.Sessions)
	}
	if err := writeLibraries(ctx, tx, prev, next); err != nil {
		return err
	}
	if err := writeCollections(ctx, tx, prev, next); err != nil {
		return err
	}
	if err := writePlaylists(ctx, tx, prev, next); err != nil {
		return err
	}
	if prev.ServerName != next.ServerName {
		if _, err := tx.ExecContext(ctx, "UPDATE server SET server_name = ?", next.ServerName); err != nil {
			return err
		}
	}
	if prev.VacuumOnStartup != next.VacuumOnStartup || prev.OptimizeOnShutdown != next.OptimizeOnShutdown {
		if _, err := tx.ExecContext(ctx, "UPDATE server SET vacuum_on_startup = ?, optimize_on_shutdown = ?", next.VacuumOnStartup, next.OptimizeOnShutdown); err != nil {
			return err
		}
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
				is_administrator, is_disabled, is_hidden, enable_all_folders, enabled_folders, home_sections)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, salt = excluded.salt, password_hash = excluded.password_hash,
				iterations = excluded.iterations, configuration = excluded.configuration, settings = excluded.settings,
				image_revision = excluded.image_revision, image_mime = excluded.image_mime, is_administrator = excluded.is_administrator,
				is_disabled = excluded.is_disabled, is_hidden = excluded.is_hidden, enable_all_folders = excluded.enable_all_folders,
				enabled_folders = excluded.enabled_folders, home_sections = excluded.home_sections`, nextRow...); err != nil {
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
	revision, mime, _, _ := imageColumnValues(u.Image)
	var sections any
	if u.HomeSections != nil {
		text, err := json.Marshal(u.HomeSections)
		if err != nil {
			return nil, err
		}
		sections = string(text)
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
		p.IsAdministrator, p.IsDisabled, p.IsHidden, p.EnableAllFolders, string(folders), sections}, nil
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
	if d.SchemaVersion != 1 || d.ServerID == "" || len(d.Users) == 0 || d.Sessions == nil || d.Libraries == nil {
		return errInvalidSchema
	}
	for _, library := range d.Libraries {
		if library.ID == "" || library.Name == "" || library.Type == "" || library.Paths == nil {
			return errInvalidSchema
		}
	}
	for _, c := range d.Collections {
		if c.ID == "" || c.Name == "" || c.Items == nil {
			return errInvalidSchema
		}
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

func loadLibraries(ctx context.Context, db *sql.DB) ([]Library, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, name, type, paths, realtime_monitor FROM libraries ORDER BY position")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	libraries := []Library{}
	for rows.Next() {
		var library Library
		var paths string
		if err := rows.Scan(&library.ID, &library.Name, &library.Type, &paths, &library.Monitor); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(paths), &library.Paths); err != nil {
			return nil, err
		}
		libraries = append(libraries, library)
	}
	return libraries, rows.Err()
}

// writeLibraries rewrites the libraries when they or the configured flag
// changed; there are few, and their order is part of them.
func writeLibraries(ctx context.Context, tx *sql.Tx, prev, next Data) error {
	if prev.LibrariesConfigured == next.LibrariesConfigured && reflect.DeepEqual(prev.Libraries, next.Libraries) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE server SET libraries_configured = ?", next.LibrariesConfigured); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM libraries"); err != nil {
		return err
	}
	for n, library := range next.Libraries {
		paths, err := json.Marshal(library.Paths)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO libraries (id, position, name, type, paths, realtime_monitor) VALUES (?, ?, ?, ?, ?, ?)",
			library.ID, n, library.Name, library.Type, string(paths), library.Monitor); err != nil {
			return err
		}
	}
	return nil
}

func loadCollections(ctx context.Context, db *sql.DB) ([]Collection, []string, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, name, added, items, set_key, set_items, declined, image_revision, image_mime, image_width, image_height FROM collections ORDER BY position")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var collections []Collection
	for rows.Next() {
		var c Collection
		var added, items, setItems, declined string
		var revision, mime sql.NullString
		var width, height sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Name, &added, &items, &c.Set, &setItems, &declined, &revision, &mime, &width, &height); err != nil {
			return nil, nil, err
		}
		c.Image = nullImage(revision, mime, width, height)
		if err := c.Added.UnmarshalText([]byte(added)); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(items), &c.Items); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(setItems), &c.SetItems); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(declined), &c.Declined); err != nil {
			return nil, nil, err
		}
		collections = append(collections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	dismissed, err := db.QueryContext(ctx, "SELECT set_key FROM dismissed_sets ORDER BY set_key")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = dismissed.Close() }()
	var sets []string
	for dismissed.Next() {
		var key string
		if err := dismissed.Scan(&key); err != nil {
			return nil, nil, err
		}
		sets = append(sets, key)
	}
	return collections, sets, dismissed.Err()
}

// writeCollections rewrites the collections when any changed, like the
// libraries; an administrator edits them rarely.
func writeCollections(ctx context.Context, tx *sql.Tx, prev, next Data) error {
	if !slices.Equal(prev.DismissedSets, next.DismissedSets) {
		if _, err := tx.ExecContext(ctx, "DELETE FROM dismissed_sets"); err != nil {
			return err
		}
		for _, key := range next.DismissedSets {
			if _, err := tx.ExecContext(ctx, "INSERT INTO dismissed_sets (set_key) VALUES (?)", key); err != nil {
				return err
			}
		}
	}
	if reflect.DeepEqual(prev.Collections, next.Collections) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM collections"); err != nil {
		return err
	}
	for n, c := range next.Collections {
		items, err := json.Marshal(c.Items)
		if err != nil {
			return err
		}
		setItems, err := json.Marshal(c.SetItems)
		if err != nil {
			return err
		}
		declined, err := json.Marshal(c.Declined)
		if err != nil {
			return err
		}
		added, err := timeText(c.Added)
		if err != nil {
			return err
		}
		revision, mime, width, height := imageColumnValues(c.Image)
		if _, err := tx.ExecContext(ctx, `INSERT INTO collections (id, position, name, added, items, set_key, set_items, declined, image_revision, image_mime, image_width, image_height)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, n, c.Name, added, string(items), c.Set, string(setItems), string(declined), revision, mime, width, height); err != nil {
			return err
		}
	}
	return nil
}

func loadPlaylists(ctx context.Context, db *sql.DB) ([]Playlist, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, user_id, name, added, modified, entries, image_revision, image_mime, image_width, image_height FROM playlists ORDER BY position")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var playlists []Playlist
	for rows.Next() {
		var p Playlist
		var added, modified, entries string
		var revision, mime sql.NullString
		var width, height sql.NullInt64
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &added, &modified, &entries, &revision, &mime, &width, &height); err != nil {
			return nil, err
		}
		p.Image = nullImage(revision, mime, width, height)
		if err := p.Added.UnmarshalText([]byte(added)); err != nil {
			return nil, err
		}
		if err := p.Modified.UnmarshalText([]byte(modified)); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(entries), &p.Entries); err != nil {
			return nil, err
		}
		playlists = append(playlists, p)
	}
	return playlists, rows.Err()
}

// writePlaylists writes the playlists that changed and deletes the removed
// ones: users edit them as often as they watch.
func writePlaylists(ctx context.Context, tx *sql.Tx, prev, next Data) error {
	if reflect.DeepEqual(prev.Playlists, next.Playlists) {
		return nil
	}
	for _, p := range prev.Playlists {
		if _, ok := next.Playlist(p.ID); !ok {
			if _, err := tx.ExecContext(ctx, "DELETE FROM playlists WHERE id = ?", p.ID); err != nil {
				return err
			}
		}
	}
	for n, p := range next.Playlists {
		// A playlist at the same position and unchanged keeps its row.
		if n < len(prev.Playlists) && reflect.DeepEqual(prev.Playlists[n], p) {
			continue
		}
		entries, err := json.Marshal(p.Entries)
		if err != nil {
			return err
		}
		added, err := timeText(p.Added)
		if err != nil {
			return err
		}
		modified, err := timeText(p.Modified)
		if err != nil {
			return err
		}
		revision, mime, width, height := imageColumnValues(p.Image)
		if _, err := tx.ExecContext(ctx, `INSERT INTO playlists (id, position, user_id, name, added, modified, entries, image_revision, image_mime, image_width, image_height)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET position = excluded.position, name = excluded.name, modified = excluded.modified, entries = excluded.entries,
				image_revision = excluded.image_revision, image_mime = excluded.image_mime, image_width = excluded.image_width, image_height = excluded.image_height`,
			p.ID, n, p.UserID, p.Name, added, modified, string(entries), revision, mime, width, height); err != nil {
			return err
		}
	}
	return nil
}
