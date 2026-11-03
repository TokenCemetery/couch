package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

var (
	tvdbAPI     = "https://api4.thetvdb.com/v4"
	tvdbArtwork = "https://artworks.thetvdb.com"
)

const (
	// tvdbBackground is the artwork type of a series background.
	tvdbBackground = 3
)

// tvdb is a TheTVDB API v4 client for series, seasons and episodes.
type tvdb struct {
	key   string
	lang  language
	pace  *throttle
	token string
	// seasons and episodes cache a series' seasons and a season's
	// episodes within a refresh run.
	seasons  map[string][]tvdbSeason
	episodes map[string][]tvdbEpisode
}

func newTVDb(key string, lang language) *tvdb {
	return &tvdb{key: key, lang: lang, pace: &throttle{every: spacing.tvdb}, seasons: map[string][]tvdbSeason{}, episodes: map[string][]tvdbEpisode{}}
}

func (t *tvdb) login(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{"apikey": t.key})
	if err != nil {
		return errRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tvdbAPI+"/login", bytes.NewReader(body))
	if err != nil {
		return errRequest
	}
	req.Header.Set("Content-Type", "application/json")
	var result struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := fetchJSON(req, t.pace, &result); err != nil {
		return err
	}
	if result.Data.Token == "" {
		return errAuth
	}
	t.token = result.Data.Token
	return nil
}

// get logs in when needed and once more when the token has expired.
func (t *tvdb) get(ctx context.Context, path string, params url.Values, v any) error {
	for attempt := 0; ; attempt++ {
		if t.token == "" {
			if err := t.login(ctx); err != nil {
				return err
			}
		}
		target := tvdbAPI + path
		if len(params) > 0 {
			target += "?" + params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return errRequest
		}
		req.Header.Set("Authorization", "Bearer "+t.token)
		err = fetchJSON(req, t.pace, v)
		if errors.Is(err, errAuth) && attempt == 0 {
			t.token = ""
			continue
		}
		return err
	}
}

func tvdbImage(path string) string {
	if strings.HasPrefix(path, "/") {
		return tvdbArtwork + path
	}
	return path
}

func (t *tvdb) seriesID(ctx context.Context, ids map[string]string, title string, year int) (string, error) {
	if id := ids["Tvdb"]; id != "" {
		return id, nil
	}
	if title == "" {
		return "", errNotFound
	}
	for _, withYear := range []bool{year > 0, false} {
		params := url.Values{"query": {title}, "type": {"series"}, "limit": {"1"}}
		if withYear {
			params.Set("year", strconv.Itoa(year))
		}
		var result struct {
			Data []struct {
				TVDbID string `json:"tvdb_id"`
			} `json:"data"`
		}
		if err := t.get(ctx, "/search", params, &result); err != nil {
			return "", err
		}
		if len(result.Data) > 0 && digits.MatchString(result.Data[0].TVDbID) {
			return result.Data[0].TVDbID, nil
		}
		if !withYear {
			break
		}
	}
	return "", errNotFound
}

type tvdbTranslation struct {
	Data struct {
		Name     string `json:"name"`
		Overview string `json:"overview"`
		Tagline  string `json:"tagline"`
	} `json:"data"`
}

func (t *tvdb) series(ctx context.Context, ids map[string]string, title string, year int) (Info, map[string]string, error) {
	id, err := t.seriesID(ctx, ids, title, year)
	if err != nil {
		return Info{}, nil, err
	}
	var s struct {
		Data struct {
			Name       string `json:"name"`
			Overview   string `json:"overview"`
			FirstAired string `json:"firstAired"`
			Image      string `json:"image"`
			Genres     []struct {
				Name string `json:"name"`
			} `json:"genres"`
			OriginalNetwork *struct {
				Name string `json:"name"`
			} `json:"originalNetwork"`
			ContentRatings []struct {
				Name    string `json:"name"`
				Country string `json:"country"`
			} `json:"contentRatings"`
			Characters []struct {
				Name       string `json:"name"`
				PersonName string `json:"personName"`
				PeopleType string `json:"peopleType"`
				PeopleID   int    `json:"peopleId"`
				PersonImg  string `json:"personImgURL"`
			} `json:"characters"`
			Artworks []struct {
				Type  int     `json:"type"`
				Image string  `json:"image"`
				Score float64 `json:"score"`
			} `json:"artworks"`
			RemoteIDs []struct {
				ID         string `json:"id"`
				SourceName string `json:"sourceName"`
			} `json:"remoteIds"`
			Seasons []tvdbSeason `json:"seasons"`
		} `json:"data"`
	}
	if err := t.get(ctx, "/series/"+url.PathEscape(id)+"/extended", nil, &s); err != nil {
		return Info{}, nil, err
	}
	d := s.Data
	t.seasons[id] = d.Seasons
	// The record is in the series' original language; a translation
	// replaces what it has.
	info := Info{Name: clean(d.Name), Overview: clean(d.Overview), ProviderIDs: map[string]string{"Tvdb": id}}
	var tr tvdbTranslation
	err = t.get(ctx, "/series/"+url.PathEscape(id)+"/translations/"+t.lang.tvdb, nil, &tr)
	switch {
	case err == nil:
		if name := clean(tr.Data.Name); name != "" && name != info.Name {
			info.OriginalTitle, info.Name = info.Name, name
		}
		if overview := clean(tr.Data.Overview); overview != "" {
			info.Overview = overview
		}
		info.Tagline = clean(tr.Data.Tagline)
	case errors.Is(err, errNotFound):
	default:
		return Info{}, nil, err
	}
	setDate(&info, d.FirstAired)
	genres := []string{}
	for _, g := range d.Genres {
		genres = append(genres, g.Name)
	}
	info.Genres = unique(genres)
	if d.OriginalNetwork != nil {
		info.Studios = unique([]string{d.OriginalNetwork.Name})
	}
	for _, country := range []string{t.lang.tvdbCountry, "usa"} {
		for _, r := range d.ContentRatings {
			if r.Country == country && info.OfficialRating == "" {
				info.OfficialRating = clean(r.Name)
			}
		}
	}
	for _, c := range d.Characters {
		name := clean(c.PersonName)
		if name == "" {
			continue
		}
		person := Person{Name: name, Role: clean(c.Name), Thumb: tvdbImage(c.PersonImg)}
		if c.PeopleID > 0 {
			person.TvdbID = strconv.Itoa(c.PeopleID)
		}
		switch c.PeopleType {
		case "Actor":
			person.Type = "Actor"
		case "Guest Star":
			person.Type = "GuestStar"
		case "Director", "Writer":
			person.Role, person.Type = c.PeopleType, c.PeopleType
		default:
			continue
		}
		info.People = append(info.People, person)
	}
	for _, r := range d.RemoteIDs {
		switch r.SourceName {
		case "IMDB":
			addID(info.ProviderIDs, "imdb", r.ID)
		case "TheMovieDB.com":
			addID(info.ProviderIDs, "tmdb", r.ID)
		}
	}
	backdrop, best := "", -1.0
	for _, a := range d.Artworks {
		if a.Type == tvdbBackground && a.Score > best {
			backdrop, best = a.Image, a.Score
		}
	}
	return info, map[string]string{"Primary": tvdbImage(d.Image), "Backdrop": tvdbImage(backdrop)}, nil
}

type tvdbEpisode struct {
	Number   int    `json:"number"`
	Season   int    `json:"seasonNumber"`
	Name     string `json:"name"`
	Overview string `json:"overview"`
	Aired    string `json:"aired"`
	Image    string `json:"image"`
}

type tvdbSeason struct {
	ID     int    `json:"id"`
	Number int    `json:"number"`
	Image  string `json:"image"`
	Year   string `json:"year"`
	Type   struct {
		Type string `json:"type"`
	} `json:"type"`
	NameTranslations     []string `json:"nameTranslations"`
	OverviewTranslations []string `json:"overviewTranslations"`
}

// season describes a season of the aired order: its translated name and
// overview, its poster and its year. The untranslated name is in the
// series' original language, so it is not used.
func (t *tvdb) season(ctx context.Context, seriesID string, number int) (Info, map[string]string, error) {
	seasons, cached := t.seasons[seriesID]
	if !cached {
		var result struct {
			Data struct {
				Seasons []tvdbSeason `json:"seasons"`
			} `json:"data"`
		}
		err := t.get(ctx, "/series/"+url.PathEscape(seriesID)+"/extended", url.Values{"short": {"true"}}, &result)
		if err != nil && !errors.Is(err, errNotFound) {
			return Info{}, nil, err
		}
		seasons = result.Data.Seasons
		t.seasons[seriesID] = seasons
	}
	i := slices.IndexFunc(seasons, func(s tvdbSeason) bool { return s.Number == number && s.Type.Type == "official" })
	if i < 0 {
		return Info{}, nil, errNotFound
	}
	s := seasons[i]
	info := Info{}
	if year, err := strconv.Atoi(s.Year); err == nil && validYear(year) {
		info.Year = year
	}
	if slices.Contains(s.NameTranslations, t.lang.tvdb) || slices.Contains(s.OverviewTranslations, t.lang.tvdb) {
		var tr tvdbTranslation
		err := t.get(ctx, "/seasons/"+strconv.Itoa(s.ID)+"/translations/"+t.lang.tvdb, nil, &tr)
		if err != nil && !errors.Is(err, errNotFound) {
			return Info{}, nil, err
		}
		info.Name, info.Overview = clean(tr.Data.Name), clean(tr.Data.Overview)
	}
	return info, map[string]string{"Primary": tvdbImage(s.Image)}, nil
}

func (t *tvdb) episode(ctx context.Context, seriesID string, season, episode int) (Info, map[string]string, error) {
	key := seriesID + "/" + strconv.Itoa(season)
	episodes, cached := t.episodes[key]
	if !cached {
		// The translated list falls back to the original language per field.
		for _, path := range []string{"/episodes/default/" + t.lang.tvdb, "/episodes/default"} {
			var result struct {
				Data struct {
					Episodes []tvdbEpisode `json:"episodes"`
				} `json:"data"`
			}
			err := t.get(ctx, "/series/"+url.PathEscape(seriesID)+path, url.Values{"page": {"0"}, "season": {strconv.Itoa(season)}}, &result)
			if errors.Is(err, errNotFound) {
				continue
			}
			if err != nil {
				return Info{}, nil, err
			}
			episodes = result.Data.Episodes
			break
		}
		t.episodes[key] = episodes
	}
	for _, e := range episodes {
		if e.Season != season || e.Number != episode {
			continue
		}
		info := Info{Name: clean(e.Name), Overview: clean(e.Overview)}
		setDate(&info, e.Aired)
		return info, map[string]string{"Primary": tvdbImage(e.Image)}, nil
	}
	return Info{}, nil, errNotFound
}

// person returns a person's biography, in the metadata language or else in
// English, birth date and photo.
func (t *tvdb) person(ctx context.Context, id string) (Info, string, error) {
	var p struct {
		Data struct {
			Image       string `json:"image"`
			Birth       string `json:"birth"`
			Biographies []struct {
				Biography string `json:"biography"`
				Language  string `json:"language"`
			} `json:"biographies"`
		} `json:"data"`
	}
	if err := t.get(ctx, "/people/"+url.PathEscape(id)+"/extended", nil, &p); err != nil {
		return Info{}, "", err
	}
	info := Info{}
	for _, lang := range []string{t.lang.tvdb, "eng"} {
		for _, b := range p.Data.Biographies {
			if b.Language == lang && info.Overview == "" {
				info.Overview = clean(b.Biography)
			}
		}
	}
	setDate(&info, p.Data.Birth)
	return info, tvdbImage(p.Data.Image), nil
}
