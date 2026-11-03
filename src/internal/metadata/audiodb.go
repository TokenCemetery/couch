package metadata

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

var audioDBAPI = "https://www.theaudiodb.com/api/v1/json/"

// audioDB is a TheAudioDB API v1 client for music artists and albums, the
// source the reference Emby uses for them (#136).
type audioDB struct {
	key  string
	lang language
	pace *throttle
}

func newAudioDB(key string, lang language) *audioDB {
	return &audioDB{key: key, lang: lang, pace: &throttle{every: spacing.audiodb}}
}

// audioDBEntry is an artist or album as TheAudioDB answers it: every value
// is a string or null, and the localized texts are fields of their own.
type audioDBEntry map[string]any

func (e audioDBEntry) text(key string) string {
	s, _ := e[key].(string)
	return strings.TrimSpace(s)
}

// localized returns the text of field in the metadata language, else in
// English, else under the field's bare name, as Jellyfin reads it.
func (e audioDBEntry) localized(field string, lang language) string {
	for _, suffix := range []string{lang.audiodb, "EN", ""} {
		if s := e.text(field + suffix); s != "" {
			return s
		}
	}
	return ""
}

func (a *audioDB) get(ctx context.Context, method string, params url.Values, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, audioDBAPI+url.PathEscape(a.key)+"/"+method+"?"+params.Encode(), nil)
	if err != nil {
		return errRequest
	}
	return fetchJSON(req, a.pace, v)
}

// artist looks a music artist up by name: an exact match of their name or
// one of their alternate names, ignoring case and punctuation. It returns
// the biography and provider IDs, and the URLs of the photo and fanart.
func (a *audioDB) artist(ctx context.Context, name string) (Info, map[string]string, error) {
	var body struct {
		Artists []audioDBEntry `json:"artists"`
	}
	if err := a.get(ctx, "search.php", url.Values{"s": {name}}, &body); err != nil {
		return Info{}, nil, err
	}
	for _, e := range body.Artists {
		names := append([]string{e.text("strArtist")}, strings.Split(e.text("strArtistAlternate"), ",")...)
		if !slices.ContainsFunc(names, func(n string) bool { return sameTitle(n, name) }) {
			continue
		}
		info := Info{Overview: e.localized("strBiography", a.lang), ProviderIDs: providerIDsOf(map[string]string{
			"AudioDbArtist": e.text("idArtist"), "MusicBrainzArtist": e.text("strMusicBrainzID")})}
		return info, map[string]string{"Primary": e.text("strArtistThumb"), "Backdrop": e.text("strArtistFanart")}, nil
	}
	return Info{}, nil, errNotFound
}

// album looks an album up by its album artist and exact title; of albums
// with that title, the one released in year wins. It returns the
// description and provider IDs, and the URL of the cover.
func (a *audioDB) album(ctx context.Context, artist, title string, year int) (Info, map[string]string, error) {
	var body struct {
		Album []audioDBEntry `json:"album"`
	}
	if err := a.get(ctx, "searchalbum.php", url.Values{"s": {artist}, "a": {title}}, &body); err != nil {
		return Info{}, nil, err
	}
	var found audioDBEntry
	for _, e := range body.Album {
		if !sameTitle(e.text("strAlbum"), title) {
			continue
		}
		if found == nil || (year > 0 && e.text("intYearReleased") == strconv.Itoa(year)) {
			found = e
		}
	}
	if found == nil {
		return Info{}, nil, errNotFound
	}
	info := Info{Overview: found.localized("strDescription", a.lang), ProviderIDs: providerIDsOf(map[string]string{
		"AudioDbAlbum": found.text("idAlbum"), "MusicBrainzReleaseGroup": found.text("strMusicBrainzID")})}
	return info, map[string]string{"Primary": found.text("strAlbumThumb")}, nil
}

// providerIDsOf drops the empty IDs; nil when none is left.
func providerIDsOf(ids map[string]string) map[string]string {
	for key, id := range ids {
		if id == "" {
			delete(ids, key)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}
