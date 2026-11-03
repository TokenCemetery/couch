package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

func randomSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// bitrate estimates the source bitrate from the container size and duration.
// Emby reports a slightly higher figure than the sum of stream bitrates; Couch
// derives its own value rather than copying a formula it cannot verify.
func bitrate(item media.Item) int64 {
	if item.RunTimeTicks <= 0 || item.Size <= 0 {
		return 0
	}
	seconds := float64(item.RunTimeTicks) / 10000000
	rate := math.Ceil(float64(item.Size) * 8 / seconds)
	if rate >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(rate)
}

func (s *Server) playbackInfo(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	catalog := s.catalogFor(session)
	item, found := findItem(catalog, r.PathValue("item"))
	if !found || item.IsFolder() {
		fail(w, 404, "NotFound")
		return
	}
	body, err := readPlaybackRequest(w, r)
	if err != nil {
		fail(w, 400, "InvalidRequest")
		return
	}
	if body.UserId != nil && *body.UserId != "" && !strings.EqualFold(*body.UserId, session.UserID) {
		fail(w, 403, "Forbidden")
		return
	}
	if body.MediaSourceId != nil && *body.MediaSourceId != "" && *body.MediaSourceId != "mediasource_"+item.ID {
		fail(w, 404, "MediaSourceNotFound")
		return
	}
	video, audio := playbackStreams(item, body.AudioStreamIndex)
	if body.AudioStreamIndex != nil && audio == nil {
		fail(w, 400, "InvalidAudioStreamIndex")
		return
	}
	if body.SubtitleStreamIndex != nil && *body.SubtitleStreamIndex >= 0 && !hasSubtitle(item, *body.SubtitleStreamIndex) {
		fail(w, 400, "InvalidSubtitleStreamIndex")
		return
	}
	playSession := randomSessionID()
	// Emby Web switches quality or audio track by asking again with the play
	// it replaces in CurrentPlaySessionId, and deletes that play's encoding
	// only after this answer. Ending it here frees its HLS slot, which a switch
	// between two transcoded qualities would otherwise find taken.
	if current := r.URL.Query().Get("CurrentPlaySessionId"); current != "" {
		if len(current) > 128 {
			fail(w, 400, "InvalidRequest")
			return
		}
		if s.hls != nil {
			s.hls.Stop(current, session.ID)
		}
		if err := s.continuePlay(token, item.ID, current, playSession); err != nil {
			s.changed(w, err)
			return
		}
	}
	streams := mediaStreams(item)
	// Image subtitles are burned into a transcode, which the client must be
	// able to take.
	_, _, _, canEncode := body.transcodeCodecs(item, video, audio)
	burnable := func(index int) bool { return s.hls != nil && canEncode && media.SubtitleBurnable(item, index) }
	// Deliverable subtitles are fetched by the player beside the direct
	// stream. Like the stream URL, theirs carries the token for a <track>.
	// Emby Web asks for a new stream when an Encode subtitle is selected or
	// turned off.
	for i, stream := range item.Streams {
		switch {
		case catalog.SubtitleDeliverable(item, stream.Index):
			streams[i]["DeliveryMethod"] = "External"
			streams[i]["DeliveryFormat"] = "vtt"
			streams[i]["IsExternalUrl"] = false
			streams[i]["DeliveryUrl"] = subtitleURL(item, stream.Index, token)
		case burnable(stream.Index):
			streams[i]["DeliveryMethod"] = "Encode"
		}
	}
	source := mediaSource(item, streams)
	// Text subtitles are delivered as WebVTT and image subtitles burned in. A
	// selected subtitle Couch can do neither with is dropped and the video
	// plays without it, rather than refusing playback.
	subtitle := -1
	deliverable := func(index int) bool { return catalog.SubtitleDeliverable(item, index) }
	switch {
	case body.SubtitleStreamIndex != nil && *body.SubtitleStreamIndex >= 0:
		subtitle = int(*body.SubtitleStreamIndex)
	case body.SubtitleStreamIndex == nil:
		// The client left the choice to the server's subtitle mode. A text
		// track is preferred to one that needs a transcode.
		subtitle = defaultSubtitle(item, s.subtitleSelection(session), audio, deliverable)
		if subtitle < 0 {
			subtitle = defaultSubtitle(item, s.subtitleSelection(session), audio, burnable)
		}
	}
	if subtitle >= 0 && !deliverable(subtitle) && !burnable(subtitle) {
		slog.Warn("Subtitle not deliverable, playing without it", "item", item.ID, "index", subtitle)
		subtitle = -1
	}
	burn := subtitle >= 0 && !deliverable(subtitle)
	direct := !burn && body.supportsDirectStream(item, video, audio)
	// Remux to HLS when the client cannot take the file as it is, and encode
	// the video to H.264 when it cannot take the video either. A direct stream
	// still reports SupportsTranscoding when HLS could serve it: Emby Web offers
	// its quality menu only then, and a lower quality comes back here as a
	// PlaybackInfo with MaxStreamingBitrate.
	var transcoding string
	remuxBusy, canTranscode := false, false
	if s.hls != nil {
		var videoCodec, audioCodec string
		var channels int
		var ok bool
		if !burn {
			videoCodec, audioCodec, channels, ok = body.remuxCodecs(item, video, audio)
		}
		var encoding videoEncoding
		if !ok {
			audioCodec, channels, encoding, ok = body.transcodeCodecs(item, video, audio)
			videoCodec = "h264"
		}
		canTranscode = ok
		switch {
		case !ok || direct:
		case s.hls.Available(playSession, encoding.Bitrate > 0):
			transcoding = transcodingURL(item, session.DeviceID, playSession, token, videoCodec, audioCodec, audio, channels, encoding)
			if burn {
				transcoding += "&SubtitleStreamIndex=" + strconv.Itoa(subtitle) + "&SubtitleMethod=Encode"
			}
			// A transcode's plan needs no keyframe listing (#98). Emby Web
			// asks with IsPlayback=false for an item's page, which reads
			// nothing, as with Emby; a client that leaves it out may play.
			if body.IsPlayback == nil || *body.IsPlayback {
				prepared := remuxSource(catalog, item, video)
				prepared.VideoEncode = encoding.Bitrate > 0
				s.hls.Prepare(prepared)
			}
		default:
			remuxBusy = true
		}
	}
	// Like Emby, an HLS stream carries the text subtitles in its master
	// playlist when the client takes them there; the player switches between
	// them without a new PlaybackInfo.
	if transcoding != "" && body.hlsSubtitles() {
		var indexes []string
		for i, stream := range item.Streams {
			if streams[i]["DeliveryMethod"] == "External" {
				streams[i]["DeliveryMethod"] = "Hls"
				delete(streams[i], "DeliveryUrl")
				indexes = append(indexes, strconv.Itoa(stream.Index))
			}
		}
		if len(indexes) > 0 {
			transcoding += "&SubtitleStreamIndexes=" + strings.Join(indexes, ",") + "&ManifestSubtitles=vtt"
		}
	}
	source["SupportsDirectPlay"] = false
	source["SupportsDirectStream"] = direct
	source["SupportsTranscoding"] = transcoding != "" || (direct && canTranscode)
	source["ItemId"] = item.ID
	source["Formats"] = []string{}
	source["RequiredHttpHeaders"] = object{}
	source["AddApiKeyToDirectStreamUrl"] = false
	source["Bitrate"] = bitrate(item)
	if audio != nil {
		source["DefaultAudioStreamIndex"] = audio.Index
	}
	source["DefaultSubtitleStreamIndex"] = -1
	if subtitle >= 0 && (direct || transcoding != "") {
		source["DefaultSubtitleStreamIndex"] = subtitle
	}
	response := object{"MediaSources": []object{source}, "PlaySessionId": playSession}
	switch {
	case direct:
		source["DirectStreamUrl"] = directStreamURL(item, session.DeviceID, playSession, token)
	case transcoding != "":
		source["TranscodingUrl"] = transcoding
		source["TranscodingSubProtocol"] = "hls"
		source["TranscodingContainer"] = "ts"
	case remuxBusy:
		response["ErrorCode"] = "RateLimitExceeded"
	default:
		response["ErrorCode"] = "NoCompatibleStream"
	}
	if code, refused := response["ErrorCode"]; refused {
		videoCodec, audioCodec := "", ""
		if video != nil {
			videoCodec = video.Codec
		}
		if audio != nil {
			audioCodec = audio.Codec
		}
		slog.Warn("Playback refused", "item", item.ID, "error", code, "container", item.Container,
			"video", videoCodec, "audio", audioCodec, "profile", body.DeviceProfile != nil, "hls", s.hls != nil)
	}
	respond(w, 200, response)
}

// subtitleSelection reads the user's subtitle mode and languages, with Emby's
// defaults for values the user never saved.
func (s *Server) subtitleSelection(session state.Session) subtitleSelection {
	config := userOf(s.store.Snapshot(), session).Configuration
	selection := subtitleSelection{SubtitleMode: "Default"}
	_ = json.Unmarshal(config["SubtitleMode"], &selection.SubtitleMode)
	_ = json.Unmarshal(config["SubtitleLanguagePreference"], &selection.SubtitleLanguagePreference)
	return selection
}

// subtitleURL is relative like directStreamURL, for the same reason.
func subtitleURL(item media.Item, index int, token string) string {
	return "/videos/" + item.ID + "/mediasource_" + item.ID + "/subtitles/" + strconv.Itoa(index) + "/stream.vtt?api_key=" + url.QueryEscape(token)
}

// streamSubtitle serves a sidecar or embedded text subtitle as WebVTT, and a
// track's lyrics as Stream.js. The start position variant of the route
// returns the whole file: players seek within it.
func (s *Server) streamSubtitle(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	item, found := findItem(s.catalogFor(session), r.PathValue("item"))
	index, err := strconv.Atoi(r.PathValue("index"))
	if found && !item.IsFolder() && err == nil && r.PathValue("source") == "mediasource_"+item.ID && r.PathValue("file") == "stream.js" {
		s.streamLyrics(w, r, item, index)
		return
	}
	if !found || item.IsFolder() || err != nil || r.PathValue("source") != "mediasource_"+item.ID || r.PathValue("file") != "stream.vtt" {
		fail(w, 404, "NotFound")
		return
	}
	text, ok := s.subtitleText(w, r, item, index)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	_, _ = w.Write(text) //nolint:gosec // WebVTT served as text/vtt, not HTML
}

// streamLyrics serves a lyrics stream as Emby's subtitle track events. Lines
// without times have no StartPositionTicks, which Emby Web's lyrics view
// shows without following playback.
func (s *Server) streamLyrics(w http.ResponseWriter, r *http.Request, item media.Item, index int) {
	lines, err := s.catalog().OpenLyrics(r.Context(), item, index)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, 404, "NotFound")
		return
	}
	if err != nil {
		slog.Warn("Lyrics unavailable", "item", item.ID, "index", index)
		fail(w, 404, "MediaUnavailable")
		return
	}
	events := make([]object, 0, len(lines))
	for i, line := range lines {
		event := object{"Id": strconv.Itoa(i), "Text": line.Text}
		if line.Start != nil {
			event["StartPositionTicks"], event["EndPositionTicks"] = *line.Start, *line.End
		}
		events = append(events, event)
	}
	respond(w, 200, object{"TrackEvents": events})
}

// subtitleText returns a subtitle stream as WebVTT. On failure the response
// has been written.
func (s *Server) subtitleText(w http.ResponseWriter, r *http.Request, item media.Item, index int) ([]byte, bool) {
	text, err := s.catalog().OpenSubtitle(r.Context(), item, index)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, 404, "NotFound")
		return nil, false
	}
	if err != nil {
		slog.Warn("Subtitle unavailable", "item", item.ID, "index", index)
		fail(w, 404, "MediaUnavailable")
		return nil, false
	}
	return text, true
}

// directStreamURL matches the relative form Emby returns: no "/emby" prefix,
// because the client passes this through apiClient.getUrl, which adds the
// prefix itself. Including it here yields "/emby/emby/..." and a dead URL.
//
// The access token is embedded, as the reference server does, because the URL
// is handed to a media element that cannot send an Authorization header. Couch
// never logs request URLs, so the token stays out of the logs.
func directStreamURL(item media.Item, deviceID, playSession, token string) string {
	query := url.Values{}
	query.Set("MediaSourceId", "mediasource_"+item.ID)
	query.Set("PlaySessionId", playSession)
	query.Set("Static", "true")
	query.Set("api_key", token)
	if deviceID != "" {
		query.Set("DeviceId", deviceID)
	}
	name := "stream"
	if item.Container != "" {
		name += "." + item.Container
	}
	return "/videos/" + item.ID + "/" + name + "?" + query.Encode()
}

// findItem returns the item or series or season with this ID in catalog.
func findItem(catalog *media.Catalog, id string) (media.Item, bool) {
	if catalog == nil || id == "" {
		return media.Item{}, false
	}
	for _, items := range [][]media.Item{catalog.Items, catalog.Folders} {
		for _, item := range items {
			if item.ID == id {
				return item, true
			}
		}
	}
	return media.Item{}, false
}

// streamVideo serves the original file. http.ServeContent implements Range,
// If-Range, HEAD, 206 and 416 exactly, so Couch does not reimplement them, and
// the file is streamed rather than read into memory.
func (s *Server) streamVideo(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	// The route may carry a container suffix, as in /videos/{id}/stream.mp4.
	if !streamRoute(r.PathValue("stream")) {
		fail(w, 404, "NotFound")
		return
	}
	item, found := findItem(s.catalogFor(session), r.PathValue("item"))
	if !found || item.IsFolder() {
		fail(w, 404, "NotFound")
		return
	}
	file, err := s.catalog().Open(item)
	if err != nil {
		// A catalogued file that cannot be opened usually means an unmounted
		// or moved library; the client only shows a generic playback error.
		slog.Warn("Media file unavailable", "item", item.ID)
		fail(w, 404, "MediaUnavailable")
		return
	}
	defer func() { _ = file.Close() }()
	serveMediaFile(w, r, file, item)
}

// downloadItem serves the original file as an attachment for the "Download"
// command. Emby Web navigates to the URL, so the token arrives as api_key.
// The file name is built from the item name: Path is never sent to clients.
func (s *Server) downloadItem(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	item, found := findItem(s.catalogFor(session), r.PathValue("item"))
	if !found || item.IsFolder() {
		fail(w, 404, "NotFound")
		return
	}
	file, err := s.catalog().Open(item)
	if err != nil {
		// A catalogued file that cannot be opened usually means an unmounted
		// or moved library; the client only shows a generic playback error.
		slog.Warn("Media file unavailable", "item", item.ID)
		fail(w, 404, "MediaUnavailable")
		return
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Name + path.Ext(item.Path)}))
	serveMediaFile(w, r, file, item)
}

// playstateReport is the body Emby Web posts to the /Sessions/Playing family.
// Unknown fields are ignored: Couch only persists what it can act on.
type playstateReport struct {
	ItemId        string
	PositionTicks *int64
	PlaySessionId string
	IsPaused      bool
}

// reportPlaystate records progress. A report for an unknown item is refused
// rather than stored, so a stale client cannot grow the state file.
func (s *Server) reportPlaystate(event string) authenticated {
	return func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var body playstateReport
		if r.ContentLength != 0 && decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		if body.ItemId == "" {
			body.ItemId = value(r, "ItemId")
		}
		if body.PlaySessionId == "" {
			body.PlaySessionId = value(r, "PlaySessionId")
		}
		if len(body.PlaySessionId) > 128 {
			fail(w, 400, "InvalidRequest")
			return
		}
		if event == "stop" && body.PlaySessionId != "" && s.hls != nil {
			s.hls.Stop(body.PlaySessionId, session.ID)
		}
		item, found := findItem(s.catalogFor(session), body.ItemId)
		if !found || item.IsFolder() {
			fail(w, 404, "NotFound")
			return
		}
		position := int64(0)
		hasPosition := body.PositionTicks != nil
		if body.PositionTicks != nil {
			position = *body.PositionTicks
		} else if text := value(r, "PositionTicks"); text != "" {
			parsed, err := strconv.ParseInt(text, 10, 64)
			if err != nil || parsed < 0 {
				fail(w, 400, "InvalidRequest")
				return
			}
			position = parsed
			hasPosition = true
		}
		if position < 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		if item.RunTimeTicks > 0 {
			position = min(position, item.RunTimeTicks)
		}
		problem := ""
		_, err := s.updateItemSession(token, item, func(st *state.ItemState, current *state.Session) {
			if problem = playReportProblem(current, item.ID, body.PlaySessionId, event); problem != "" {
				return
			}
			if hasPosition {
				st.PositionTicks = position
			}
			st.LastPlayed = time.Now().UTC()
			st.HiddenFromResume = false
			switch event {
			case "start":
				st.PlayCount++
			case "stop":
				// Finishing the tail of a file counts as watched, and the
				// stored position is cleared so it does not resume at the end.
				if item.RunTimeTicks > 0 && st.PositionTicks >= item.RunTimeTicks-item.RunTimeTicks/10 {
					st.Played, st.PositionTicks = true, 0
				}
			}
		})
		// The client is still answered 204: a duplicate or late report is
		// normal after retries. The log lets a lost position be diagnosed.
		if err == nil && problem != "" {
			slog.Warn("Playback report ignored", "event", event, "reason", problem, "item", item.ID)
		}
		if err == nil && problem == "" && event == "stop" && !hasPosition {
			slog.Info("Playback stopped without a position", "item", item.ID)
		}
		if err == nil && problem == "" {
			s.played(session, event, item, position, body.IsPaused)
		}
		s.changed(w, err)
	}
}

// maxSwitches bounds the IDs one play keeps from quality and track switches.
const maxSwitches = 8

// continuePlay adds playSession to the client's current play of item. After a
// switch Emby Web reports progress and stop with the new PlaySessionId when the
// stream changed, and keeps the old one when it did not, without a new start
// (#73). Only the session's newest play of the same item, not yet stopped and
// named by current, continues; anything else is left alone.
func (s *Server) continuePlay(token, itemID, current, playSession string) error {
	s.itemMu.Lock()
	defer s.itemMu.Unlock()
	return s.store.Change(token, func(_ *state.User, session *state.Session) {
		n := len(session.RecentPlays)
		if n == 0 {
			return
		}
		play := &session.RecentPlays[n-1]
		if play.Is(current) && play.ItemID == itemID && !play.Stopped {
			if len(play.Switched) >= maxSwitches {
				play.Switched = slices.Clone(play.Switched[len(play.Switched)-maxSwitches+1:])
			}
			play.Switched = append(play.Switched, playSession)
		}
	})
}

// ID-bearing reports use a durable 64-start retry window per client session.
// Only the newest start accepts progress/stop; a completed play cannot reopen.
// Legacy reports without an ID retain arrival-order semantics.
func acceptPlayReport(session *state.Session, itemID, playID, event string) bool {
	return playReportProblem(session, itemID, playID, event) == ""
}

// playReportProblem applies acceptPlayReport's rules and names the rule that
// rejects a report, or returns "" when the report is accepted.
func playReportProblem(session *state.Session, itemID, playID, event string) string {
	if playID == "" {
		return ""
	}
	index := slices.IndexFunc(session.RecentPlays, func(p state.PlaybackRecord) bool { return p.Is(playID) })
	if event == "start" {
		if index >= 0 {
			return "repeated start"
		}
		if len(session.RecentPlays) >= 64 {
			session.RecentPlays = slices.Clone(session.RecentPlays[len(session.RecentPlays)-63:])
		}
		session.RecentPlays = append(session.RecentPlays, state.PlaybackRecord{ID: playID, ItemID: itemID})
		return ""
	}
	switch {
	case index < 0:
		return "unknown play session"
	case index != len(session.RecentPlays)-1:
		return "not the newest play session"
	}
	play := &session.RecentPlays[index]
	switch {
	case play.Stopped:
		return "play session already stopped"
	case play.ItemID != itemID:
		return "play session belongs to another item"
	}
	if event == "stop" {
		play.Stopped = true
	}
	return ""
}

// setItemFlag backs the played and favourite toggles, which answer with the
// item's updated UserData rather than an empty body.
func (s *Server) setItemFlag(favorite, value bool) authenticated {
	return func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		item, found := findItem(s.catalogFor(session), r.PathValue("item"))
		if set, ok := s.sessionBoxSets(session)[r.PathValue("item")]; ok {
			s.setBoxSetFlag(w, token, session, set, favorite, value)
			return
		}
		if list, ok := s.sessionPlaylists(session)[r.PathValue("item")]; ok {
			s.setPlaylistFlag(w, token, session, list, favorite, value)
			return
		}
		// An artist can be a favorite, which the Favorites tab lists.
		if e, ok := s.entityIn(s.catalogFor(session), r.PathValue("item")); !found && ok && e.Type == "MusicArtist" && favorite {
			data, err := s.updateItem(token, media.Item{ID: r.PathValue("item")}, func(st *state.ItemState) { st.IsFavorite = value })
			if err != nil {
				s.changed(w, err)
				return
			}
			respond(w, 200, data)
			return
		}
		if !found {
			fail(w, 404, "NotFound")
			return
		}
		// Marking a series or season played marks its episodes, as in Emby; the
		// folder's own Played is derived from them.
		if !favorite && item.IsFolder() {
			if err := s.setEpisodesPlayed(token, session.UserID, s.folderEpisodes(item), value); err != nil {
				s.changed(w, err)
				return
			}
			data := s.folderPlayedData(item, userOf(s.store.Snapshot(), session).Items)
			data["ItemId"] = item.ID
			respond(w, 200, data)
			return
		}
		data, err := s.updateItem(token, item, func(st *state.ItemState) {
			if favorite {
				st.IsFavorite = value
				return
			}
			setPlayed(st, value)
		})
		if err != nil {
			s.changed(w, err)
			return
		}
		respond(w, 200, data)
	}
}

// setEpisodesPlayed marks episodes in one transaction, so a failure leaves
// none of them changed, then publishes each episode's new user data.
func (s *Server) setEpisodesPlayed(token, userID string, episodes []media.Item, value bool) error {
	ids := make([]string, len(episodes))
	for i, episode := range episodes {
		ids[i] = episode.ID
	}
	s.itemMu.Lock()
	defer s.itemMu.Unlock()
	saved, err := s.store.SetItems(token, ids, func(_ string, st *state.ItemState) { setPlayed(st, value) })
	if err != nil {
		return err
	}
	for _, episode := range episodes {
		data := itemUserData(saved[episode.ID], episode.RunTimeTicks)
		data["ItemId"] = episode.ID
		s.publishUserData(userID, data)
	}
	return nil
}

func setPlayed(st *state.ItemState, value bool) {
	st.Played = value
	if value {
		st.PositionTicks = 0
		st.LastPlayed = time.Now().UTC()
		if st.PlayCount == 0 {
			st.PlayCount = 1
		}
	}
}

// hideFromResume serves "Remove from Continue Watching". Hiding clears the
// stored position, which removes an item in progress, and marks the item so a
// next-up episode is not offered either. Hide=false clears the mark only; the
// position is not restored.
func (s *Server) hideFromResume(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	var hide *bool
	if err != nil || playbackQuery(query, "Hide", &hide, strconv.ParseBool) != nil || hide == nil {
		fail(w, 400, "InvalidRequest")
		return
	}
	item, found := findItem(s.catalogFor(session), r.PathValue("item"))
	if !found {
		fail(w, 404, "NotFound")
		return
	}
	data, err := s.updateItem(token, item, func(st *state.ItemState) {
		if *hide {
			st.PositionTicks = 0
		}
		st.HiddenFromResume = *hide
	})
	if err != nil {
		s.changed(w, err)
		return
	}
	respond(w, 200, data)
}

func (s *Server) playbackRoutes(mux *http.ServeMux) {
	for path, event := range map[string]string{
		"/sessions/playing":          "start",
		"/sessions/playing/progress": "progress",
		"/sessions/playing/stopped":  "stop",
	} {
		mux.HandleFunc("POST "+path, s.protect(s.reportPlaystate(event)))
	}
	for _, path := range []string{"/users/{user}/playeditems/{item}", "/users/{user}/favoriteitems/{item}"} {
		favorite := strings.Contains(path, "favorite")
		mux.HandleFunc("POST "+path, s.protect(s.setItemFlag(favorite, true)))
		mux.HandleFunc("DELETE "+path, s.protect(s.setItemFlag(favorite, false)))
		// Emby Web clears the flag with POST .../Delete instead of DELETE.
		mux.HandleFunc("POST "+path+"/delete", s.protect(s.setItemFlag(favorite, false)))
	}
	mux.HandleFunc("POST /users/{user}/items/{item}/hidefromresume", s.protect(s.hideFromResume))
	mux.HandleFunc("GET /users/{user}/items/resume", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		s.listItems(w, r, session, false, true)
	}))
	mux.HandleFunc("POST /items/{item}/playbackinfo", s.protect(s.playbackInfo))
	// Emby also answers GET for clients that cannot post a profile.
	mux.HandleFunc("GET /items/{item}/playbackinfo", s.protect(s.playbackInfo))
	mux.HandleFunc("GET /items/{item}/download", s.protect(s.downloadItem))
	for _, prefix := range []string{"/videos", "/items"} {
		mux.HandleFunc("GET "+prefix+"/{item}/{source}/subtitles/{index}/{file}", s.protect(s.streamSubtitle))
		mux.HandleFunc("GET "+prefix+"/{item}/{source}/subtitles/{index}/{start}/{file}", s.protect(s.streamSubtitle))
	}
	for _, prefix := range []string{"/videos", "/audio"} {
		// A GET pattern also serves HEAD.
		mux.HandleFunc("GET "+prefix+"/{item}/{stream}", s.protect(s.streamVideo))
	}
	mux.HandleFunc("GET /audio/{item}/universal", s.protect(s.universalAudio))
}

// streamRoute rejects anything but the direct stream endpoint; HLS has its
// own routes (hls.go).
func streamRoute(name string) bool {
	return strings.TrimSuffix(name, path.Ext(name)) == "stream"
}
