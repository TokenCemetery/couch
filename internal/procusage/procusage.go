// Package procusage reads the resource use of a finished child process.
package procusage

import (
	"os"
	"runtime"
	"syscall"
	"time"
)

// Of reports a finished process's CPU time (user and system) and peak
// resident memory in KiB, which macOS reports in bytes.
func Of(state *os.ProcessState) (time.Duration, int64) {
	cpu := state.UserTime() + state.SystemTime()
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return cpu, 0
	}
	peak := usage.Maxrss
	if runtime.GOOS == "darwin" {
		peak /= 1024
	}
	return cpu, peak
}
