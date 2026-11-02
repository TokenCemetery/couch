package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strings"
)

// Library content types.
const (
	Movies  = "movies"
	TVShows = "tvshows"
	Music   = "music"
)

// Library is a set of folders cataloged as one content type. A movies
// library holds the videos that are not recognized episodes; a TV shows
// library holds the episodes with their series and seasons; a music library
// holds the audio files with their albums. Paths are absolute and clean.
type Library struct {
	ID    string
	Name  string
	Type  string
	Paths []string
}

// DefaultLibraries returns the Movies and TV Shows libraries of one
// directory, with the IDs a catalogue of that directory has always had, so
// item IDs and client references survive the move to stored libraries.
func DefaultLibraries(directory string) ([]Library, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("invalid media directory")
	}
	movies := stableID("movies\x00" + absolute)
	return []Library{
		{ID: movies, Name: "Movies", Type: Movies, Paths: []string{absolute}},
		{ID: stableID("series\x00" + movies), Name: "TV Shows", Type: TVShows, Paths: []string{absolute}},
	}, nil
}

// NewLibraryID returns a random ID for a library created later.
func NewLibraryID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Inside reports whether path is root or below it; both are clean and
// absolute.
func Inside(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// Overlap reports whether path is, contains or lies in a folder of another
// library of the same type than library, so a file would appear twice.
func Overlap(libraries []Library, library Library, path string) bool {
	for _, other := range libraries {
		if other.ID == library.ID || other.Type != library.Type {
			continue
		}
		for _, p := range other.Paths {
			if Inside(path, p) || Inside(p, path) {
				return true
			}
		}
	}
	return slices.ContainsFunc(library.Paths, func(p string) bool { return p != path && (Inside(path, p) || Inside(p, path)) })
}

// folderPaths returns every folder of the libraries once, sorted.
func folderPaths(libraries []Library) []string {
	paths := []string{}
	for _, library := range libraries {
		paths = append(paths, library.Paths...)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// folderKinds reports whether the libraries that use folder catalog its
// videos, its audio files or both.
func folderKinds(libraries []Library, folder string) (video, audio bool) {
	for _, library := range libraries {
		if slices.Contains(library.Paths, folder) {
			if library.Type == Music {
				audio = true
			} else {
				video = true
			}
		}
	}
	return video, audio
}

// Libraries returns the catalogue's libraries in their configured order. A
// catalogue built without libraries, as tests do, has a Movies library with
// its ID and, when it has series, a TV Shows library.
func (c *Catalog) Libraries() []Library {
	if c == nil {
		return nil
	}
	if c.sources != nil || c.libraries != nil {
		return c.libraries
	}
	libraries := []Library{{ID: c.ID, Name: "Movies", Type: Movies}}
	if len(c.Folders) > 0 {
		libraries = append(libraries, Library{ID: c.SeriesLibraryID(), Name: "TV Shows", Type: TVShows})
	}
	return libraries
}

// Library returns the library with this ID.
func (c *Catalog) Library(id string) (Library, bool) {
	for _, library := range c.Libraries() {
		if library.ID == id {
			return library, true
		}
	}
	return Library{}, false
}

// Only returns the catalogue as a user limited to the libraries ids sees it:
// those libraries with their items and nothing else. It shares c's open
// folders, so it is never closed itself.
func (c *Catalog) Only(ids []string) *Catalog {
	if c == nil {
		return nil
	}
	libraries := []Library{}
	for _, library := range c.Libraries() {
		if slices.Contains(ids, library.ID) {
			libraries = append(libraries, library)
		}
	}
	if c.sources != nil {
		return c.Reassign(libraries)
	}
	// A catalogue built without libraries keeps its items in place.
	view := &Catalog{ID: c.ID, libraries: libraries, extractor: c.extractor, Items: []Item{}, Folders: []Item{}}
	for _, item := range c.Items {
		if slices.Contains(ids, c.LibraryOf(item)) {
			view.Items = append(view.Items, item)
		}
	}
	for _, folder := range c.Folders {
		if slices.Contains(ids, c.LibraryOf(folder)) {
			view.Folders = append(view.Folders, folder)
		}
	}
	return view
}

// LibraryOf returns the ID of the library that holds item.
func (c *Catalog) LibraryOf(item Item) string {
	switch item.Type() {
	case "Movie":
		return c.Parent(item)
	case "Series", "MusicAlbum":
		return item.ParentID
	case "Audio":
		if album, ok := c.Folder(item.ParentID); ok {
			return album.ParentID
		}
		return item.ParentID
	}
	if series, ok := c.Folder(item.SeriesID); ok {
		return series.ParentID
	}
	return c.SeriesLibraryID()
}

// assemble fills Items and Folders from the scanned folders: each library
// takes its type's share of its folders, and its ID becomes the parent of its
// movies, series, albums and tracks without an album. A file reached twice is
// kept once.
func (c *Catalog) assemble() {
	items, folders := []Item{}, []Item{}
	seen := map[string]bool{}
	add := func(list *[]Item, item Item) {
		if !seen[item.ID] {
			seen[item.ID] = true
			*list = append(*list, item)
		}
	}
	for _, library := range c.libraries {
		for _, path := range library.Paths {
			src := c.sources[path]
			if src == nil {
				continue
			}
			for _, item := range src.items {
				switch {
				case library.Type == Movies && item.Kind == "":
					item.ParentID = library.ID
					add(&items, item)
				case library.Type == TVShows && item.Kind == "Episode":
					add(&items, item)
				case library.Type == Music && item.Kind == "Audio":
					if item.ParentID == "" {
						item.ParentID = library.ID
					}
					add(&items, item)
				}
			}
			for _, folder := range src.folders {
				switch {
				case library.Type == TVShows && folder.Kind == "Series":
					folder.ParentID = library.ID
					add(&folders, folder)
				case library.Type == TVShows && folder.Kind == "Season":
					add(&folders, folder)
				case library.Type == Music && folder.Kind == "MusicAlbum":
					folder.ParentID = library.ID
					add(&folders, folder)
				}
			}
		}
	}
	slices.SortFunc(items, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(folders, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	c.Items, c.Folders = items, folders
}
