package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/TokenCemetery/couch/internal/hls"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// audioBitrate is the AAC bit rate of an encoded track, unless the client
// asks for less.
const audioBitrate = 192000

// universalAudio answers Audio/{id}/universal, which Emby Web plays tracks
// from without asking for PlaybackInfo: the file itself when the client
// lists its container and codec in Container and it fits the limits, else
// the track encoded to AAC. Couch answers a TranscodingProtocol of hls with
// a whole encoded M4A file too, which the audio element plays and seeks.
func (s *Server) universalAudio(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	catalog := s.catalogFor(session)
	item, found := findItem(catalog, r.PathValue("item"))
	if !found || item.Type() != "Audio" {
		fail(w, 404, "NotFound")
		return
	}
	stream := audioStream(item)
	query := r.URL.Query()
	if playsDirectly(item, stream, query) {
		file, err := catalog.Open(item)
		if err != nil {
			slog.Warn("Media file unavailable", "item", item.ID)
			fail(w, 404, "MediaUnavailable")
			return
		}
		defer func() { _ = file.Close() }()
		serveMediaFile(w, r, file, item)
		return
	}
	if s.hls == nil {
		slog.Warn("Playback refused", "item", item.ID, "reason", "the client cannot play the track and FFmpeg is not available")
		failText(w, 400, "This track needs transcoding, which requires FFmpeg on the server.")
		return
	}
	bitrate := int64(audioBitrate)
	if limit, err := strconv.ParseInt(query.Get("MaxStreamingBitrate"), 10, 64); err == nil && limit > 0 {
		bitrate = max(min(bitrate, limit), 64000)
	}
	channels := min(max(stream.Channels, 1), 2)
	source := hls.AudioSource{ItemID: item.ID, Version: fmt.Sprintf("%d/%d", item.Size, item.Modified.UnixNano()),
		Open: func() (*os.File, error) { return catalog.Open(item) }, Stream: stream.Index, Channels: channels, Bitrate: bitrate}
	file, err := s.hls.Audio(r.Context(), source)
	switch {
	case err == nil:
	case errors.Is(err, hls.ErrBusy):
		w.Header().Set("Retry-After", "10")
		fail(w, 503, "TooManyRequests")
		return
	case r.Context().Err() != nil:
		return
	default:
		fail(w, 500, "TranscodingFailed")
		return
	}
	defer func() { _ = file.Close() }()
	serveFile(w, r, file, "audio/mp4", derive("audio", item.ID+"\x00"+source.Version+"\x00"+strconv.FormatInt(bitrate, 10)))
}

// audioStream is the track's first audio stream.
func audioStream(item media.Item) media.Stream {
	for _, s := range item.Streams {
		if s.Type == "Audio" {
			return s
		}
	}
	return media.Stream{}
}

// playsDirectly reports whether the client can play the file itself: no
// Container list means it did not say, so it gets the file, as from Emby.
// Each entry is a container, optionally with "|codec".
func playsDirectly(item media.Item, stream media.Stream, query map[string][]string) bool {
	get := func(key string) string {
		for k, v := range query {
			if strings.EqualFold(k, key) && len(v) > 0 {
				return v[0]
			}
		}
		return ""
	}
	for key, value := range map[string]int64{"MaxStreamingBitrate": bitrate(item), "MaxAudioSampleRate": int64(stream.SampleRate), "MaxAudioBitDepth": int64(stream.BitDepth)} {
		if limit, err := strconv.ParseInt(get(key), 10, 64); err == nil && limit > 0 && value > limit {
			return false
		}
	}
	containers := get("Container")
	if containers == "" {
		return true
	}
	for _, entry := range strings.Split(containers, ",") {
		container, codec, _ := strings.Cut(strings.TrimSpace(entry), "|")
		if strings.EqualFold(container, item.Container) && (codec == "" || strings.EqualFold(codec, stream.Codec)) {
			return true
		}
	}
	return false
}
