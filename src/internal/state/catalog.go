package state

import (
	"context"
	"time"
)

// CatalogEntry is one file of the saved media index. The state package keeps
// Probe opaque; package media encodes it.
type CatalogEntry struct {
	Folder, Path    string
	Size            int64
	Modified, Added time.Time
	// Reprobe marks a file whose probe the next rescan repeats.
	Reprobe bool
	Probe   string
}

// CatalogIndex is the saved media index. Folders are the library folders it
// describes, also those without files; Entries are their files.
type CatalogIndex struct {
	Folders []string
	Entries []CatalogEntry
}

// CatalogChange is what a scan changed in the saved media index.
type CatalogChange struct {
	// Folders are the folders scanned, recorded also when they hold no files.
	Folders []string
	Write   []CatalogEntry
	// Delete holds the entries to remove; only Folder and Path are used.
	Delete []CatalogEntry
	// Drop are folders no longer in any library, removed with their entries.
	Drop []string
}

// Catalog returns the saved media index, empty when none was saved.
func (s *Store) Catalog(ctx context.Context) (CatalogIndex, error) {
	index := CatalogIndex{Folders: []string{}, Entries: []CatalogEntry{}}
	folders, err := s.db.QueryContext(ctx, "SELECT folder FROM catalog_folders")
	if err != nil {
		return CatalogIndex{}, err
	}
	defer func() { _ = folders.Close() }()
	for folders.Next() {
		var folder string
		if err := folders.Scan(&folder); err != nil {
			return CatalogIndex{}, err
		}
		index.Folders = append(index.Folders, folder)
	}
	if err := folders.Err(); err != nil {
		return CatalogIndex{}, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT folder, path, size, modified, added, reprobe, probe FROM catalog")
	if err != nil {
		return CatalogIndex{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e CatalogEntry
		var modified, added string
		if err := rows.Scan(&e.Folder, &e.Path, &e.Size, &modified, &added, &e.Reprobe, &e.Probe); err != nil {
			return CatalogIndex{}, err
		}
		if err := e.Modified.UnmarshalText([]byte(modified)); err != nil {
			return CatalogIndex{}, err
		}
		if err := e.Added.UnmarshalText([]byte(added)); err != nil {
			return CatalogIndex{}, err
		}
		index.Entries = append(index.Entries, e)
	}
	return index, rows.Err()
}

// ChangeCatalog applies a change to the saved media index in one transaction.
func (s *Store) ChangeCatalog(ctx context.Context, change CatalogChange) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, folder := range change.Folders {
		if _, err := tx.ExecContext(ctx, "INSERT INTO catalog_folders (folder) VALUES (?) ON CONFLICT DO NOTHING", folder); err != nil {
			return err
		}
	}
	for _, e := range change.Write {
		modified, err := timeText(e.Modified)
		if err != nil {
			return err
		}
		added, err := timeText(e.Added)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog (folder, path, size, modified, added, reprobe, probe) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (folder, path) DO UPDATE SET size = excluded.size, modified = excluded.modified, added = excluded.added,
				reprobe = excluded.reprobe, probe = excluded.probe`,
			e.Folder, e.Path, e.Size, modified, added, e.Reprobe, e.Probe); err != nil {
			return err
		}
	}
	for _, e := range change.Delete {
		if _, err := tx.ExecContext(ctx, "DELETE FROM catalog WHERE folder = ? AND path = ?", e.Folder, e.Path); err != nil {
			return err
		}
	}
	for _, folder := range change.Drop {
		if _, err := tx.ExecContext(ctx, "DELETE FROM catalog WHERE folder = ?", folder); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM catalog_folders WHERE folder = ?", folder); err != nil {
			return err
		}
	}
	return tx.Commit()
}
