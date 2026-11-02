package hls

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/TokenCemetery/couch/internal/procusage"
)

const (
	// maxAudioJobs caps concurrent audio encodes; more wait up to
	// audioWait for a slot.
	maxAudioJobs = 2
	audioWait    = 10 * time.Second
	// audioTimeout bounds one encode of a whole track.
	audioTimeout = 2 * time.Minute
	// maxAudioFiles encoded tracks are kept for seeking and replays; the
	// oldest goes first.
	maxAudioFiles = 32
)

// AudioSource is a track to encode for a client that cannot play its file.
type AudioSource struct {
	ItemID string
	// Version changes when the file changes.
	Version string
	Open    func() (*os.File, error)
	// Stream is the audio stream to encode; Bitrate the AAC bit rate and
	// Channels the channels, at most 2.
	Stream, Channels int
	Bitrate          int64
}

func (s AudioSource) key() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d\x00%d", s.ItemID, s.Version, s.Stream, s.Channels, s.Bitrate))
	return hex.EncodeToString(sum[:16])
}

// audioJobs holds the encodes in progress by key, so concurrent requests
// for one track share an encode, and a slot per running one.
type audioJobs struct {
	running map[string]chan struct{}
	slots   chan struct{}
}

// Audio returns the track encoded to AAC in an M4A file, encoding the whole
// track first unless an earlier request did. The file is seekable, so it is
// served with ranges like the original. The caller closes it.
func (m *Manager) Audio(ctx context.Context, source AudioSource) (*os.File, error) {
	dir := filepath.Join(m.dir, "audio")
	file := filepath.Join(dir, source.key()+".m4a")
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrBusy
		}
		if m.audio.running == nil {
			m.audio = audioJobs{running: map[string]chan struct{}{}, slots: make(chan struct{}, maxAudioJobs)}
		}
		if done, busy := m.audio.running[file]; busy {
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if f, err := os.Open(file); err == nil { //nolint:gosec // file is named by a hash under the transcode directory
			now := time.Now()
			_ = os.Chtimes(file, now, now)
			m.mu.Unlock()
			return f, nil
		}
		done := make(chan struct{})
		m.audio.running[file] = done
		slots := m.audio.slots
		m.mu.Unlock()
		err := m.encodeAudio(ctx, slots, dir, file, source)
		m.mu.Lock()
		delete(m.audio.running, file)
		m.mu.Unlock()
		close(done)
		if err != nil {
			return nil, err
		}
	}
}

func (m *Manager) encodeAudio(ctx context.Context, slots chan struct{}, dir, file string, source AudioSource) error {
	wait, stop := context.WithTimeout(ctx, audioWait)
	defer stop()
	select {
	case slots <- struct{}{}:
	case <-wait.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBusy
	}
	defer func() { <-slots }()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	in, err := source.Open()
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	partial := file + ".part"
	defer func() { _ = os.Remove(partial) }()
	// The encode is not tied to the request: a client that gives up during
	// it finds the file ready when it asks again.
	run, cancel := context.WithTimeout(m.scanCtx, audioTimeout)
	defer cancel()
	args := []string{"-v", "error", "-nostdin", "-protocol_whitelist", "fd", "-threads", "1", "-fd", "3", "-i", "fd:",
		"-map", "0:" + strconv.Itoa(source.Stream), "-c:a", "aac", "-b:a", strconv.FormatInt(source.Bitrate, 10),
		"-ac", strconv.Itoa(source.Channels), "-map_metadata", "-1", "-movflags", "+faststart", "-f", "ipod", "-y", partial}
	cmd := exec.CommandContext(run, m.ffmpeg, args...) //nolint:gosec // ffmpeg from PATH; arguments are built here
	cmd.Env = []string{"LC_ALL=C"}
	cmd.ExtraFiles = []*os.File{in}
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if cmd.ProcessState != nil {
		cpu, peak := procusage.Of(cmd.ProcessState)
		slog.Info("Audio encode ended", "item", source.ItemID, "audio", "aac", "cpu", cpu.Round(time.Millisecond), "peak_rss_kib", peak)
	}
	if err != nil {
		slog.Warn("Audio encode failed", "item", source.ItemID, "error", err)
		return ErrSegment
	}
	if err := syncFile(partial); err != nil {
		return err
	}
	if err := os.Rename(partial, file); err != nil {
		return err
	}
	pruneAudio(dir)
	return nil
}

// syncFile flushes FFmpeg's output before it is renamed into the cache, so a
// power loss cannot leave an empty track under the final name.
func syncFile(name string) error {
	f, err := os.Open(name) //nolint:gosec // name is under the transcode directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// pruneAudio removes the least recently used encoded tracks beyond
// maxAudioFiles. A removed file stays readable by a request that has it open.
func pruneAudio(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type encoded struct {
		name string
		used time.Time
	}
	files := []encoded{}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && filepath.Ext(entry.Name()) == ".m4a" {
			files = append(files, encoded{entry.Name(), info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b encoded) int { return b.used.Compare(a.used) })
	for _, f := range files[min(maxAudioFiles, len(files)):] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}
