package media

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	maxLyricsBytes = 1 << 20
	maxTagJobs     = 2
)

// lyricsKey matches the tags FFmpeg reads lyrics into: ID3 USLT as
// lyrics-<language> or lyrics-<description>-<language>, Vorbis LYRICS or
// UNSYNCEDLYRICS, MP4 ©lyr as lyrics. FFmpeg does not read ID3 SYLT, so
// timed lyrics come from LRC text only.
var lyricsKey = regexp.MustCompile(`(?is)^(?:(?:unsynced)?lyrics|lyrics-(?:.*-)?([a-z]{3}))$`)

// lyricsStreams returns a stream for each lyrics tag of an audio file,
// numbered after streams and in tag name order. A tag that holds LRC times
// is reported as LRC, any other as plain text.
func lyricsStreams(streams []Stream, tags ...map[string]string) []Stream {
	found := map[string]string{}
	for _, list := range tags {
		for key, value := range list {
			if lyricsKey.MatchString(key) && strings.TrimSpace(value) != "" {
				if _, seen := found[strings.ToLower(key)]; !seen {
					found[strings.ToLower(key)] = value
				}
			}
		}
	}
	index := 0
	for _, s := range streams {
		index = max(index, s.Index+1)
	}
	lyrics := []Stream{}
	for _, key := range slices.Sorted(maps.Keys(found)) {
		stream := Stream{Index: index, Type: "Subtitle", Codec: "txt", Lyrics: true, LyricsTag: key}
		if language := lyricsKey.FindStringSubmatch(key)[1]; language != "xxx" && language != "und" {
			stream.Language = language
		}
		if timedLyrics(found[key]) {
			stream.Codec = "lrc"
		}
		lyrics = append(lyrics, stream)
		index++
	}
	return lyrics
}

// tagReader runs FFprobe to read one tag of a file, at most maxTagJobs at
// once; further requests wait for a slot, without opening the file, until
// their own context ends.
type tagReader struct {
	binary string
	slots  chan struct{}
}

func newTagReader(binary string) *tagReader {
	return &tagReader{binary: binary, slots: make(chan struct{}, maxTagJobs)}
}

// read returns the tag named key of the file open returns, from the format
// or a stream. All tags are shown: a name such as an ID3 description may hold
// characters -show_entries does not take.
func (t *tagReader) read(ctx context.Context, open func() (*os.File, error), key string) (string, error) {
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	file, err := open()
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := runProbe(ctx, t.binary, nil, file, "-v", "error", "-protocol_whitelist", "fd", "-threads", "1",
		"-fd", "3", "-i", "fd:", "-of", "json", "-show_entries", "format_tags:stream_tags")
	if err != nil {
		return "", err
	}
	var result struct {
		Format  struct{ Tags map[string]string }
		Streams []struct{ Tags map[string]string }
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.New("invalid ffprobe output")
	}
	lists := []map[string]string{result.Format.Tags}
	for _, s := range result.Streams {
		lists = append(lists, s.Tags)
	}
	for _, list := range lists {
		for k, v := range list {
			if strings.EqualFold(k, key) {
				return v, nil
			}
		}
	}
	return "", os.ErrNotExist
}

// LyricsIndex returns the index of a track's first lyrics stream, or -1.
func LyricsIndex(item Item) int {
	for _, s := range item.Streams {
		if s.Lyrics {
			return s.Index
		}
	}
	return -1
}

// LyricsLine is one line of a track's lyrics, with its start and end in
// ticks. Lyrics without times have neither.
type LyricsLine struct {
	Text       string
	Start, End *int64
}

// OpenLyrics returns a track's lyrics stream as lines.
func (c *Catalog) OpenLyrics(ctx context.Context, item Item, index int) ([]LyricsLine, error) {
	i := slices.IndexFunc(item.Streams, func(s Stream) bool { return s.Index == index && s.Lyrics })
	if c == nil || i < 0 {
		return nil, os.ErrNotExist
	}
	stream := item.Streams[i]
	var text string
	if stream.Path != "" {
		data, _, err := c.ReadSidecar(item, stream.Path, maxLyricsBytes)
		if err != nil {
			return nil, errors.New("lyrics file is unavailable")
		}
		if text, err = decodeText(data); err != nil {
			return nil, errors.New("lyrics file is not UTF-8")
		}
	} else {
		if c.tags == nil {
			return nil, os.ErrNotExist
		}
		tag, err := c.tags.read(ctx, func() (*os.File, error) { return c.Open(item) }, stream.LyricsTag)
		if err != nil {
			return nil, errors.New("lyrics tag is unavailable")
		}
		if text, err = decodeText([]byte(tag)); err != nil {
			return nil, errors.New("lyrics tag is not UTF-8")
		}
	}
	return parseLyrics(text, item.RunTimeTicks), nil
}

var (
	lrcTime = regexp.MustCompile(`^\[(\d{1,3}):([0-5]?\d)(?:[.:](\d{1,3}))?\]`)
	// lrcTag matches the LRC header tags; other bracketed lines are text.
	lrcTag  = regexp.MustCompile(`(?i)^\[(ar|al|ti|au|by|re|ve|tool|length|offset|#):([^\]]*)\]$`)
	lrcWord = regexp.MustCompile(`<\d{1,3}:[0-5]?\d(?:[.:]\d{1,3})?>`)
)

// timedLyrics reports whether text has an LRC time on any line.
func timedLyrics(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if lrcTime.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

// parseLyrics reads LRC or plain text, as decodeText returns it. LRC lines
// may carry several times, the [offset:ms] tag and word times, which are
// dropped; other header tags and, in timed text, lines without a time are
// left out. A line ends where the next one starts, the
// last one at runtime. An empty timed line only ends the one before it. Text
// without any time is returned line by line without times.
func parseLyrics(text string, runtime int64) []LyricsLine {
	type timed struct {
		start int64
		text  string
	}
	var times []timed
	plain := []LyricsLine{}
	offset := int64(0)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		var starts []int64
		for {
			m := lrcTime.FindStringSubmatch(line)
			if m == nil {
				break
			}
			minutes, _ := strconv.ParseInt(m[1], 10, 64)
			seconds, _ := strconv.ParseInt(m[2], 10, 64)
			fraction, _ := strconv.ParseInt((m[3] + "000")[:3], 10, 64)
			starts = append(starts, ((minutes*60+seconds)*1000+fraction)*10000)
			line = line[len(m[0]):]
		}
		if len(starts) == 0 {
			if m := lrcTag.FindStringSubmatch(line); m != nil {
				if strings.EqualFold(m[1], "offset") {
					offset, _ = strconv.ParseInt(strings.TrimSpace(m[2]), 10, 64)
				}
			} else if line != "" {
				plain = append(plain, LyricsLine{Text: line})
			}
			continue
		}
		line = strings.TrimSpace(lrcWord.ReplaceAllString(line, ""))
		for _, start := range starts {
			times = append(times, timed{start: start, text: line})
		}
	}
	if len(times) == 0 {
		return plain
	}
	// A positive offset shows the lines earlier.
	for i := range times {
		times[i].start = max(0, times[i].start-offset*10000)
	}
	slices.SortStableFunc(times, func(a, b timed) int { return cmp.Compare(a.start, b.start) })
	lines := []LyricsLine{}
	for i, t := range times {
		if t.text == "" {
			continue
		}
		start, end := t.start, max(runtime, t.start)
		if i+1 < len(times) {
			end = times[i+1].start
		}
		lines = append(lines, LyricsLine{Text: t.text, Start: &start, End: &end})
	}
	return lines
}
