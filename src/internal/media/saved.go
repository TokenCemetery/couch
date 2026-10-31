package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// savedVersion changes whenever the saved form or what a probe records does;
// an index of another version is rejected and the library scanned again.
const savedVersion = 1

const maxSavedBytes = 256 << 20

// savedCatalog is the index Save writes: what probing found and when each
// path was first seen. Everything derived from a path (ID, name, series and
// season) is computed again on load.
type savedCatalog struct {
	Version int
	ID      string
	Items   []savedItem
}

type savedItem struct {
	Path         string
	Size         int64
	Modified     time.Time
	Added        time.Time
	RunTimeTicks int64
	Streams      []Stream
}

// Save writes the catalog to file atomically, so a crash leaves either the
// previous index or this one.
func (c *Catalog) Save(file string) error {
	saved := savedCatalog{Version: savedVersion, ID: c.ID, Items: make([]savedItem, len(c.Items))}
	for n, item := range c.Items {
		saved.Items[n] = savedItem{Path: item.Path, Size: item.Size, Modified: item.Modified, Added: item.Added,
			RunTimeTicks: item.RunTimeTicks, Streams: item.Streams}
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".catalog-*")
	if err != nil {
		return err
	}
	// After a successful rename this removal fails harmlessly.
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), file); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// Load returns the catalog Save wrote to file for directory, without probing
// any media. Rescan it to pick up changes made since. A missing file returns
// an error matching fs.ErrNotExist.
func Load(ctx context.Context, directory, file string) (*Catalog, error) {
	probe, stats, err := newProbe(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := load(directory, file, probe)
	if err != nil {
		return nil, err
	}
	catalog.probeStats = stats
	attachExtractor(catalog)
	return catalog, nil
}

func load(directory, file string, probe func(context.Context, *os.File) (Item, error)) (*Catalog, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("invalid media directory")
	}
	f, err := os.Open(file) //nolint:gosec // file is in the operator-selected data directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var saved savedCatalog
	if err := json.NewDecoder(io.LimitReader(f, maxSavedBytes)).Decode(&saved); err != nil {
		return nil, errors.New("media index is not readable")
	}
	id := stableID("movies\x00" + absolute)
	if saved.Version != savedVersion || saved.ID != id {
		return nil, errors.New("media index is for another version or directory")
	}
	if len(saved.Items) == 0 || len(saved.Items) > maxMovies {
		return nil, errors.New("media index has no videos or too many")
	}
	catalog := &Catalog{ID: id, Items: make([]Item, 0, len(saved.Items)), probe: probe}
	seen := make(map[string]bool, len(saved.Items))
	for _, s := range saved.Items {
		if !filepath.IsLocal(s.Path) || path.Clean(s.Path) != s.Path || seen[s.Path] {
			return nil, errors.New("media index has an invalid path")
		}
		seen[s.Path] = true
		name := path.Base(s.Path)
		catalog.Items = append(catalog.Items, Item{
			ID: stableID(id + "\x00" + s.Path), Name: strings.TrimSuffix(name, path.Ext(name)), Path: s.Path,
			Container: strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")),
			Size:      s.Size, Modified: s.Modified, Added: s.Added, RunTimeTicks: s.RunTimeTicks, Streams: s.Streams,
		})
	}
	slices.SortFunc(catalog.Items, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	catalog.groupEpisodes()
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, errors.New("cannot open media directory")
	}
	catalog.root = root
	return catalog, nil
}
