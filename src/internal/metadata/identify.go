package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrNoProvider is returned by Search when neither movie and series
// provider has a key.
var ErrNoProvider = errors.New("no online metadata provider is configured")

// maxResults caps the matches Search returns per provider.
const maxResults = 10

// SearchResult is a match a provider found for Identify.
type SearchResult struct {
	Name        string
	Year        int
	Overview    string
	ProviderIDs map[string]string
	// ImageURL is the provider's poster, empty when it has none.
	ImageURL string
	// Provider names the provider that found it.
	Provider string
}

// Search looks a movie or series up for Identify: by its provider IDs when
// any are given, else by name and year. Series are searched with TheTVDB
// before TMDb, as the refresher asks them; movies with TMDb only.
func (r *Refresher) Search(ctx context.Context, kind, name string, year int, ids map[string]string) ([]SearchResult, error) {
	if r.cfg.TMDbKey == "" && r.cfg.TVDbKey == "" {
		return nil, ErrNoProvider
	}
	results := []SearchResult{}
	add := func(found []SearchResult, err error) error {
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		results = append(results, found...)
		return nil
	}
	byID := idText(ids) != ""
	if kind == "Series" && r.cfg.TVDbKey != "" {
		tv := newTVDb(r.cfg.TVDbKey, r.lang)
		if byID && ids["Tvdb"] != "" {
			if err := add(detail("TheTVDB", func() (Info, map[string]string, error) { return tv.series(ctx, ids, "", 0) })); err != nil {
				return nil, err
			}
		} else if !byID {
			if err := add(tv.searchList(ctx, name, year)); err != nil {
				return nil, err
			}
		}
	}
	if r.cfg.TMDbKey != "" && (kind == "Movie" || kind == "Series") {
		tm := newTMDb(r.cfg.TMDbKey, r.lang)
		fetch := func() (Info, map[string]string, error) { return tm.movie(ctx, ids, "", 0) }
		path := "movie"
		if kind == "Series" {
			fetch = func() (Info, map[string]string, error) { return tm.series(ctx, ids, "", 0) }
			path = "tv"
		}
		var err error
		if byID {
			err = add(detail("TheMovieDb", fetch))
		} else {
			err = add(tm.searchList(ctx, path, name, year))
		}
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// detail turns a provider's record of an item into a search result.
func detail(provider string, fetch func() (Info, map[string]string, error)) ([]SearchResult, error) {
	info, images, err := fetch()
	if err != nil {
		return nil, err
	}
	return []SearchResult{{Name: info.Name, Year: info.Year, Overview: info.Overview, ProviderIDs: info.ProviderIDs, ImageURL: images["Primary"], Provider: provider}}, nil
}

// searchList returns TMDb's matches for a title, kind being "movie" or "tv".
func (t *tmdb) searchList(ctx context.Context, kind, title string, year int) ([]SearchResult, error) {
	params := url.Values{"query": {title}, "include_adult": {"false"}}
	if year > 0 {
		key := "year"
		if kind == "tv" {
			key = "first_air_date_year"
		}
		params.Set(key, strconv.Itoa(year))
	}
	var found struct {
		Results []struct {
			ID           int    `json:"id"`
			Title        string `json:"title"`
			Name         string `json:"name"`
			ReleaseDate  string `json:"release_date"`
			FirstAirDate string `json:"first_air_date"`
			Overview     string `json:"overview"`
			PosterPath   string `json:"poster_path"`
		} `json:"results"`
	}
	if err := t.get(ctx, "/search/"+kind, params, &found); err != nil {
		return nil, err
	}
	results := []SearchResult{}
	for _, r := range found.Results[:min(maxResults, len(found.Results))] {
		info := Info{Name: clean(r.Title + r.Name)}
		setDate(&info, r.ReleaseDate+r.FirstAirDate)
		results = append(results, SearchResult{Name: info.Name, Year: info.Year, Overview: clean(r.Overview),
			ProviderIDs: map[string]string{"Tmdb": strconv.Itoa(r.ID)}, ImageURL: tmdbImage(tmdbPoster, r.PosterPath), Provider: "TheMovieDb"})
	}
	return results, nil
}

// searchList returns TheTVDB's series matches for a title.
func (t *tvdb) searchList(ctx context.Context, title string, year int) ([]SearchResult, error) {
	params := url.Values{"query": {title}, "type": {"series"}, "limit": {strconv.Itoa(maxResults)}}
	if year > 0 {
		params.Set("year", strconv.Itoa(year))
	}
	var found struct {
		Data []struct {
			TVDbID   string `json:"tvdb_id"`
			Name     string `json:"name"`
			Year     string `json:"year"`
			Overview string `json:"overview"`
			ImageURL string `json:"image_url"`
		} `json:"data"`
	}
	if err := t.get(ctx, "/search", params, &found); err != nil {
		return nil, err
	}
	results := []SearchResult{}
	for _, r := range found.Data[:min(maxResults, len(found.Data))] {
		if !digits.MatchString(r.TVDbID) {
			continue
		}
		year, _ := strconv.Atoi(r.Year)
		results = append(results, SearchResult{Name: clean(r.Name), Year: year, Overview: clean(r.Overview),
			ProviderIDs: map[string]string{"Tvdb": r.TVDbID}, ImageURL: tvdbImage(r.ImageURL), Provider: "TheTVDB"})
	}
	return results, nil
}

// SearchImage fetches a search result's poster for the Identify dialog.
// Only images on the providers' image hosts are fetched, and only a JPEG or
// PNG within the download limit is returned.
func SearchImage(ctx context.Context, target string) ([]byte, string, error) {
	if !strings.HasPrefix(target, tmdbImages) && !strings.HasPrefix(target, tvdbArtwork+"/") {
		return nil, "", errNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", errRequest
	}
	body, err := fetch(req, &throttle{every: spacing.images}, maxImageBytes)
	if err != nil {
		return nil, "", err
	}
	_, format, err := decodeConfig(body)
	if err != nil {
		return nil, "", err
	}
	return body, "image/" + format, nil
}
