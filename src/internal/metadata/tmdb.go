package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Provider addresses; tests point them at a local server.
var (
	tmdbAPI    = "https://api.themoviedb.org/3"
	tmdbImages = "https://image.tmdb.org/t/p/"
)

const (
	// maxCast keeps the people list to the billed cast, as Emby shows it.
	maxCast = 20
)

// tmdb is a TMDb API v3 client. key is a v3 API key or a v4 read access
// token, which is sent as a bearer token instead.
type tmdb struct {
	key  string
	lang language
	pace *throttle
	// seasons caches season lookups within a refresh run.
	seasons map[string]tmdbSeason
}

func newTMDb(key string, lang language) *tmdb {
	return &tmdb{key: key, lang: lang, pace: &throttle{every: spacing.tmdb}, seasons: map[string]tmdbSeason{}}
}

func (t *tmdb) get(ctx context.Context, path string, params url.Values, v any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("language", t.lang.tmdb)
	bearer := strings.HasPrefix(t.key, "eyJ")
	if !bearer {
		params.Set("api_key", t.key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tmdbAPI+path+"?"+params.Encode(), nil)
	if err != nil {
		return errRequest
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	return fetchJSON(req, t.pace, v)
}

type tmdbNamed struct {
	Name string `json:"name"`
}

// tmdbPerson is the person in a credit.
type tmdbPerson struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ProfilePath string `json:"profile_path"`
}

func (p tmdbPerson) credit(role, kind string) Person {
	person := Person{Name: p.Name, Role: role, Type: kind, Thumb: tmdbImage(tmdbProfile, p.ProfilePath)}
	if p.ID > 0 {
		person.TmdbID = strconv.Itoa(p.ID)
	}
	return person
}

type tmdbCredits struct {
	Cast []struct {
		tmdbPerson
		Character string `json:"character"`
	} `json:"cast"`
	Crew []struct {
		tmdbPerson
		Job        string `json:"job"`
		Department string `json:"department"`
	} `json:"crew"`
	GuestStars []struct {
		tmdbPerson
		Character string `json:"character"`
	} `json:"guest_stars"`
}

func (c tmdbCredits) people() []Person {
	people := []Person{}
	for n, p := range c.Cast {
		if n == maxCast {
			break
		}
		people = append(people, p.credit(p.Character, "Actor"))
	}
	for _, p := range c.GuestStars {
		people = append(people, p.credit(p.Character, "GuestStar"))
	}
	for _, p := range c.Crew {
		switch {
		case p.Job == "Director":
			people = append(people, p.credit("Director", "Director"))
		case p.Department == "Writing":
			people = append(people, p.credit(p.Job, "Writer"))
		}
	}
	return people
}

func names(list []tmdbNamed) []string {
	values := make([]string, 0, len(list))
	for _, v := range list {
		values = append(values, v.Name)
	}
	return unique(values)
}

// unique cleans values and drops empty ones and duplicates.
func unique(values []string) []string {
	result := []string{}
	for _, v := range values {
		if v = clean(v); v != "" && !containsFold(result, v) {
			result = append(result, v)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// TMDb image widths Couch downloads: originals reach 4K and several MiB,
// more than any Emby Web screen shows.
const (
	tmdbPoster   = "w780"
	tmdbBackdrop = "w1280"
	tmdbProfile  = "h632"
)

func tmdbImage(size, path string) string {
	if path == "" {
		return ""
	}
	return tmdbImages + size + path
}

// find resolves an IMDb or TheTVDB ID to a TMDb ID of kind "movie" or "tv".
func (t *tmdb) find(ctx context.Context, ids map[string]string, kind string) (string, error) {
	if id := ids["Tmdb"]; id != "" {
		return id, nil
	}
	for _, by := range []struct{ source, key string }{{"imdb_id", "Imdb"}, {"tvdb_id", "Tvdb"}} {
		source, key := by.source, by.key
		if ids[key] == "" || (kind == "movie" && key == "Tvdb") {
			continue
		}
		var found struct {
			Movies []struct {
				ID int `json:"id"`
			} `json:"movie_results"`
			Series []struct {
				ID int `json:"id"`
			} `json:"tv_results"`
		}
		if err := t.get(ctx, "/find/"+url.PathEscape(ids[key]), url.Values{"external_source": {source}}, &found); err != nil {
			return "", err
		}
		if kind == "movie" && len(found.Movies) > 0 {
			return strconv.Itoa(found.Movies[0].ID), nil
		}
		if kind == "tv" && len(found.Series) > 0 {
			return strconv.Itoa(found.Series[0].ID), nil
		}
	}
	return "", errNotFound
}

// search returns the TMDb ID of the best match for a title, trying without
// the year when nothing matches with it. TMDb ranks results by the title in
// the requested language, so an English file name searched in Russian can
// rank a minor title first: a result whose title or original title equals
// the query wins, else the most popular one.
func (t *tmdb) search(ctx context.Context, kind, title string, year int) (string, error) {
	yearKey := "year"
	if kind == "tv" {
		yearKey = "first_air_date_year"
	}
	for _, withYear := range []bool{year > 0, false} {
		params := url.Values{"query": {title}, "include_adult": {"false"}}
		if withYear {
			params.Set(yearKey, strconv.Itoa(year))
		}
		var result struct {
			Results []struct {
				ID            int     `json:"id"`
				Title         string  `json:"title"`
				OriginalTitle string  `json:"original_title"`
				Name          string  `json:"name"`
				OriginalName  string  `json:"original_name"`
				Popularity    float64 `json:"popularity"`
			} `json:"results"`
		}
		if err := t.get(ctx, "/search/"+kind, params, &result); err != nil {
			return "", err
		}
		best := -1
		for n, r := range result.Results {
			if slices.ContainsFunc([]string{r.Title, r.OriginalTitle, r.Name, r.OriginalName}, func(s string) bool { return sameTitle(s, title) }) {
				best = n
				break
			}
			if best < 0 || r.Popularity > result.Results[best].Popularity {
				best = n
			}
		}
		if best >= 0 {
			return strconv.Itoa(result.Results[best].ID), nil
		}
		if !withYear {
			break
		}
	}
	return "", errNotFound
}

// sameTitle compares titles by their letters and digits only, ignoring case.
func sameTitle(a, b string) bool {
	key := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	return a != "" && key(a) == key(b)
}

func (t *tmdb) resolve(ctx context.Context, kind string, ids map[string]string, title string, year int) (string, error) {
	id, err := t.find(ctx, ids, kind)
	if errors.Is(err, errNotFound) && title != "" {
		return t.search(ctx, kind, title, year)
	}
	return id, err
}

func (t *tmdb) movie(ctx context.Context, ids map[string]string, title string, year int) (Info, map[string]string, error) {
	id, err := t.resolve(ctx, "movie", ids, title, year)
	if err != nil {
		return Info{}, nil, err
	}
	var m struct {
		Title         string      `json:"title"`
		OriginalTitle string      `json:"original_title"`
		Overview      string      `json:"overview"`
		Tagline       string      `json:"tagline"`
		ReleaseDate   string      `json:"release_date"`
		VoteAverage   float64     `json:"vote_average"`
		Genres        []tmdbNamed `json:"genres"`
		Companies     []tmdbNamed `json:"production_companies"`
		PosterPath    string      `json:"poster_path"`
		BackdropPath  string      `json:"backdrop_path"`
		IMDbID        string      `json:"imdb_id"`
		Credits       tmdbCredits `json:"credits"`
		Keywords      struct {
			Keywords []tmdbNamed `json:"keywords"`
		} `json:"keywords"`
		ReleaseDates struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Dates   []struct {
					Certification string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	if err := t.get(ctx, "/movie/"+url.PathEscape(id), url.Values{"append_to_response": {"credits,keywords,release_dates"}}, &m); err != nil {
		return Info{}, nil, err
	}
	info := Info{
		Name: clean(m.Title), OriginalTitle: clean(m.OriginalTitle), Overview: clean(m.Overview), Tagline: clean(m.Tagline),
		Community: m.VoteAverage, Genres: names(m.Genres), Studios: names(m.Companies), Tags: names(m.Keywords.Keywords),
		People: m.Credits.people(), ProviderIDs: map[string]string{"Tmdb": id},
	}
	setDate(&info, m.ReleaseDate)
	addID(info.ProviderIDs, "imdb", m.IMDbID)
	for _, country := range []string{t.lang.country, "US"} {
		for _, r := range m.ReleaseDates.Results {
			for _, d := range r.Dates {
				if r.Country == country && info.OfficialRating == "" && d.Certification != "" {
					info.OfficialRating = clean(d.Certification)
				}
			}
		}
	}
	return info, map[string]string{"Primary": tmdbImage(tmdbPoster, m.PosterPath), "Backdrop": tmdbImage(tmdbBackdrop, m.BackdropPath)}, nil
}

func (t *tmdb) series(ctx context.Context, ids map[string]string, title string, year int) (Info, map[string]string, error) {
	id, err := t.resolve(ctx, "tv", ids, title, year)
	if err != nil {
		return Info{}, nil, err
	}
	var s struct {
		Name         string      `json:"name"`
		OriginalName string      `json:"original_name"`
		Overview     string      `json:"overview"`
		Tagline      string      `json:"tagline"`
		FirstAired   string      `json:"first_air_date"`
		VoteAverage  float64     `json:"vote_average"`
		Genres       []tmdbNamed `json:"genres"`
		Networks     []tmdbNamed `json:"networks"`
		PosterPath   string      `json:"poster_path"`
		BackdropPath string      `json:"backdrop_path"`
		Credits      tmdbCredits `json:"credits"`
		Keywords     struct {
			Results []tmdbNamed `json:"results"`
		} `json:"keywords"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
		ExternalIDs struct {
			IMDbID string `json:"imdb_id"`
			TVDbID int    `json:"tvdb_id"`
		} `json:"external_ids"`
	}
	if err := t.get(ctx, "/tv/"+url.PathEscape(id), url.Values{"append_to_response": {"credits,keywords,content_ratings,external_ids"}}, &s); err != nil {
		return Info{}, nil, err
	}
	info := Info{
		Name: clean(s.Name), OriginalTitle: clean(s.OriginalName), Overview: clean(s.Overview), Tagline: clean(s.Tagline),
		Community: s.VoteAverage, Genres: names(s.Genres), Studios: names(s.Networks), Tags: names(s.Keywords.Results),
		People: s.Credits.people(), ProviderIDs: map[string]string{"Tmdb": id},
	}
	setDate(&info, s.FirstAired)
	addID(info.ProviderIDs, "imdb", s.ExternalIDs.IMDbID)
	if s.ExternalIDs.TVDbID > 0 {
		addID(info.ProviderIDs, "tvdb", strconv.Itoa(s.ExternalIDs.TVDbID))
	}
	for _, country := range []string{t.lang.country, "US"} {
		for _, r := range s.ContentRatings.Results {
			if r.Country == country && info.OfficialRating == "" {
				info.OfficialRating = clean(r.Rating)
			}
		}
	}
	return info, map[string]string{"Primary": tmdbImage(tmdbPoster, s.PosterPath), "Backdrop": tmdbImage(tmdbBackdrop, s.BackdropPath)}, nil
}

type tmdbSeason struct {
	// ID is 0 when the series has no such season.
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Overview   string `json:"overview"`
	AirDate    string `json:"air_date"`
	PosterPath string `json:"poster_path"`
	Episodes   []struct {
		Number      int     `json:"episode_number"`
		Name        string  `json:"name"`
		Overview    string  `json:"overview"`
		AirDate     string  `json:"air_date"`
		VoteAverage float64 `json:"vote_average"`
		StillPath   string  `json:"still_path"`
		tmdbCredits
	} `json:"episodes"`
}

// seasonOf returns the season with its episodes, empty when TMDb has none.
func (t *tmdb) seasonOf(ctx context.Context, seriesID string, season int) (tmdbSeason, error) {
	key := seriesID + "/" + strconv.Itoa(season)
	s, cached := t.seasons[key]
	if !cached {
		if err := t.get(ctx, "/tv/"+url.PathEscape(seriesID)+"/season/"+strconv.Itoa(season), nil, &s); err != nil && !errors.Is(err, errNotFound) {
			return tmdbSeason{}, err
		}
		t.seasons[key] = s
	}
	return s, nil
}

func (t *tmdb) season(ctx context.Context, seriesID string, season int) (Info, map[string]string, error) {
	s, err := t.seasonOf(ctx, seriesID, season)
	if err != nil {
		return Info{}, nil, err
	}
	if s.ID == 0 {
		return Info{}, nil, errNotFound
	}
	info := Info{Name: clean(s.Name), Overview: clean(s.Overview)}
	setDate(&info, s.AirDate)
	return info, map[string]string{"Primary": tmdbImage(tmdbPoster, s.PosterPath)}, nil
}

func (t *tmdb) episode(ctx context.Context, seriesID string, season, episode int) (Info, map[string]string, error) {
	s, err := t.seasonOf(ctx, seriesID, season)
	if err != nil {
		return Info{}, nil, err
	}
	for _, e := range s.Episodes {
		if e.Number != episode {
			continue
		}
		info := Info{Name: clean(e.Name), Overview: clean(e.Overview), Community: e.VoteAverage, People: e.people()}
		setDate(&info, e.AirDate)
		return info, map[string]string{"Primary": tmdbImage(tmdbPoster, e.StillPath)}, nil
	}
	return Info{}, nil, errNotFound
}

// person returns a person's biography, birth date and photo.
func (t *tmdb) person(ctx context.Context, id string) (Info, string, error) {
	var p struct {
		Biography   string `json:"biography"`
		Birthday    string `json:"birthday"`
		ProfilePath string `json:"profile_path"`
	}
	if err := t.get(ctx, "/person/"+url.PathEscape(id), nil, &p); err != nil {
		return Info{}, "", err
	}
	info := Info{Overview: clean(p.Biography)}
	setDate(&info, p.Birthday)
	return info, tmdbImage(tmdbProfile, p.ProfilePath), nil
}

func setDate(info *Info, text string) {
	if t, ok := parseDate(text); ok {
		info.Premiere, info.Year = t, t.Year()
	}
}
