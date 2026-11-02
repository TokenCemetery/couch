// Package library keeps the media catalogue in step with the stored
// libraries: it creates them from -media-dir on the first start, checks the
// folders an administrator adds against the media roots, scans, publishes
// each new catalogue and saves its index.
package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// scanTimeout bounds one scan of all libraries.
const scanTimeout = 10 * time.Minute

// Errors returned for folders an administrator chooses.
var (
	ErrNoRoots     = errors.New("no media roots are configured")
	ErrPath        = errors.New("the folder must be an absolute path without . or .. components")
	ErrOutsideRoot = errors.New("the folder is not inside a media root")
	ErrNotFolder   = errors.New("the folder does not exist or is not a directory")
	ErrOverlap     = errors.New("the folder overlaps a folder of another library of the same type")
	ErrType        = errors.New("only movies, TV shows and music libraries are supported")
)

// Result is the outcome of the last scan.
type Result struct {
	Start, End time.Time
	// Err is empty when every folder was scanned.
	Err string
}

// Manager owns the catalogue and its scans. One scan runs at a time;
// requests during a scan start one more after it.
type Manager struct {
	store   *state.Store
	roots   []string
	index   string
	publish func(*media.Catalog)

	mu       sync.Mutex
	interval time.Duration
	catalog  *media.Catalog
	running  bool
	last     *Result
	// changed is called when a scan starts or ends, to report the task.
	changed  func()
	requests chan struct{}
}

// New returns a manager for the libraries in store. roots are the folders
// below which an administrator may add library folders; index is the saved
// catalogue file; publish receives every new catalogue.
func New(store *state.Store, roots []string, index string, publish func(*media.Catalog)) (*Manager, error) {
	clean := []string{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		clean = append(clean, absolute)
	}
	return &Manager{store: store, roots: clean, index: index, publish: publish, requests: make(chan struct{}, 1)}, nil
}

// Roots returns the media roots.
func (m *Manager) Roots() []string { return slices.Clone(m.roots) }

// OnChange sets the function called when a scan starts or ends.
func (m *Manager) OnChange(changed func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.changed = changed
}

// Libraries returns the stored libraries.
func (m *Manager) Libraries() []media.Library {
	return toMedia(m.store.Snapshot().Libraries)
}

// Catalog returns the published catalogue, which is nil before any scan.
func (m *Manager) Catalog() *media.Catalog {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.catalog
}

// Open builds the first catalogue before Couch serves. On the first start
// with -media-dir, it stores that directory's Movies and TV Shows libraries.
// A saved index serves at once, and loaded reports that it was used, so a
// rescan should follow. Without an index the libraries are scanned; an error
// is returned only when no catalogue can be built.
func (m *Manager) Open(ctx context.Context, mediaDir string) (loaded bool, err error) {
	if d := m.store.Snapshot(); !d.LibrariesConfigured && mediaDir != "" {
		libraries, err := media.DefaultLibraries(mediaDir)
		if err != nil {
			return false, err
		}
		if err := m.store.SetLibraries(toState(libraries)); err != nil {
			return false, err
		}
		slog.Info("Libraries created from -media-dir", "libraries", len(libraries))
	}
	libraries := m.Libraries()
	if len(libraries) == 0 {
		return false, nil
	}
	catalog, err := media.Load(ctx, libraries, m.index)
	switch {
	case err == nil:
		videos, tracks := counts(catalog)
		slog.Info("Media index loaded; rescanning in the background", "videos", videos, "tracks", tracks, "folders", len(catalog.Folders))
		m.set(catalog)
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
	default:
		slog.Warn("Media index not used; scanning", "error", err)
	}
	slog.Info("Scanning media libraries")
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	start := time.Now().UTC()
	catalog, err = media.ScanLibraries(scanCtx, libraries)
	if catalog == nil {
		return false, err
	}
	m.finish(start, err)
	logScan("Media scan complete", catalog, err)
	m.set(catalog)
	m.save(catalog)
	return false, nil
}

// Run rescans on a request, on hangup, every interval (0 disables) and, with
// now set, once at the start, until ctx ends.
func (m *Manager) Run(ctx context.Context, hangup <-chan os.Signal, interval time.Duration, now bool) {
	m.mu.Lock()
	m.interval = interval
	m.mu.Unlock()
	var tick <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		if !now {
			select {
			case <-ctx.Done():
				return
			case <-hangup:
			case <-tick:
			case <-m.requests:
			}
		}
		now = false
		m.rescan(ctx)
	}
}

// Scan asks Run for a rescan; it returns at once.
func (m *Manager) Scan() {
	select {
	case m.requests <- struct{}{}:
	default:
	}
}

// Interval returns the periodic rescan interval, 0 when there is none.
func (m *Manager) Interval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interval
}

// Status reports whether a scan runs and how the last one ended.
func (m *Manager) Status() (running bool, last *Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running, m.last
}

func (m *Manager) rescan(ctx context.Context) {
	libraries := m.Libraries()
	start := time.Now().UTC()
	m.setRunning(true)
	slog.Info("Rescanning media libraries")
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
	var next *media.Catalog
	var err error
	if current := m.Catalog(); current != nil {
		next, err = current.Rescan(scanCtx, libraries)
	} else if len(libraries) > 0 {
		next, err = media.ScanLibraries(scanCtx, libraries)
	}
	cancel()
	m.finish(start, err)
	if next == nil {
		if err != nil {
			slog.Warn("Media rescan failed; keeping the previous catalog", "error", err)
		}
		return
	}
	logScan("Media rescan complete", next, err)
	// Libraries changed during the scan are published at once; the scan
	// they requested fills their new folders.
	if current := m.Libraries(); !reflect.DeepEqual(current, libraries) {
		next = next.Reassign(current)
	}
	m.set(next)
	m.save(next)
}

// SetLibraries stores the libraries, publishes them at once with what is
// already scanned, and asks for a rescan that fills new folders.
func (m *Manager) SetLibraries(libraries []media.Library) error {
	if err := m.store.SetLibraries(toState(libraries)); err != nil {
		return err
	}
	if current := m.Catalog(); current != nil {
		m.set(current.Reassign(libraries))
	}
	m.Scan()
	return nil
}

// Folder checks a folder an administrator chose for library and returns
// it cleaned: an existing directory inside a media root, also once symbolic
// links are resolved, that no library of the same type already covers.
func (m *Manager) Folder(libraries []media.Library, library media.Library, path string) (string, error) {
	if len(m.roots) == 0 {
		return "", ErrNoRoots
	}
	if library.Type != media.Movies && library.Type != media.TVShows && library.Type != media.Music {
		return "", ErrType
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrPath
	}
	if _, err := m.Inside(path); err != nil {
		return "", err
	}
	if media.Overlap(libraries, library, path) {
		return "", ErrOverlap
	}
	return path, nil
}

// Inside checks that path, a clean absolute path, is an existing directory
// inside a media root, also once symbolic links are resolved, and returns
// that root.
func (m *Manager) Inside(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrNotFolder
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", ErrNotFolder
	}
	for _, root := range m.roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if media.Inside(path, root) && media.Inside(resolved, realRoot) {
			return root, nil
		}
	}
	return "", ErrOutsideRoot
}

// Close releases the published catalogue.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.catalog.Close()
}

func (m *Manager) set(next *media.Catalog) {
	m.mu.Lock()
	previous := m.catalog
	m.catalog = next
	m.mu.Unlock()
	m.publish(next)
	previous.CloseRemoved(next)
}

func (m *Manager) setRunning(running bool) {
	m.mu.Lock()
	m.running = running
	changed := m.changed
	m.mu.Unlock()
	if changed != nil {
		changed()
	}
}

func (m *Manager) finish(start time.Time, err error) {
	result := &Result{Start: start, End: time.Now().UTC()}
	if err != nil {
		result.Err = err.Error()
	}
	m.mu.Lock()
	m.last = result
	m.mu.Unlock()
	m.setRunning(false)
}

// save writes the media index; a failure costs only a full scan at the next
// start.
func (m *Manager) save(catalog *media.Catalog) {
	if err := catalog.Save(m.index); err != nil {
		slog.Warn("Media index not saved", "error", err)
	}
}

func logScan(message string, catalog *media.Catalog, err error) {
	videos, tracks := counts(catalog)
	attrs := []any{"videos", videos, "tracks", tracks, "folders", len(catalog.Folders), "skipped", catalog.Skipped,
		"probes", catalog.Probes.Runs, "probe_cpu", catalog.Probes.CPU.Round(time.Millisecond), "probe_peak_rss_kib", catalog.Probes.PeakKiB}
	if err != nil {
		attrs = append(attrs, "failed_folders", fmt.Sprint(err))
	}
	slog.Info(message, attrs...)
}

// counts returns how many videos and audio tracks the catalogue holds.
func counts(catalog *media.Catalog) (videos, tracks int) {
	for _, item := range catalog.Items {
		if item.Type() == "Audio" {
			tracks++
		} else {
			videos++
		}
	}
	return videos, tracks
}

func toMedia(libraries []state.Library) []media.Library {
	out := make([]media.Library, len(libraries))
	for n, l := range libraries {
		out[n] = media.Library{ID: l.ID, Name: l.Name, Type: l.Type, Paths: slices.Clone(l.Paths)}
	}
	return out
}

func toState(libraries []media.Library) []state.Library {
	out := make([]state.Library, len(libraries))
	for n, l := range libraries {
		out[n] = state.Library{ID: l.ID, Name: l.Name, Type: l.Type, Paths: slices.Clone(l.Paths)}
	}
	return out
}
