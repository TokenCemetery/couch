package state

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Backup writes a consistent copy of the state in dir to dest, a new or empty
// directory: the database through VACUUM INTO, then the uploaded image files that copy
// references. It does not take the data directory lock, so it works while
// Couch runs; a commit waits for the snapshot instead of interleaving with it.
// The copy is loaded and validated as Open would before Backup succeeds.
func Backup(dir, dest string) error {
	if err := os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	if entries, err := os.ReadDir(dest); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dest)
	}
	abs, err := filepath.Abs(filepath.Join(dir, "couch.db"))
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	src, err := sql.Open("sqlite", "file:"+(&url.URL{Path: abs}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	src.SetMaxOpenConns(1)
	// An image can be replaced between the snapshot and the copy of its
	// file; the next snapshot then references the new file.
	for range 3 {
		err = backupOnce(context.Background(), src, dir, dest)
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return err
}

func backupOnce(ctx context.Context, src *sql.DB, dir, dest string) error {
	target := filepath.Join(dest, "couch.db")
	_ = os.Remove(target)
	db, err := openDB(target)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := src.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return err
	}
	d, err := loadDB(ctx, db)
	if err != nil {
		return err
	}
	if err := validate(d); err != nil {
		return err
	}
	if err := syncFile(target); err != nil {
		return err
	}
	copied := map[string]bool{}
	for _, image := range d.images() {
		if image == nil || copied[image.Revision] {
			continue
		}
		copied[image.Revision] = true
		if b, err := hex.DecodeString(image.Revision); err != nil || len(b) != 16 {
			return fmt.Errorf("invalid image revision %q", image.Revision)
		}
		name := "user-image-" + image.Revision
		content, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // dir is the operator-selected data directory; the revision is checked hex
		if err != nil {
			return err
		}
		if err := writeFileSynced(filepath.Join(dest, name), content); err != nil {
			return err
		}
	}
	return syncFile(dest)
}

// syncFile flushes a file or directory to disk.
func syncFile(name string) error {
	f, err := os.Open(name) //nolint:gosec // name is in the operator-selected data or backup directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
