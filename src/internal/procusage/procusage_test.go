package procusage

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestUnits(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cpu, peak := Of(cmd.ProcessState)
	// A shell's peak RSS is some hundreds of KiB to a few MiB; bytes taken
	// for KiB would be over 100 000.
	if cpu < 0 || peak < 100 || peak > 100_000 {
		t.Fatalf("cpu %v, peak %d KiB", cpu, peak)
	}
}

func TestSelf(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cpu, peak, err := Self(syscall.RUSAGE_SELF)
	// A Go test binary holds at least a few MiB and less than 1 GiB.
	if err != nil || cpu <= 0 || peak < 1024 || peak > 1<<20 {
		t.Fatalf("self: cpu %v, peak %d KiB, %v", cpu, peak, err)
	}
	if _, peak, err := Self(syscall.RUSAGE_CHILDREN); err != nil || peak < 100 || peak > 100_000 {
		t.Fatalf("children: peak %d KiB, %v", peak, err)
	}
}
