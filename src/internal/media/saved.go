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

	"github.com/TokenCemetery/couch/internal/state"
)

// savedVersion is the last version of catalog.json, the index before it was
// kept in the database; such a file is read once to import it. Version 1
// held one directory, version 2 no audio files and version 4 no photos;
// they are still read. Version 3 lacked embedded covers and is not.
// Versions before 6 lacked the colour properties that tell HDR from SDR:
// their videos are marked to be probed again by the background rescan.
const savedVersion = 6

const maxSavedBytes = 256 << 20

// savedCatalog is the form of catalog.json: what probing found in each
// library folder and when each path was first seen.
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
	Tags         Tags  `json:",omitzero"`
	Cover        Image `json:",omitzero"`
	Photo        Photo `json:",omitzero"`
	// Reprobe keeps an item's mark until a rescan probes it again.
	Reprobe bool `json:",omitempty"`
}

// savedProbe is what a state.CatalogEntry's Probe holds. Everything derived
// from a path (ID, name, series and season) is computed again on load.
type savedProbe struct {
	RunTimeTicks int64
	Streams      []Stream
	Tags         Tags  `json:",omitzero"`
	Cover        Image `json:",omitzero"`
	Photo        Photo `json:",omitzero"`
}

// Index returns the catalog's saved form: the folders it scanned and one
// entry per file, in folder and ID order, with times in UTC.
func (c *Catalog) Index() (state.CatalogIndex, error) {
	index := state.CatalogIndex{Folders: slices.Sorted(maps.Keys(c.sources)), Entries: []state.CatalogEntry{}}
	for _, folder := range index.Folders {
		for _, item := range c.sources[folder].items {
			probe, err := json.Marshal(savedProbe{RunTimeTicks: item.RunTimeTicks, Streams: item.Streams, Tags: item.Tags, Cover: item.Cover, Photo: item.Photo})
			if err != nil {
				return state.CatalogIndex{}, err
			}
			index.Entries = append(index.Entries, state.CatalogEntry{Folder: folder, Path: item.Path, Size: item.Size, Modified: item.Modified.UTC(), Added: item.Added.UTC(),
				Reprobe: item.reprobe, Probe: string(probe)})
		}
	}
	return index, nil
}

// ReadIndexFile returns the index a catalog.json holds for the libraries. A
// missing file returns an error matching fs.ErrNotExist.
func ReadIndexFile(libraries []Library, file string) (state.CatalogIndex, error) {
	f, err := os.Open(file) //nolint:gosec // file is in the operator-selected data directory
	if err != nil {
		return state.CatalogIndex{}, err
	}
	defer func() { _ = f.Close() }()
	var saved savedCatalog
	if err := json.NewDecoder(io.LimitReader(f, maxSavedBytes)).Decode(&saved); err != nil {
		return state.CatalogIndex{}, errors.New("media index is not readable")
	}
	var folders []savedFolder
	switch saved.Version {
	case 1:
		for _, folder := range folderPaths(libraries) {
			if stableID("movies\x00"+folder) == saved.ID {
				folders = append(folders, savedFolder{Path: folder, Items: saved.Items})
			}
		}
	case 2, 4, 5, savedVersion:
		folders = saved.Folders
	default:
		return state.CatalogIndex{}, errors.New("media index is for another version")
	}
	index := state.CatalogIndex{Folders: []string{}, Entries: []state.CatalogEntry{}}
	for _, folder := range folders {
		if slices.Contains(index.Folders, folder.Path) {
			return state.CatalogIndex{}, errors.New("media index has a folder twice")
		}
		index.Folders = append(index.Folders, folder.Path)
		for _, s := range folder.Items {
			probe, err := json.Marshal(savedProbe{RunTimeTicks: s.RunTimeTicks, Streams: s.Streams, Tags: s.Tags, Cover: s.Cover, Photo: s.Photo})
			if err != nil {
				return state.CatalogIndex{}, err
			}
			index.Entries = append(index.Entries, state.CatalogEntry{Folder: folder.Path, Path: s.Path, Size: s.Size, Modified: s.Modified.UTC(), Added: s.Added.UTC(),
				Reprobe: s.Reprobe || saved.Version < 6, Probe: string(probe)})
		}
	}
	return index, nil
}

// Load returns the catalog of the libraries from the saved index, without
// probing any media. A folder the index lacks, or whose root cannot
// be opened, stays empty; Rescan to pick it up and the changes made since.
// An index with none of the libraries' folders returns an error.
func Load(ctx context.Context, libraries []Library, index state.CatalogIndex) (*Catalog, error) {
	probe, stats, err := newProbe(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := loadLibraries(libraries, index, probe)
	if err != nil {
		return nil, err
	}
	catalog.probeStats = stats
	attachExtractor(catalog)
	return catalog, nil
}

// load reads the index of one directory's default libraries, for tests.
func load(directory string, index state.CatalogIndex, probe func(context.Context, *os.File) (Item, error)) (*Catalog, error) {
	libraries, err := DefaultLibraries(directory)
	if err != nil {
		return nil, err
	}
	return loadLibraries(libraries, index, probe)
}

func loadLibraries(libraries []Library, index state.CatalogIndex, probe func(context.Context, *os.File) (Item, error)) (*Catalog, error) {
	folders := map[string][]state.CatalogEntry{}
	for _, folder := range index.Folders {
		folders[folder] = []state.CatalogEntry{}
	}
	for _, e := range index.Entries {
		if _, listed := folders[e.Folder]; listed {
			folders[e.Folder] = append(folders[e.Folder], e)
		}
	}
	catalog := &Catalog{libraries: slices.Clone(libraries), sources: map[string]*source{}, probe: probe}
	videos, tracks, photos := 0, 0, 0
	for _, folder := range folderPaths(libraries) {
		items, found := folders[folder]
		if !found {
			continue
		}
		for _, item := range items {
			switch ext := strings.ToLower(path.Ext(item.Path)); {
			case isAudio(ext):
				tracks++
			case isPhoto(ext):
				photos++
			default:
				videos++
			}
		}
		if videos > maxMovies || tracks > maxTracks || photos > maxPhotos {
			_ = catalog.Close()
			return nil, errors.New("media index has too many media files")
		}
		video, audio, photo := folderKinds(libraries, folder)
		src, err := loadSource(folder, items, video, audio, photo)
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
// catalog are left for the next rescan, and only videos keep a reprobe mark.
// A file whose probe cannot be read keeps its date added and is marked for
// the next rescan to probe again.
func loadSource(folder string, saved []state.CatalogEntry, video, audio, photo bool) (*source, error) {
	src := &source{path: folder, key: stableID("movies\x00" + folder), items: make([]Item, 0, len(saved)), video: video, audio: audio, photo: photo}
	seen := make(map[string]bool, len(saved))
	for _, s := range saved {
		if !filepath.IsLocal(s.Path) || path.Clean(s.Path) != s.Path || seen[s.Path] {
			return nil, errors.New("media index has an invalid path")
		}
		seen[s.Path] = true
		name := path.Base(s.Path)
		ext := strings.ToLower(path.Ext(name))
		music, picture := isAudio(ext), isPhoto(ext)
		if (music && !audio) || (picture && !photo) || (!music && !picture && !video) {
			continue
		}
		var probe savedProbe
		unreadable := json.Unmarshal([]byte(s.Probe), &probe) != nil
		if unreadable {
			probe = savedProbe{}
		}
		tags, cover, pictured := probe.Tags, probe.Cover, probe.Photo
		if !music {
			tags, cover = Tags{}, Image{}
		}
		if !picture {
			pictured = Photo{}
		}
		src.items = append(src.items, Item{
			ID: stableID(src.key + "\x00" + s.Path), Name: strings.TrimSuffix(name, path.Ext(name)), Path: s.Path,
			Container: containerOf(ext),
			Size:      s.Size, Modified: s.Modified, Added: s.Added, RunTimeTicks: probe.RunTimeTicks, Streams: probe.Streams, Tags: tags, Cover: cover, Photo: pictured,
			reprobe: unreadable || (!music && !picture && s.Reprobe),
		})
	}
	slices.SortFunc(src.items, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	src.classify()
	return src, nil
}
