package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// savedVersion changes whenever the saved form or what a probe records does;
// an index of another version is rejected and the libraries scanned again.
// Version 1 held one directory and version 2 no audio files; both are still
// read.
const savedVersion = 3

const maxSavedBytes = 256 << 20

// savedCatalog is the index Save writes: what probing found in each library
// folder and when each path was first seen. Everything derived from a path
// (ID, name, series and season) is computed again on load.
type savedCatalog struct {
	Version int
	Folders []savedFolder `json:",omitempty"`
	// ID and Items are the one directory of a version 1 index.
	ID    string      `json:",omitempty"`
	Items []savedItem `json:",omitempty"`
}

type savedFolder struct {
	Path  string
	Items []savedItem
}

type savedItem struct {
	Path         string
	Size         int64
	Modified     time.Time
	Added        time.Time
	RunTimeTicks int64
	Streams      []Stream
	Tags         Tags `json:",omitzero"`
}

// Save writes the catalog to file atomically, so a crash leaves either the
// previous index or this one.
func (c *Catalog) Save(file string) error {
	saved := savedCatalog{Version: savedVersion, Folders: []savedFolder{}}
	for _, folder := range slices.Sorted(maps.Keys(c.sources)) {
		src := c.sources[folder]
		f := savedFolder{Path: folder, Items: make([]savedItem, len(src.items))}
		for n, item := range src.items {
			f.Items[n] = savedItem{Path: item.Path, Size: item.Size, Modified: item.Modified, Added: item.Added,
				RunTimeTicks: item.RunTimeTicks, Streams: item.Streams, Tags: item.Tags}
		}
		saved.Folders = append(saved.Folders, f)
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

// Load returns the catalog Save wrote to file for the libraries, without
// probing any media. A folder the index lacks, or whose root cannot be opened,
// stays empty; Rescan to pick it up and the changes made since. A missing
// file returns an error matching fs.ErrNotExist, and an index with none of
// the libraries' folders an error.
func Load(ctx context.Context, libraries []Library, file string) (*Catalog, error) {
	probe, stats, err := newProbe(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := loadLibraries(libraries, file, probe)
	if err != nil {
		return nil, err
	}
	catalog.probeStats = stats
	attachExtractor(catalog)
	return catalog, nil
}

// load reads the index of one directory's default libraries, for tests.
func load(directory, file string, probe func(context.Context, *os.File) (Item, error)) (*Catalog, error) {
	libraries, err := DefaultLibraries(directory)
	if err != nil {
		return nil, err
	}
	return loadLibraries(libraries, file, probe)
}

func loadLibraries(libraries []Library, file string, probe func(context.Context, *os.File) (Item, error)) (*Catalog, error) {
	f, err := os.Open(file) //nolint:gosec // file is in the operator-selected data directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var saved savedCatalog
	if err := json.NewDecoder(io.LimitReader(f, maxSavedBytes)).Decode(&saved); err != nil {
		return nil, errors.New("media index is not readable")
	}
	folders := map[string][]savedItem{}
	switch saved.Version {
	case 1:
		for _, folder := range folderPaths(libraries) {
			if stableID("movies\x00"+folder) == saved.ID {
				folders[folder] = saved.Items
			}
		}
	case 2, savedVersion:
		for _, folder := range saved.Folders {
			if _, duplicate := folders[folder.Path]; duplicate {
				return nil, errors.New("media index has a folder twice")
			}
			folders[folder.Path] = folder.Items
		}
	default:
		return nil, errors.New("media index is for another version")
	}
	catalog := &Catalog{libraries: slices.Clone(libraries), sources: map[string]*source{}, probe: probe}
	videos, tracks := 0, 0
	for _, folder := range folderPaths(libraries) {
		items, found := folders[folder]
		if !found {
			continue
		}
		for _, item := range items {
			if isAudio(strings.ToLower(path.Ext(item.Path))) {
				tracks++
			} else {
				videos++
			}
		}
		if videos > maxMovies || tracks > maxTracks {
			_ = catalog.Close()
			return nil, errors.New("media index has too many media files")
		}
		video, audio := folderKinds(libraries, folder)
		src, err := loadSource(folder, items, video, audio)
		if err != nil {
			_ = catalog.Close()
			return nil, err
		}
		root, err := os.OpenRoot(folder)
		if err != nil {
			continue
		}
		src.root = root
		catalog.sources[folder] = src
	}
	if len(catalog.sources) == 0 {
		return nil, errors.New("media index has none of the libraries' folders")
	}
	catalog.assemble()
	return catalog, nil
}

// loadSource rebuilds a folder's items from the index, rejecting paths that
// could leave the folder. Files of a kind the folder's libraries no longer
// catalog are left for the next rescan.
func loadSource(folder string, saved []savedItem, video, audio bool) (*source, error) {
	src := &source{path: folder, key: stableID("movies\x00" + folder), items: make([]Item, 0, len(saved)), video: video, audio: audio}
	seen := make(map[string]bool, len(saved))
	for _, s := range saved {
		if !filepath.IsLocal(s.Path) || path.Clean(s.Path) != s.Path || seen[s.Path] {
			return nil, errors.New("media index has an invalid path")
		}
		seen[s.Path] = true
		name := path.Base(s.Path)
		ext := strings.ToLower(path.Ext(name))
		music := isAudio(ext)
		if (music && !audio) || (!music && !video) {
			continue
		}
		tags := s.Tags
		if !music {
			tags = Tags{}
		}
		src.items = append(src.items, Item{
			ID: stableID(src.key + "\x00" + s.Path), Name: strings.TrimSuffix(name, path.Ext(name)), Path: s.Path,
			Container: containerOf(ext),
			Size:      s.Size, Modified: s.Modified, Added: s.Added, RunTimeTicks: s.RunTimeTicks, Streams: s.Streams, Tags: tags,
		})
	}
	slices.SortFunc(src.items, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	src.classify()
	return src, nil
}
