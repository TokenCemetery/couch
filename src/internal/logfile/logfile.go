// Package logfile keeps the server log in the data directory: the current
// file and a few rotated ones, each bounded, so the logs page can show them.
package logfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits: a file is rotated before it exceeds maxBytes, and keep rotated
// files are kept besides the current one.
var (
	maxBytes int64 = 10 << 20
	keep           = 4
)

const current = "couch.log"

// Writer appends to the current log file and rotates it.
type Writer struct {
	mu   sync.Mutex
	dir  string
	file *os.File
	size int64
}

// Open opens or creates the log directory and its current file.
func Open(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	file, err := os.OpenFile(filepath.Join(w.dir, current), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file, w.size = file, info.Size()
	return nil
}

// Write appends p, rotating first when p would make the file exceed its
// limit. A log that cannot be written does not stop the server: the error is
// returned to the logger, which drops the line.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, errors.New("log file is closed")
	}
	if w.size > 0 && w.size+int64(len(p)) > maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate renames couch.log to couch-1.log, shifting older ones up and
// dropping the oldest. The caller holds mu.
func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	_ = os.Remove(filepath.Join(w.dir, rotated(keep)))
	for n := keep - 1; n >= 1; n-- {
		_ = os.Rename(filepath.Join(w.dir, rotated(n)), filepath.Join(w.dir, rotated(n+1)))
	}
	if err := os.Rename(filepath.Join(w.dir, current), filepath.Join(w.dir, rotated(1))); err != nil {
		return err
	}
	return w.open()
}

func rotated(n int) string { return "couch-" + strconv.Itoa(n) + ".log" }

// Close closes the current file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// File describes a log file.
type File struct {
	Name     string
	Size     int64
	Modified time.Time
}

// Files lists the log files in dir, newest first.
func Files(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := []File{}
	for _, entry := range entries {
		if !valid(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, File{Name: entry.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	slices.SortFunc(files, func(a, b File) int { return b.Modified.Compare(a.Modified) })
	return files, nil
}

// OpenFile opens the log file name in dir; other names are refused, so a
// request cannot read anything else.
func OpenFile(dir, name string) (*os.File, error) {
	if !valid(name) {
		return nil, os.ErrNotExist
	}
	return os.Open(filepath.Join(dir, name)) //nolint:gosec // name is couch.log or couch-N.log
}

// valid reports whether name is the current or a rotated log file.
func valid(name string) bool {
	if name == current {
		return true
	}
	n, found := strings.CutPrefix(name, "couch-")
	n, ok := strings.CutSuffix(n, ".log")
	if !found || !ok || n == "" {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
