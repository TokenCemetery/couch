// Package hls remuxes or transcodes a video into HLS with MPEG-TS segments.
// A remux copies the video, so its playlist is cut on the source's keyframes;
// a transcode forces keyframes on a fixed grid and is cut there. Either way
// the playlist matches the segments FFmpeg writes.
package hls

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SegmentSeconds is the minimum segment length of a remux; segments end on
// the first keyframe at least this far from their start.
const SegmentSeconds = 6

// EncodedSegmentSeconds is the segment length of a transcode, as Emby's: the
// first segment and a seek wait for this much video to be encoded (#99).
const EncodedSegmentSeconds = 3

const (
	// seekStep spaces the seeks that list keyframes. A seek lands on the
	// keyframe at or before its target, so seeking once per segment would
	// skip keyframes and make segments longer than SegmentSeconds.
	seekStep      = SegmentSeconds / 3
	keyframeLimit = 5 * time.Minute
	maxKeyframes  = 1 << 20
)

// Boundaries returns segment start times, relative to the file's start time,
// for keyframes in ascending order. The first segment starts at the first
// keyframe.
func Boundaries(keyframes []float64) []float64 {
	if len(keyframes) == 0 {
		return nil
	}
	bounds := []float64{keyframes[0]}
	for _, k := range keyframes[1:] {
		if k-bounds[len(bounds)-1] >= SegmentSeconds {
			bounds = append(bounds, k)
		}
	}
	return bounds
}

// Grid returns segment start times every EncodedSegmentSeconds from first,
// the video's first timestamp, to duration, both relative to the file's start
// time. The first segment starts at first, as FFmpeg counts segment times
// from the job's first packet. A last segment shorter than a second is merged
// into the one before, so the plan does not end past the video's last frame.
func Grid(first, duration float64) []float64 {
	bounds := []float64{first}
	for t := first + EncodedSegmentSeconds; t < duration-1; t += EncodedSegmentSeconds {
		bounds = append(bounds, t)
	}
	return bounds
}

// Playlist renders the VOD media playlist for bounds; duration is the length
// of the video in seconds.
func Playlist(bounds []float64, duration float64, segmentURL func(n int) string) string {
	durations := make([]float64, len(bounds))
	target := 0.0
	for n, start := range bounds {
		end := duration
		if n+1 < len(bounds) {
			end = bounds[n+1]
		}
		durations[n] = max(end-start, 0.001)
		target = max(target, durations[n])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", int(target+0.999))
	for n, d := range durations {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", d, segmentURL(n))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// keyframes lists keyframe times of one video stream, relative to the
// file's start time, and returns that start time. It reads them from the
// container's index where one agrees with FFprobe (indexed), and lists them
// with FFprobe otherwise. The result is cached by the Manager.
func keyframes(ctx context.Context, ffprobe string, file *os.File, stream int) ([]float64, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, keyframeLimit)
	defer cancel()
	start, duration, err := fileTimes(ctx, ffprobe, file)
	if err != nil {
		return nil, 0, errors.New("cannot list keyframes")
	}
	times := indexed(ctx, ffprobe, file, stream, start, duration)
	if times == nil {
		if times, err = listKeyframes(ctx, ffprobe, file, stream, start, duration); err != nil {
			return nil, 0, err
		}
	}
	// Seeks list keyframes in time order and packets come in decode order;
	// a stray one out of order or repeated would break the segment plan.
	result := times[:0]
	for _, t := range times {
		if t-start >= 0 && (len(result) == 0 || t-start > result[len(result)-1]) {
			result = append(result, t-start)
		}
	}
	return result, start, nil
}

// indexed reads the keyframes from the container's index (indexKeyframes)
// and keeps them only if they agree with FFprobe and are dense enough. The
// first must be the stream's first packet, and the last the keyframe a seek
// to the end lands on, so neither end of the index is missing; no two may be
// more than two segments apart, as a sparse index would lengthen segments.
func indexed(ctx context.Context, ffprobe string, file *os.File, stream int, start, duration float64) []float64 {
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	times, err := indexKeyframes(io.NewSectionReader(file, 0, info.Size()), stream)
	if err != nil || len(times) == 0 {
		return nil
	}
	slices.Sort(times)
	for i := 1; i < len(times); i++ {
		if times[i]-times[i-1] > 2*SegmentSeconds {
			return nil
		}
	}
	var ends []float64
	end := strconv.FormatFloat(start+duration, 'f', 3, 64)
	err = probe(ctx, ffprobe, file, []string{"-select_streams", strconv.Itoa(stream), "-show_entries", "packet=pts_time,flags", "-read_intervals", "%+#1," + end + "%+#1"}, func(fields []string) error {
		if t, key, ok := packet(fields); ok && key {
			ends = append(ends, t)
		}
		return nil
	})
	if err != nil || len(ends) != 2 || math.Abs(ends[0]-times[0]) > 0.001 || math.Abs(ends[1]-times[len(times)-1]) > 0.001 {
		return nil
	}
	return times
}

// listKeyframes lists keyframe times of one video stream with FFprobe. It
// seeks every seekStep seconds and keeps the keyframe each seek lands on, so
// it reads a few packets per seek instead of the whole file. Any subset of
// the keyframes gives a plan FFmpeg follows exactly. Containers whose seeks
// do not land on keyframes (MPEG-TS) fall back to reading every packet.
func listKeyframes(ctx context.Context, ffprobe string, file *os.File, stream int, start, duration float64) ([]float64, error) {
	selected := []string{"-select_streams", strconv.Itoa(stream), "-show_entries", "packet=pts_time,flags"}
	var times []float64
	if duration > 0 && duration/seekStep < maxKeyframes {
		intervals := make([]string, 0, int(duration/seekStep)+1)
		for t := 0.0; t < duration; t += seekStep {
			intervals = append(intervals, strconv.FormatFloat(start+t, 'f', 3, 64)+"%+#1")
		}
		seeked := true
		err := probe(ctx, ffprobe, file, append(selected, "-read_intervals", strings.Join(intervals, ",")), func(fields []string) error {
			t, key, ok := packet(fields)
			if ok && !key {
				seeked = false
			}
			if ok && key {
				times = append(times, t)
			}
			return nil
		})
		if err != nil {
			return nil, errors.New("cannot list keyframes")
		}
		if !seeked {
			times = nil
		}
	}
	if len(times) == 0 {
		err := probe(ctx, ffprobe, file, selected, func(fields []string) error {
			if t, key, ok := packet(fields); ok && key {
				if len(times) >= maxKeyframes {
					return errors.New("too many keyframes")
				}
				times = append(times, t)
			}
			return nil
		})
		if err != nil || len(times) == 0 {
			return nil, errors.New("cannot list keyframes")
		}
	}
	return times, nil
}

// firstFrame returns the time of the video stream's first packet, relative
// to the file's start time, and that start time. It reads the header and a
// few packets.
func firstFrame(ctx context.Context, ffprobe string, file *os.File, stream int) (first, start, duration float64, err error) {
	start, duration, err = fileTimes(ctx, ffprobe, file)
	if err != nil {
		return 0, 0, 0, err
	}
	found := false
	err = probe(ctx, ffprobe, file, []string{"-select_streams", strconv.Itoa(stream), "-show_entries", "packet=pts_time,flags", "-read_intervals", "%+#1"}, func(fields []string) error {
		if t, _, ok := packet(fields); ok && !found {
			first, found = max(t-start, 0), true
		}
		return nil
	})
	if err == nil && !found {
		err = errors.New("video stream has no packets")
	}
	return first, start, duration, err
}

// fileTimes returns the file's start time and duration in seconds from its
// header, without reading packets.
func fileTimes(ctx context.Context, ffprobe string, file *os.File) (start, duration float64, err error) {
	err = probe(ctx, ffprobe, file, []string{"-show_entries", "format=start_time,duration"}, func(fields []string) error {
		if len(fields) >= 3 && fields[0] == "format" {
			start, _ = strconv.ParseFloat(fields[1], 64)
			duration, _ = strconv.ParseFloat(fields[2], 64)
		}
		return nil
	})
	return start, duration, err
}

// packet parses a CSV packet line with pts_time and flags.
func packet(fields []string) (t float64, key, ok bool) {
	if len(fields) < 3 || fields[0] != "packet" {
		return 0, false, false
	}
	t, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, false, false // N/A: the packet has no timestamp
	}
	return t, strings.HasPrefix(fields[2], "K"), true
}

// probe runs FFprobe on file from its start with CSV output and passes each
// line's fields to line; an error from line stops FFprobe.
func probe(ctx context.Context, ffprobe string, file *os.File, args []string, line func(fields []string) error) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// +genpts fills in missing PTS the way FFmpeg does for the job (#96).
	// The fd protocol reads in unbounded blocks by default, so each seek in
	// an MP4 header pulls in megabytes (#98).
	args = append([]string{"-v", "error", "-protocol_whitelist", "fd", "-fflags", "+genpts", "-blocksize", "65536", "-fd", "3"}, args...)
	cmd := exec.CommandContext(ctx, ffprobe, append(args, "-of", "csv", "fd:")...) //nolint:gosec // binary is ffprobe from PATH; arguments are numbers and fixed options
	cmd.Env = []string{"LC_ALL=C"}
	cmd.ExtraFiles = []*os.File{file}
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var lineErr error
	lines := bufio.NewScanner(out)
	for lineErr == nil && lines.Scan() {
		lineErr = line(strings.Split(lines.Text(), ","))
	}
	if lineErr == nil {
		lineErr = lines.Err()
	}
	if lineErr != nil {
		cancel()
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return err
	}
	return lineErr
}
