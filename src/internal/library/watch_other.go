//go:build !linux

package library

import (
	"context"
	"time"
)

// watch is not available without inotify: libraries are rescanned on
// request or every -rescan-interval.
func watch(context.Context, []string, time.Duration, func()) error {
	return errNoWatch
}
