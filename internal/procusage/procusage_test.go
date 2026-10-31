package procusage

import (
	"os/exec"
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
