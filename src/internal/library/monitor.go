package library

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"
)

// errNoWatch reports a system without folder change events.
var errNoWatch = errors.New("this system reports no folder changes")

// A change starts a scan once changes stop for settleQuiet, so a file being
// copied is probed when it is complete, or at the latest settleLongest after
// the first change, so a file written all the time does not hold scans off.
var (
	settleQuiet   = 10 * time.Second
	settleLongest = 5 * time.Minute
)

// retryMissing is how often a library folder that cannot be watched, such
// as an unmounted volume, is tried again.
var retryMissing = time.Minute

// monitor watches the folders of the libraries with real-time monitoring
// on, and stops watching those turned off or removed. It does nothing
// before Run. It holds the lock while it reads the libraries, so that of
// two changes at once the later one is watched.
func (m *Manager) monitor() {
	m.mu.Lock()
	defer m.mu.Unlock()
	folders := []string{}
	for _, l := range m.Libraries() {
		if l.Monitor {
			folders = append(folders, l.Paths...)
		}
	}
	slices.Sort(folders)
	folders = slices.Compact(folders)
	if m.run == nil || slices.Equal(folders, m.watched) {
		return
	}
	if m.unwatch != nil {
		m.unwatch()
		m.unwatch = nil
	}
	m.watched = folders
	if len(folders) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(m.run)
	m.unwatch = cancel
	changes := make(chan struct{}, 1)
	go settle(ctx, changes, settleQuiet, settleLongest, m.Scan)
	notify := func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	// A failed watch is started again; the scan after it finds what
	// changed while nothing was watched.
	retry := retryMissing
	go func() {
		for {
			err := watch(ctx, folders, retry, notify)
			switch {
			case err == nil:
				return
			case errors.Is(err, errNoWatch):
				slog.Info("Real-time monitoring is not available on this system; rescan on request or with -rescan-interval")
				return
			}
			slog.Warn("Library folders are not watched; trying again", "error", err, "retry", retry)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
			}
			notify()
		}
	}()
}

// settle calls fire once changes stop arriving for quiet, or once longest
// has passed since the first change it has not fired for, until ctx ends.
func settle(ctx context.Context, changes <-chan struct{}, quiet, longest time.Duration, fire func()) {
	var timer <-chan time.Time
	var first time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			now := time.Now()
			if first.IsZero() {
				first = now
			}
			timer = time.After(min(quiet, longest-now.Sub(first)))
		case <-timer:
			first, timer = time.Time{}, nil
			fire()
		}
	}
}
