package library

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// watchMask selects the events that change what a scan finds: files and
// folders created, written, deleted or moved. Opening and reading, which
// the scan itself does, are not watched, nor are attribute changes, which
// reading can cause through access times.
const watchMask = unix.IN_CREATE | unix.IN_CLOSE_WRITE | unix.IN_MODIFY | unix.IN_DELETE | unix.IN_DELETE_SELF |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_MOVE_SELF | unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

// maxWatchDepth bounds the folders watched below a library folder, as the
// scan bounds those it walks.
const maxWatchDepth = 64

var errWatchesFull = errors.New("too many folders to watch: raise fs.inotify.max_user_watches")

// watch calls changed for every change below folders until ctx ends. It
// watches each folder, followed if it is a symbolic link as the scan does,
// and its subfolders down to the scan depth, without following links, and
// adds folders as they appear. A folder that cannot be watched, wholly or
// for lack of watches, is tried again every retry. Events only start
// a scan, which reads the folders by its own rules.
func watch(ctx context.Context, folders []string, retry time.Duration, changed func()) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "inotify")
	defer func() { _ = file.Close() }()
	w := &watcher{fd: fd, paths: map[int32]string{}, roots: map[string]string{}, missing: map[string]bool{}}
	for _, folder := range folders {
		w.missing[folder] = true
	}
	w.retry()
	slog.Info("Watching library folders", "folders", len(folders), "directories", len(w.paths), "unavailable", len(w.missing))
	// One buffer is read into and handled in turn.
	buffer := make([]byte, 64<<10)
	ready := make(chan int)
	consumed := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		for {
			n, err := file.Read(buffer)
			if err != nil {
				failed <- err
				return
			}
			select {
			case ready <- n:
			case <-ctx.Done():
				return
			}
			select {
			case <-consumed:
			case <-ctx.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(retry)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			return err
		case <-ticker.C:
			if w.retry() {
				changed()
			}
		case n := <-ready:
			w.read(buffer[:n])
			changed()
			select {
			case consumed <- struct{}{}:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

type watcher struct {
	fd int
	// paths holds the watched folders by watch descriptor, with library
	// folders' links resolved.
	paths map[int32]string
	// roots maps each watched library folder to its resolved path.
	roots map[string]string
	// missing holds the library folders not wholly watched now.
	missing map[string]bool
	full    bool
}

// retry watches the library folders in missing and reports whether one
// became watched.
func (w *watcher) retry() bool {
	added := false
	for folder := range w.missing {
		resolved, err := filepath.EvalSymlinks(folder)
		if err != nil {
			continue
		}
		if err := w.add(resolved, 0); err != nil {
			w.fill(folder)
			continue
		}
		if w.watched(resolved) {
			delete(w.missing, folder)
			w.roots[folder] = resolved
			added = true
		}
	}
	if len(w.missing) == 0 {
		w.full = false
	}
	return added
}

// fill leaves folder for a retry when the watches ran out, and says so once.
func (w *watcher) fill(folder string) {
	if !w.full {
		slog.Warn("Some library folders are not watched", "error", errWatchesFull)
	}
	w.full = true
	w.missing[folder] = true
}

// add watches dir, depth folders below a library folder, and its
// subfolders down to the scan depth.
func (w *watcher) add(dir string, depth int) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			// A folder that cannot be read stays unwatched.
			return fs.SkipDir
		case !entry.IsDir():
			return nil
		}
		if level, _ := below(path, dir); depth+level > maxWatchDepth {
			return fs.SkipDir
		}
		wd, err := unix.InotifyAddWatch(w.fd, path, watchMask)
		switch {
		case errors.Is(err, unix.ENOSPC):
			return errWatchesFull
		case err != nil:
			return fs.SkipDir
		}
		w.paths[int32(wd)] = path //nolint:gosec // watch descriptors are int32 in inotify events
		return nil
	})
}

func (w *watcher) watched(path string) bool {
	for _, p := range w.paths {
		if p == path {
			return true
		}
	}
	return false
}

// below reports whether path is root or inside it, and how many folders
// below root it is.
func below(path, root string) (int, bool) {
	rel, err := filepath.Rel(root, path)
	switch {
	case err != nil || rel == ".." || strings.HasPrefix(rel, "../"):
		return 0, false
	case rel == ".":
		return 0, true
	}
	return strings.Count(rel, "/") + 1, true
}

// root returns the library folder whose resolved path is path.
func (w *watcher) root(path string) (string, bool) {
	for folder, resolved := range w.roots {
		if resolved == path {
			return folder, true
		}
	}
	return "", false
}

// read handles a buffer of inotify events: it watches the folders created
// or moved in, forgets removed watches, gives up a library folder that was
// moved away so that its path is watched again, and, when the queue
// overflowed, watches every folder again.
func (w *watcher) read(buffer []byte) {
	for len(buffer) >= unix.SizeofInotifyEvent {
		wd := int32(binary.NativeEndian.Uint32(buffer[0:])) //nolint:gosec // inotify_event.wd is an int
		mask := binary.NativeEndian.Uint32(buffer[4:])
		end := unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(buffer[12:]))
		if end > len(buffer) {
			return
		}
		name := strings.TrimRight(string(buffer[unix.SizeofInotifyEvent:end]), "\x00")
		buffer = buffer[end:]
		switch {
		case mask&unix.IN_Q_OVERFLOW != 0:
			for folder := range w.roots {
				w.missing[folder] = true
			}
			w.retry()
		case mask&unix.IN_IGNORED != 0:
			path := w.paths[wd]
			delete(w.paths, wd)
			if folder, ok := w.root(path); ok {
				delete(w.roots, folder)
				w.missing[folder] = true
			}
		case mask&unix.IN_MOVE_SELF != 0:
			// The watches follow a moved library folder; dropping them ends
			// in IN_IGNORED, and the retry watches what is at its path then.
			moved := w.paths[wd]
			if _, ok := w.root(moved); ok {
				for other, path := range w.paths {
					if _, inside := below(path, moved); inside {
						_, _ = unix.InotifyRmWatch(w.fd, uint32(other)) //nolint:gosec // a descriptor from InotifyAddWatch
					}
				}
			}
		case mask&unix.IN_ISDIR != 0 && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0:
			if parent, ok := w.paths[wd]; ok {
				w.addBelow(filepath.Join(parent, name))
			}
		}
	}
}

// addBelow watches a folder that appeared inside a library folder, within
// the scan depth.
func (w *watcher) addBelow(path string) {
	for folder, resolved := range w.roots {
		if depth, ok := below(path, resolved); ok {
			if err := w.add(path, depth); err != nil {
				w.fill(folder)
			}
			return
		}
	}
}
