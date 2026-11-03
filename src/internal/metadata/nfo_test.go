package metadata

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestParseNFO(t *testing.T) {
	movie := `<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>
<movie>
  <title>The Movie</title>
  <originaltitle>Le Film</originaltitle>
  <sorttitle>Movie, The</sorttitle>
  <outline>Short.</outline>
  <plot>Long plot.</plot>
  <tagline>One line.</tagline>
  <ratings>
    <rating name="imdb" max="10"><value>6.1</value></rating>
    <rating name="themoviedb" max="10" default="true"><value>7.5</value></rating>
  </ratings>
  <criticrating>85</criticrating>
  <mpaa>PG-13</mpaa>
  <uniqueid type="imdb" default="true">tt0111161</uniqueid>
  <uniqueid type="tmdb">278</uniqueid>
  <genre>Drama / Crime</genre>
  <genre>drama</genre>
  <studio>Castle Rock</studio>
  <tag>prison</tag>
  <director>Frank Darabont</director>
  <credits>Stephen King</credits>
  <premiered>1994-09-23</premiered>
  <actor><name>Tim Robbins</name><role>Andy</role><thumb>https://image.tmdb.org/t/p/original/tim.jpg</thumb></actor>
</movie>
https://www.themoviedb.org/movie/999`
	got, err := parseNFO([]byte(movie))
	if err != nil {
		t.Fatal(err)
	}
	want := Info{
		Name: "The Movie", OriginalTitle: "Le Film", SortName: "Movie, The", Overview: "Long plot.", Tagline: "One line.",
		Year: 1994, Premiere: time.Date(1994, 9, 23, 0, 0, 0, 0, time.UTC),
		Genres: []string{"Drama", "Crime"}, Studios: []string{"Castle Rock"}, Tags: []string{"prison"},
		Community: 7.5, Critic: 85, OfficialRating: "PG-13",
		People: []Person{{Name: "Tim Robbins", Role: "Andy", Type: "Actor", Thumb: "https://image.tmdb.org/t/p/original/tim.jpg"},
			{Name: "Frank Darabont", Role: "Director", Type: "Director"}, {Name: "Stephen King", Role: "Writer", Type: "Writer"}},
		// The <uniqueid> wins over the trailing URL.
		ProviderIDs: map[string]string{"Imdb": "tt0111161", "Tmdb": "278"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("movie:\n got %+v\nwant %+v", got, want)
	}

	show, err := parseNFO([]byte(`<tvshow><title>Show</title><id>81189</id><year>2008</year><rating>9</rating></tvshow>`))
	if err != nil || show.Name != "Show" || show.Year != 2008 || show.Community != 9 || show.ProviderIDs["Tvdb"] != "81189" {
		t.Fatalf("tvshow: %+v %v", show, err)
	}
	link, err := parseNFO([]byte("https://www.imdb.com/title/tt0068646/\n"))
	if err != nil || !reflect.DeepEqual(link, Info{ProviderIDs: map[string]string{"Imdb": "tt0068646"}}) {
		t.Fatalf("link: %+v %v", link, err)
	}
	for _, bad := range []string{"", "not xml", "<musicvideo><title>x</title></musicvideo>", string(make([]byte, maxNFOBytes+1))} {
		if _, err := parseNFO([]byte(bad)); err == nil {
			t.Fatalf("accepted %.40q", bad)
		}
	}
}

func TestLookupName(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		year        int
	}{
		{"The.Movie.2010.1080p.BluRay.x264", "The Movie", 2010},
		{"Blade Runner 2049 (2017)", "Blade Runner 2049", 2017},
		{"1917", "1917", 0},
		{"2001 A Space Odyssey (1968) [Remastered]", "2001 A Space Odyssey", 1968},
		{"Some_Show", "Some Show", 0},
		{"Movie Name 720p", "Movie Name", 0},
		{"Mr. Robot", "Mr. Robot", 0},
	} {
		title, year := lookupName(tc.name)
		if title != tc.title || year != tc.year {
			t.Errorf("%q: %q %d, want %q %d", tc.name, title, year, tc.title, tc.year)
		}
	}
}

// TestIDIsAnInt64 checks that derived IDs are decimal numbers that fit the
// int64 Emby clients read genre, studio and tag IDs as (#121), and ignore
// case and surrounding spaces.
func TestIDIsAnInt64(t *testing.T) {
	id := ID("Genre", "Rock")
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n < 0 || strconv.FormatInt(n, 10) != id {
		t.Fatalf("ID %q is not a decimal int64: %v", id, err)
	}
	if ID("Genre", " rock ") != id || ID("Studio", "Rock") == id {
		t.Fatal("IDs differ by case or agree across kinds")
	}
}
