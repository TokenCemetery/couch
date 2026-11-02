package hls

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestAudioEncodesOnceAndKeepsRecentTracks encodes a WMA track to AAC in an
// M4A file, shares one encode between concurrent requests, reuses it, and
// keeps only the most recently used tracks.
func TestAudioEncodesOnceAndKeepsRecentTracks(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe required")
	}
	dir := t.TempDir()
	track := filepath.Join(dir, "track.wma")
	if out, err := exec.CommandContext(context.Background(), ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=duration=5", "-ac", "2", "-c:a", "wmav2", track).CombinedOutput(); err != nil { //nolint:gosec // test input
		t.Fatalf("%v %s", err, out)
	}
	m, err := NewManager(ffmpeg, ffprobe, filepath.Join(dir, "transcode"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	opens := 0
	var mu sync.Mutex
	source := AudioSource{ItemID: "track", Version: "1", Stream: 0, Channels: 2, Bitrate: 128000, Open: func() (*os.File, error) {
		mu.Lock()
		opens++
		mu.Unlock()
		return os.Open(track)
	}}
	var wg sync.WaitGroup
	files := make([]*os.File, 3)
	errs := make([]error, 3)
	for n := range files {
		wg.Go(func() { files[n], errs[n] = m.Audio(context.Background(), source) })
	}
	wg.Wait()
	for n, f := range files {
		if errs[n] != nil {
			t.Fatal(errs[n])
		}
		_ = f.Close()
	}
	if opens != 1 {
		t.Fatalf("encodes %d, want 1", opens)
	}
	f, err := m.Audio(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(context.Background(), ffprobe, "-v", "error", "-show_entries", "format=format_name,duration:stream=codec_name,channels", "-of", "default=nw=1", f.Name()).Output() //nolint:gosec // test output
	_ = f.Close()
	if err != nil || !strings.Contains(string(out), "codec_name=aac") || !strings.Contains(string(out), "channels=2") || !strings.Contains(string(out), "mp4") {
		t.Fatalf("encoded track: %v %s", err, out)
	}
	if opens != 1 {
		t.Fatalf("a ready track was encoded again")
	}
	for n := range maxAudioFiles + 2 {
		other := source
		other.Version = strconv.Itoa(n + 2)
		f, err := m.Audio(context.Background(), other)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	entries, err := os.ReadDir(filepath.Join(dir, "transcode", "audio"))
	if err != nil || len(entries) != maxAudioFiles {
		t.Fatalf("kept %d tracks, want %d (%v)", len(entries), maxAudioFiles, err)
	}
}
