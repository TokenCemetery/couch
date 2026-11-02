package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/procusage"
)

const maxProbeOutput = 1 << 20

type probeOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (b *probeOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > maxProbeOutput {
		b.cancel()
		return 0, errors.New("probe output limit exceeded")
	}
	return b.buffer.Write(p)
}

// ProbeStats sums the FFprobe runs of one scan.
type ProbeStats struct {
	Runs    int
	CPU     time.Duration // user and system
	PeakKiB int64         // the largest peak resident memory of a run
}

func (p *ProbeStats) add(state *os.ProcessState) {
	cpu, peak := procusage.Of(state)
	p.Runs++
	p.CPU += cpu
	p.PeakKiB = max(p.PeakKiB, peak)
}

// runProbe runs FFprobe and, when stats is set, adds the run's resource use.
func runProbe(ctx context.Context, binary string, stats *ProbeStats, file *os.File, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // binary is ffprobe from PATH; args are fixed by the callers
	// Do not inherit FFREPORT, credentials, or other application environment variables.
	cmd.Env = []string{"LC_ALL=C"}
	if file != nil {
		cmd.ExtraFiles = []*os.File{file}
	}
	output := &probeOutput{cancel: cancel}
	cmd.Stdout = output
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stats != nil && cmd.ProcessState != nil {
		stats.add(cmd.ProcessState)
	}
	if err != nil {
		return nil, errors.New("ffprobe failed or exceeded its limits")
	}
	return output.buffer.Bytes(), nil
}

// newProbe returns the file probe and the stats its runs add to.
func newProbe(ctx context.Context) (func(context.Context, *os.File) (Item, error), *ProbeStats, error) {
	binary, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil, nil, errors.New("-media-dir requires ffprobe on PATH")
	}
	protocols, err := runProbe(ctx, binary, nil, nil, "-v", "error", "-protocols")
	if err != nil || !strings.Contains(string(protocols), "\n  fd\n") {
		return nil, nil, errors.New("ffprobe must support the fd input protocol")
	}
	stats := &ProbeStats{}
	return func(ctx context.Context, file *os.File) (Item, error) {
		data, err := runProbe(ctx, binary, stats, file, "-v", "error", "-max_alloc", "67108864",
			"-protocol_whitelist", "fd", "-threads", "1", "-probesize", "5000000", "-analyzeduration", "5000000",
			// The fd protocol reads in unbounded blocks by default, so each
			// seek in an MP4 header pulls in megabytes; 64 KiB reads cut
			// the scan's reads to what FFprobe reads by path.
			"-max_streams", "64", "-blocksize", "65536", "-fd", "3", "-i", "fd:", "-of", "json",
			// Clients decide direct play from codec profile, level and pixel
			// format, so those are probed alongside the basic descriptors.
			// Audio files keep their tags in the format, or in the stream
			// for Ogg and Opus.
			"-show_entries", "format=duration,bit_rate:format_tags="+audioTags+":stream=index,codec_type,codec_name,codec_tag_string,profile,level,width,height,channels,channel_layout,sample_rate,bit_rate,pix_fmt,bits_per_raw_sample,bits_per_sample,refs,avg_frame_rate,r_frame_rate,time_base,field_order:stream_tags=language,"+audioTags+":stream_disposition=default,forced,attached_pic,hearing_impaired:stream_side_data=rotation")
		if err != nil {
			return Item{}, err
		}
		return parseProbe(data)
	}, stats, nil
}

// audioTags are the tags read from audio files; FFprobe matches them
// ignoring case.
const audioTags = "title,artist,album_artist,album,genre,track,disc,date"

// parseTags reads an audio file's tags, whose keys may be in any case.
// Artists are separated by ";" and genres by ";" or "/".
func parseTags(raw map[string]string) Tags {
	get := func(key string) string {
		for k, v := range raw {
			if strings.EqualFold(k, key) {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	number := func(text string) int {
		text, _, _ = strings.Cut(text, "/")
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 0 || n > 9999 {
			return 0
		}
		return n
	}
	split := func(text, separators string) []string {
		values := []string{}
		for _, v := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune(separators, r) }) {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(values, v) {
				values = append(values, v)
			}
		}
		if len(values) == 0 {
			return nil
		}
		return values
	}
	tags := Tags{Title: get("title"), Album: get("album"), AlbumArtist: get("album_artist"),
		Artists: split(get("artist"), ";"), Genres: split(get("genre"), ";/"),
		Track: number(get("track")), Disc: number(get("disc"))}
	if date := get("date"); len(date) >= 4 {
		tags.Year = number(date[:4])
	}
	return tags
}

// coverMIME names the image type of an attached picture's codec; only JPEG
// and PNG covers are served.
func coverMIME(codec string) string {
	switch codec {
	case "mjpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	}
	return ""
}

// profileName normalises ffprobe's profile field, which is a string for most
// codecs but a numeric identifier for a few.
func profileName(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// frameRate converts ffprobe's "num/den" rational to frames per second.
func frameRate(value string) float64 {
	numerator, denominator, ok := strings.Cut(value, "/")
	if !ok {
		return 0
	}
	n, err := strconv.ParseFloat(numerator, 64)
	d, err2 := strconv.ParseFloat(denominator, 64)
	if err != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

func parseProbe(data []byte) (Item, error) {
	var result struct {
		Format struct {
			Duration string
			BitRate  string `json:"bit_rate"`
			Tags     map[string]string
		}
		Streams []struct {
			Index         int
			CodecType     string  `json:"codec_type"`
			CodecName     string  `json:"codec_name"`
			CodecTag      string  `json:"codec_tag_string"`
			Profile       any     `json:"profile"`
			Level         float64 `json:"level"`
			Width         int
			Height        int
			Channels      int
			ChannelLayout string `json:"channel_layout"`
			SampleRate    string `json:"sample_rate"`
			BitRate       string `json:"bit_rate"`
			PixelFormat   string `json:"pix_fmt"`
			BitDepth      string `json:"bits_per_raw_sample"`
			SampleBits    int    `json:"bits_per_sample"`
			RefFrames     int    `json:"refs"`
			AvgFrameRate  string `json:"avg_frame_rate"`
			RFrameRate    string `json:"r_frame_rate"`
			TimeBase      string `json:"time_base"`
			FieldOrder    string `json:"field_order"`
			Tags          map[string]string
			SideData      []struct{ Rotation float64 } `json:"side_data_list"`
			Disposition   struct {
				Default         int
				Forced          int
				AttachedPic     int `json:"attached_pic"`
				HearingImpaired int `json:"hearing_impaired"`
			}
		}
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return Item{}, errors.New("invalid ffprobe output")
	}
	item := Item{Streams: []Stream{}}
	if seconds, err := strconv.ParseFloat(result.Format.Duration, 64); err == nil && seconds > 0 && seconds < float64(math.MaxInt64)/10000000 {
		item.RunTimeTicks = int64(seconds * 10000000)
	}
	item.Tags = parseTags(result.Format.Tags)
	for _, raw := range result.Streams {
		tags := parseTags(raw.Tags)
		if raw.CodecType == "audio" && item.Tags.Title == "" && item.Tags.Album == "" && len(item.Tags.Artists) == 0 {
			item.Tags = tags
		}
		language := ""
		for k, v := range raw.Tags {
			if strings.EqualFold(k, "language") {
				language = v
			}
		}
		stream := Stream{Index: raw.Index, Codec: raw.CodecName, Language: language,
			Title: tags.Title, CodecTag: raw.CodecTag, TimeBase: raw.TimeBase,
			Profile: profileName(raw.Profile), Level: raw.Level,
			IsDefault: raw.Disposition.Default != 0, IsForced: raw.Disposition.Forced != 0,
			IsHearingImpaired: raw.Disposition.HearingImpaired != 0,
			IsInterlaced:      raw.FieldOrder != "" && raw.FieldOrder != "progressive"}
		switch raw.CodecType {
		case "video":
			// An attached picture is a cover, not a video track; an audio
			// file's first JPEG or PNG one is its cover.
			if raw.Disposition.AttachedPic != 0 {
				if mime := coverMIME(raw.CodecName); item.Cover == (Image{}) && mime != "" && raw.Width > 0 && raw.Height > 0 {
					item.Cover = Image{Width: raw.Width, Height: raw.Height, MIME: mime}
				}
				continue
			}
			stream.Type, stream.Width, stream.Height = "Video", raw.Width, raw.Height
			stream.PixelFormat, stream.RefFrames = raw.PixelFormat, raw.RefFrames
			stream.BitDepth, _ = strconv.Atoi(raw.BitDepth)
			stream.AverageFrameRate = frameRate(raw.AvgFrameRate)
			stream.RealFrameRate = frameRate(raw.RFrameRate)
			for _, side := range raw.SideData {
				if side.Rotation != 0 {
					stream.Rotation = (int(math.Round(side.Rotation))%360 + 360) % 360
				}
			}
		case "audio":
			stream.Type, stream.Channels = "Audio", raw.Channels
			stream.ChannelLayout = raw.ChannelLayout
			stream.SampleRate, _ = strconv.Atoi(raw.SampleRate)
			stream.BitDepth = raw.SampleBits
		case "subtitle":
			stream.Type = "Subtitle"
		default:
			continue
		}
		stream.BitRate, _ = strconv.ParseInt(raw.BitRate, 10, 64)
		item.Streams = append(item.Streams, stream)
	}
	if len(item.Streams) == 0 {
		return Item{}, errors.New("no video or audio stream")
	}
	// FLAC and other lossless streams report no bit rate of their own: an
	// audio file's only stream takes the file's.
	if only := &item.Streams[0]; len(item.Streams) == 1 && only.Type == "Audio" && only.BitRate == 0 {
		only.BitRate, _ = strconv.ParseInt(result.Format.BitRate, 10, 64)
	}
	return item, nil
}
