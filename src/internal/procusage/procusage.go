// Package procusage reads the resource use of a finished child process.
package procusage

import (
	"os"
	"runtime"
	"syscall"
	"time"
)

// Of reports a finished process's CPU time (user and system) and peak
// resident memory in KiB.
func Of(state *os.ProcessState) (time.Duration, int64) {
	cpu := state.UserTime() + state.SystemTime()
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return cpu, 0
	}
	return cpu, peakKiB(usage)
}

// Self reports the CPU time and peak resident memory in KiB of this process
// (who is RUSAGE_SELF) or of its finished children (RUSAGE_CHILDREN): the
// children's CPU time is their sum, their peak that of the largest.
func Self(who int) (time.Duration, int64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(who, &usage); err != nil {
		return 0, 0, err
	}
	cpu := time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	return cpu, peakKiB(&usage), nil
}

// peakKiB converts Maxrss, which macOS reports in bytes, to KiB.
func peakKiB(usage *syscall.Rusage) int64 {
	if runtime.GOOS == "darwin" {
		return usage.Maxrss / 1024
	}
	return usage.Maxrss
}
