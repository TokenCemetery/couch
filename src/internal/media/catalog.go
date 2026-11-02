// Package media builds a read-only video and music catalogue from the folders
// of the configured libraries.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const maxEntries = 100000
const maxMovies = 10000
const maxTracks = 10000

// Catalog and its items are immutable after a scan returns. Each folder's
// root stays open so playback can reopen a file later without re-resolving
// its path outside the folders the administrator selected.
type Catalog struct {
	// ID is the Movies library of a catalogue built without libraries, as
	// tests do; a scanned catalogue's libraries are in Libraries.
	ID        string
	Items     []Item
	Folders   []Item
	Skipped   int
	libraries []Library
	// sources holds each scanned folder by its path.
	sources map[string]*source
	// extractor delivers embedded text subtitles; nil without FFmpeg.
	extractor *subtitleExtractor
	probe     func(context.Context, *os.File) (Item, error)
	// Probes is what FFprobe cost in the scan that built the catalogue;
	// probeStats collects it and is nil when the probe is not FFprobe.
	Probes     ProbeStats
	probeStats *ProbeStats
	index      catalogIndex
	imageDirs  imageDirCache
}

// Item is a movie, series, season, episode, music album or audio track in
// the catalogue.
type Item struct {
	ID            string
	Name          string
	Kind          string
	ParentID      string
	SeriesID      string
	SeriesName    string
	SeasonID      string
	SeasonNumber  int
	EpisodeNumber int
	// Path is relative to the catalogue root and is never sent to clients.
	Path      string
	Container string
	Size      int64
	Modified  time.Time
	// Added is when Couch first saw the path: the file's mtime for files
	// present at startup, the rescan time for files found later.
	Added        time.Time
	RunTimeTicks int64
	Streams      []Stream
	// Tags are an audio file's metadata, completed from its path by
	// classifyMusic; empty for videos.
	Tags Tags
	// Cover is the picture embedded in an audio file, without a Revision;
	// zero when there is none, and for videos.
	Cover Image
	// src is the folder the item was found in.
	src *source
}

// Tags are an audio file's metadata, as probed. Track and Disc are 0 and
// strings empty when the file does not say.
type Tags struct {
	Title       string   `json:",omitempty"`
	Album       string   `json:",omitempty"`
	AlbumArtist string   `json:",omitempty"`
	Artists     []string `json:",omitempty"`
	Genres      []string `json:",omitempty"`
	Track       int      `json:",omitempty"`
	Disc        int      `json:",omitempty"`
	Year        int      `json:",omitempty"`
}

// source is one library folder, walked once however many libraries share it.
type source struct {
	path string
	// key prefixes the IDs of the folder's items.
	key     string
	root    *os.Root
	items   []Item
	folders []Item
	skipped int
	// video and audio say which files the folder's libraries catalog.
	video, audio bool
}

// sourceOf returns item's folder. An item built without one, as tests do,
// belongs to the catalogue's only folder.
func (c *Catalog) sourceOf(item Item) *source {
	if item.src != nil {
		return item.src
	}
	if c != nil && len(c.sources) == 1 {
		for _, src := range c.sources {
			return src
		}
	}
	return nil
}

// rootOf returns the root of item's folder, or nil.
func (c *Catalog) rootOf(item Item) *os.Root {
	if src := c.sourceOf(item); src != nil {
		return src.root
	}
	return nil
}

// Open reopens an item's file read-only, confined to its library folder.
func (c *Catalog) Open(item Item) (*os.File, error) {
	root := c.rootOf(item)
	if c == nil || root == nil {
		return nil, errors.New("catalogue has no open root")
	}
	file, err := root.OpenFile(filepath.FromSlash(item.Path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("media file is unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("media file is unavailable")
	}
	return file, nil
}

// Close releases the folders' roots. Items can no longer be opened afterwards.
func (c *Catalog) Close() error {
	if c == nil {
		return nil
	}
	var errs []error
	for _, src := range c.sources {
		errs = append(errs, src.root.Close())
	}
	return errors.Join(errs...)
}

// CloseRemoved releases the roots of c's folders that next no longer has,
// once next is published in its place.
func (c *Catalog) CloseRemoved(next *Catalog) {
	if c == nil {
		return
	}
	for path, src := range c.sources {
		if next == nil || next.sources[path] == nil || next.sources[path].root != src.root {
			_ = src.root.Close()
		}
	}
}

// Stream is an audio, video, or subtitle track reported by FFprobe.
type Stream struct {
	Index    int
	Type     string
	Codec    string
	Language string `json:",omitempty"`
	Title    string `json:",omitempty"`
	// Profile, Level and PixelFormat drive a client's direct play decision.
	Profile           string  `json:",omitempty"`
	Level             float64 `json:",omitempty"`
	CodecTag          string  `json:",omitempty"`
	PixelFormat       string  `json:",omitempty"`
	TimeBase          string  `json:",omitempty"`
	Width             int     `json:",omitempty"`
	Height            int     `json:",omitempty"`
	Rotation          int     `json:",omitempty"` // counterclockwise for display, 0-359 degrees
	BitDepth          int     `json:",omitempty"`
	RefFrames         int     `json:",omitempty"`
	AverageFrameRate  float64 `json:",omitempty"`
	RealFrameRate     float64 `json:",omitempty"`
	Channels          int     `json:",omitempty"`
	ChannelLayout     string  `json:",omitempty"`
	SampleRate        int     `json:",omitempty"`
	BitRate           int64   `json:",omitempty"`
	IsInterlaced      bool
	IsDefault         bool
	IsForced          bool
	IsHearingImpaired bool
	// Path is set for an external subtitle file, relative to the media
	// directory. It is never sent to clients.
	Path string `json:",omitempty"`
}

func stableID(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

// Scan catalogs one directory as the Movies and TV Shows libraries
// DefaultLibraries returns, failing if the directory cannot be scanned.
func Scan(ctx context.Context, directory string) (*Catalog, error) {
	libraries, err := DefaultLibraries(directory)
	if err != nil {
		return nil, err
	}
	catalog, err := ScanLibraries(ctx, libraries)
	if err != nil {
		if catalog != nil {
			_ = catalog.Close()
		}
		return nil, err
	}
	return catalog, nil
}

// ScanLibraries probes the libraries' folders sequentially. No metadata is
// written to them. A folder that cannot be scanned is left out and named in
// the returned error together with a catalogue of the others; a global limit
// or a cancelled ctx returns no catalogue.
func ScanLibraries(ctx context.Context, libraries []Library) (*Catalog, error) {
	probe, stats, err := newProbe(ctx)
	if err != nil {
		return nil, err
	}
	base := &Catalog{probe: probe, probeStats: stats, sources: map[string]*source{}}
	attachExtractor(base)
	return base.Rescan(ctx, libraries)
}

// attachExtractor sets up embedded subtitle delivery. FFmpeg is optional:
// without it only sidecar subtitles are delivered. The extractor and its job
// limit are shared with rescanned catalogs.
func attachExtractor(catalog *Catalog) {
	if binary, err := exec.LookPath("ffmpeg"); err == nil {
		catalog.extractor = newSubtitleExtractor(binary)
	}
}

// scan catalogs directory with a given probe, for tests.
func scan(ctx context.Context, directory string, probe func(context.Context, *os.File) (Item, error)) (*Catalog, error) {
	libraries, err := DefaultLibraries(directory)
	if err != nil {
		return nil, err
	}
	catalog, err := (&Catalog{probe: probe, sources: map[string]*source{}}).Rescan(ctx, libraries)
	if err != nil {
		if catalog != nil {
			_ = catalog.Close()
		}
		return nil, err
	}
	return catalog, nil
}

// errLimit marks a global scan limit: the whole scan fails, not one folder.
var errLimit = errors.New("scan limit exceeded")

// budget counts what a scan has used across all folders.
type budget struct {
	entries, videos, tracks int
}

// Rescan walks the libraries' folders again and returns a new catalog; c is
// not changed. Files with the same path, size and mtime reuse c's probe
// results, and every known path keeps its ID and date added; files found in
// a folder c already had are dated now, those of a new folder by their mtime.
// A folder that cannot be walked, or that has no videos where c had some (an
// unmounted volume looks like that), keeps c's items and is named in the
// returned error alongside the new catalog. A global limit or a cancelled ctx
// returns no catalog. Catalogs share the roots of the folders they have in
// common: close removed ones with CloseRemoved, and only the last catalog.
func (c *Catalog) Rescan(ctx context.Context, libraries []Library) (*Catalog, error) {
	if c.probeStats != nil {
		*c.probeStats = ProbeStats{}
	}
	next := &Catalog{libraries: slices.Clone(libraries), sources: map[string]*source{}, probe: c.probe, probeStats: c.probeStats, extractor: c.extractor}
	used := &budget{}
	var failures []error
	opened := []*os.Root{}
	for _, path := range folderPaths(libraries) {
		previous := c.sources[path]
		video, audio := folderKinds(libraries, path)
		src, err := c.walkSource(ctx, path, previous, used, video, audio)
		if err != nil {
			if errors.Is(err, errLimit) || ctx.Err() != nil {
				for _, root := range opened {
					_ = root.Close()
				}
				return nil, err
			}
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			if previous == nil {
				continue
			}
			src = previous
		}
		if previous == nil || previous.root != src.root {
			opened = append(opened, src.root)
		}
		next.sources[path] = src
		next.Skipped += src.skipped
	}
	next.assemble()
	if c.probeStats != nil {
		next.Probes = *c.probeStats
	}
	return next, errors.Join(failures...)
}

// Reassign returns a catalog of libraries that reuses c's scanned folders
// without walking them; a folder c has not scanned stays empty until the next
// Rescan. It publishes a library change at once.
func (c *Catalog) Reassign(libraries []Library) *Catalog {
	next := &Catalog{libraries: slices.Clone(libraries), sources: map[string]*source{}, probe: c.probe, probeStats: c.probeStats, extractor: c.extractor, Probes: c.Probes}
	for _, path := range folderPaths(libraries) {
		if src := c.sources[path]; src != nil {
			next.sources[path] = src
			next.Skipped += src.skipped
		}
	}
	next.assemble()
	return next
}

// walkSource scans the folder at path into a new source, with its videos,
// its audio files or both. previous is the folder's last scan, or nil.
func (c *Catalog) walkSource(ctx context.Context, folder string, previous *source, used *budget, video, audio bool) (*source, error) {
	src := &source{path: folder, key: stableID("movies\x00" + folder), items: []Item{}, video: video, audio: audio}
	known := map[string]Item{}
	var now time.Time
	if previous != nil {
		src.root = previous.root
		for _, item := range previous.items {
			known[item.Path] = item
		}
		now = time.Now().UTC()
	} else {
		root, err := os.OpenRoot(folder)
		if err != nil {
			return nil, errors.New("cannot open media folder")
		}
		src.root = root
	}
	if err := c.walk(ctx, src, known, now, used); err != nil {
		if previous == nil {
			_ = src.root.Close()
		}
		return nil, err
	}
	if previous != nil && len(src.items) == 0 && len(previous.items) > 0 {
		return nil, errors.New("media folder has no media files")
	}
	return src, nil
}

// walk scans src's root into src. known holds the folder's previous items by
// path; files new to a folder scanned before are dated now, otherwise by
// their mtime.
func (c *Catalog) walk(ctx context.Context, src *source, known map[string]Item, now time.Time, used *budget) error {
	root, probe := src.root, c.probe
	subtitles := []string{}
	visit := func(path string, entry fs.DirEntry) error {
		ext := strings.ToLower(filepath.Ext(path))
		if sidecarCodec(ext) != "" {
			subtitles = append(subtitles, path)
			return nil
		}
		music := isAudio(ext)
		switch {
		case music && src.audio:
			if used.tracks >= maxTracks {
				return fmt.Errorf("libraries exceed the audio file limit: %w", errLimit)
			}
		case isVideo(ext) && src.video:
			if used.videos >= maxMovies {
				return fmt.Errorf("libraries exceed the video limit: %w", errLimit)
			}
		default:
			return nil
		}
		// Root confines symlink races; NONBLOCK prevents a swapped-in FIFO from hanging.
		file, err := root.OpenFile(filepath.FromSlash(path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			src.skipped++
			return nil
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			src.skipped++
			return nil
		}
		modified := info.ModTime().UTC()
		var item Item
		if previous, ok := known[path]; ok && previous.Size == info.Size() && previous.Modified.Equal(modified) {
			// Sidecars and episode fields are derived again below.
			item = Item{RunTimeTicks: previous.RunTimeTicks, Streams: embeddedStreams(previous.Streams), Tags: previous.Tags, Cover: previous.Cover}
		} else {
			item, err = probe(ctx, file)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// An audio file needs an audio track; a video file is refused
			// when its tracks are known and none is video.
			if err != nil || (music && !hasStream(item, "Audio")) || (!music && len(item.Streams) > 0 && !hasStream(item, "Video")) {
				src.skipped++
				return nil
			}
		}
		if !music {
			item.Tags, item.Cover = Tags{}, Image{}
		}
		item.ID = stableID(src.key + "\x00" + path)
		item.Path = path
		item.Name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		item.Size, item.Modified = info.Size(), modified
		switch previous, ok := known[path]; {
		case ok:
			item.Added = previous.Added
		case now.IsZero():
			item.Added = modified
		default:
			item.Added = now
		}
		item.Container = containerOf(ext)
		src.items = append(src.items, item)
		if music {
			used.tracks++
		} else {
			used.videos++
		}
		return nil
	}
	var walkDir func(string, int) error
	walkDir = func(relative string, depth int) error {
		if depth > 64 {
			return errors.New("media folder exceeds scan depth")
		}
		directory, err := root.OpenFile(filepath.FromSlash(relative), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
		if err != nil {
			return errors.New("cannot read media folder")
		}
		defer func() { _ = directory.Close() }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Avoid reading and sorting an arbitrarily large directory into memory.
			batch, readErr := directory.ReadDir(128)
			if readErr != nil && readErr != io.EOF {
				return errors.New("cannot read media folder entries")
			}
			for _, entry := range batch {
				if err := ctx.Err(); err != nil {
					return err
				}
				used.entries++
				if used.entries > maxEntries {
					return fmt.Errorf("libraries exceed the entry limit: %w", errLimit)
				}
				relativePath := path.Join(relative, entry.Name())
				if entry.IsDir() {
					if err := walkDir(relativePath, depth+1); err != nil {
						return err
					}
				} else if entry.Type().IsRegular() {
					if err := visit(relativePath, entry); err != nil {
						return err
					}
				}
			}
			if readErr == io.EOF {
				return nil
			}
		}
	}
	if err := walkDir(".", 0); err != nil {
		return err
	}
	slices.SortFunc(src.items, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
	attachSidecars(src.items, subtitles)
	src.classify()
	return nil
}

// isVideo reports whether a file extension, in lower case, is a video's.
func isVideo(ext string) bool {
	switch ext {
	case ".mp4", ".m4v", ".mkv", ".webm", ".avi", ".mov", ".mpg", ".mpeg", ".ts", ".m2ts", ".wmv", ".ogv":
		return true
	}
	return false
}

// isAudio reports whether a file extension, in lower case, is an audio file's.
func isAudio(ext string) bool {
	switch ext {
	case ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".oga", ".opus", ".wav", ".wma", ".aiff", ".aif":
		return true
	}
	return false
}

// containerOf names the container of a file extension, in lower case, as
// Emby clients match it in their direct play profiles.
func containerOf(ext string) string {
	switch ext {
	case ".oga":
		return "ogg"
	case ".aif":
		return "aiff"
	}
	return strings.TrimPrefix(ext, ".")
}

func hasStream(item Item, kind string) bool {
	return slices.ContainsFunc(item.Streams, func(s Stream) bool { return s.Type == kind })
}

// ReadSidecar reads a small file next to item's media, such as a .nfo,
// confined to item's library folder; name is relative to that folder. A
// missing file returns an error matching fs.ErrNotExist; a file larger than
// limit is not read.
func (c *Catalog) ReadSidecar(item Item, name string, limit int64) ([]byte, fs.FileInfo, error) {
	root := c.rootOf(item)
	if c == nil || root == nil {
		return nil, nil, errors.New("catalogue has no open root")
	}
	file, err := root.OpenFile(filepath.FromSlash(name), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fs.ErrNotExist
	}
	if info.Size() > limit {
		return nil, info, errors.New("sidecar file is too large")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit))
	return content, info, err
}
