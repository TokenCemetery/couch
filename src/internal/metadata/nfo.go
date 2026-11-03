package metadata

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxNFOBytes bounds a .nfo file; real ones are a few KiB.
const maxNFOBytes = 1 << 20

// nfo is the Kodi/Emby .nfo element set Couch reads. Root is movie, tvshow,
// season or episodedetails.
type nfo struct {
	XMLName       xml.Name
	Title         string      `xml:"title"`
	OriginalTitle string      `xml:"originaltitle"`
	SortTitle     string      `xml:"sorttitle"`
	Plot          string      `xml:"plot"`
	Outline       string      `xml:"outline"`
	Tagline       string      `xml:"tagline"`
	Year          string      `xml:"year"`
	Premiered     string      `xml:"premiered"`
	Aired         string      `xml:"aired"`
	ReleaseDate   string      `xml:"releasedate"`
	Rating        string      `xml:"rating"`
	Ratings       []nfoRating `xml:"ratings>rating"`
	CriticRating  string      `xml:"criticrating"`
	MPAA          string      `xml:"mpaa"`
	Genres        []string    `xml:"genre"`
	Studios       []string    `xml:"studio"`
	Tags          []string    `xml:"tag"`
	Directors     []string    `xml:"director"`
	Credits       []string    `xml:"credits"`
	Actors        []struct {
		Name  string `xml:"name"`
		Role  string `xml:"role"`
		Thumb string `xml:"thumb"`
	} `xml:"actor"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
	ID     string `xml:"id"`
	TMDbID string `xml:"tmdbid"`
	IMDbID string `xml:"imdbid"`
	TVDbID string `xml:"tvdbid"`
}

type nfoRating struct {
	Name    string `xml:"name,attr"`
	Default bool   `xml:"default,attr"`
	Value   string `xml:"value"`
}

var (
	imdbURL = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
	tmdbURL = regexp.MustCompile(`themoviedb\.org/(?:movie|tv)/(\d+)`)
	imdbID  = regexp.MustCompile(`^tt\d+$`)
	digits  = regexp.MustCompile(`^\d+$`)
)

// parseNFO reads a .nfo file. Kodi also accepts a file that holds only a
// provider URL, or XML followed by one; the URL then supplies the provider ID.
func parseNFO(content []byte) (Info, error) {
	if len(content) > maxNFOBytes {
		return Info{}, errors.New("nfo file is too large")
	}
	var n nfo
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = false
	// Files that declare another encoding are read as UTF-8 rather than rejected.
	decoder.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	xmlErr := decoder.Decode(&n)
	linked := map[string]string{}
	if m := imdbURL.FindSubmatch(content); m != nil {
		linked["Imdb"] = string(m[1])
	}
	if m := tmdbURL.FindSubmatch(content); m != nil {
		linked["Tmdb"] = string(m[1])
	}
	if xmlErr != nil {
		if len(linked) == 0 {
			return Info{}, errors.New("nfo file is not readable")
		}
		return Info{ProviderIDs: linked}, nil
	}
	switch n.XMLName.Local {
	case "movie", "tvshow", "season", "episodedetails":
	default:
		return Info{}, errors.New("nfo file has an unknown root element")
	}
	info := Info{
		Name:           clean(n.Title),
		OriginalTitle:  clean(n.OriginalTitle),
		SortName:       clean(n.SortTitle),
		Overview:       clean(n.Plot),
		Tagline:        clean(n.Tagline),
		OfficialRating: clean(n.MPAA),
		Genres:         list(n.Genres),
		Studios:        list(n.Studios),
		Tags:           list(n.Tags),
	}
	if info.Overview == "" {
		info.Overview = clean(n.Outline)
	}
	for _, text := range []string{n.Premiered, n.Aired, n.ReleaseDate} {
		if t, ok := parseDate(text); ok {
			info.Premiere = t
			break
		}
	}
	if y, err := strconv.Atoi(strings.TrimSpace(n.Year)); err == nil && validYear(y) {
		info.Year = y
	} else if !info.Premiere.IsZero() {
		info.Year = info.Premiere.Year()
	}
	info.Community = rating(n)
	if v, err := strconv.ParseFloat(strings.TrimSpace(n.CriticRating), 64); err == nil && v > 0 && v <= 100 {
		info.Critic = v
	}
	for _, a := range n.Actors {
		if name := clean(a.Name); name != "" {
			info.People = append(info.People, Person{Name: name, Role: clean(a.Role), Type: "Actor", Thumb: clean(a.Thumb)})
		}
	}
	for _, name := range list(n.Directors) {
		info.People = append(info.People, Person{Name: name, Role: "Director", Type: "Director"})
	}
	for _, name := range list(n.Credits) {
		info.People = append(info.People, Person{Name: name, Role: "Writer", Type: "Writer"})
	}
	ids := map[string]string{}
	for _, u := range n.UniqueIDs {
		addID(ids, u.Type, u.Value)
	}
	addID(ids, "tmdb", n.TMDbID)
	addID(ids, "imdb", n.IMDbID)
	addID(ids, "tvdb", n.TVDbID)
	// A bare <id> is the IMDb ID in movie files and the TheTVDB ID in
	// series files written by Kodi.
	switch id := strings.TrimSpace(n.ID); {
	case imdbID.MatchString(id):
		addID(ids, "imdb", id)
	case n.XMLName.Local == "tvshow":
		addID(ids, "tvdb", id)
	}
	// IDs in the XML win over a link after it.
	for kind, id := range linked {
		addID(ids, kind, id)
	}
	if len(ids) > 0 {
		info.ProviderIDs = ids
	}
	return info, nil
}

// addID records a valid provider ID unless one of its kind is known.
func addID(ids map[string]string, kind, value string) {
	value = strings.TrimSpace(value)
	key := map[string]string{"tmdb": "Tmdb", "imdb": "Imdb", "tvdb": "Tvdb"}[strings.ToLower(strings.TrimSpace(kind))]
	if key == "" || ids[key] != "" {
		return
	}
	if (key == "Imdb" && imdbID.MatchString(value)) || (key != "Imdb" && digits.MatchString(value) && value != "0") {
		ids[key] = value
	}
}

// rating returns the community rating: the default of <ratings>, its first
// entry, or the older single <rating>.
func rating(n nfo) float64 {
	texts := []string{}
	for _, r := range n.Ratings {
		if r.Default {
			texts = append([]string{r.Value}, texts...)
		} else {
			texts = append(texts, r.Value)
		}
	}
	texts = append(texts, n.Rating)
	for _, text := range texts {
		if v, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil && v > 0 && v <= 10 {
			return v
		}
	}
	return 0
}

func parseDate(text string) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if len(text) >= 10 {
		if t, err := time.Parse(time.DateOnly, text[:10]); err == nil && validYear(t.Year()) {
			return t, true
		}
	}
	return time.Time{}, false
}

func validYear(y int) bool { return y >= 1800 && y <= 2200 }

// clean trims text and drops control characters other than line breaks.
func clean(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text))
}

// list cleans values, splits Kodi's " / " joined values and drops empty
// ones and duplicates.
func list(values []string) []string {
	result := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, " / ") {
			part = clean(part)
			if part != "" && !containsFold(result, part) {
				result = append(result, part)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func containsFold(list []string, value string) bool {
	for _, v := range list {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}
