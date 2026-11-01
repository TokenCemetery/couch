// Package metadata reads descriptive metadata for catalogue items from local
// .nfo files and, when their API keys are set, from TMDb and TheTVDB.
package metadata

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"time"
)

// Info is what Couch knows about an item beyond its file. Zero values mean
// unknown.
type Info struct {
	Name           string            `json:",omitempty"`
	OriginalTitle  string            `json:",omitempty"`
	SortName       string            `json:",omitempty"`
	Overview       string            `json:",omitempty"`
	Tagline        string            `json:",omitempty"`
	Year           int               `json:",omitempty"`
	Premiere       time.Time         `json:",omitzero"`
	Genres         []string          `json:",omitempty"`
	Studios        []string          `json:",omitempty"`
	Tags           []string          `json:",omitempty"`
	Community      float64           `json:",omitempty"` // 0–10
	Critic         float64           `json:",omitempty"` // 0–100
	OfficialRating string            `json:",omitempty"`
	People         []Person          `json:",omitempty"`
	ProviderIDs    map[string]string `json:",omitempty"` // Tmdb, Tvdb, Imdb
	// Images are downloaded provider images by Emby image type.
	Images map[string]Image `json:",omitempty"`
}

// Person is a cast or crew member. Type is Actor, GuestStar, Director or
// Writer, as Emby names them.
type Person struct {
	Name string
	Role string `json:",omitempty"`
	Type string
}

// Image is a provider image saved under the data directory.
type Image struct {
	URL           string
	File          string // name in the image directory
	Width, Height int
	MIME          string
}

// Revision changes whenever the image does.
func (i Image) Revision() string { return ID("image", i.URL) }

// ID derives the stable ID of a genre, studio, tag or person from its name.
func ID(kind, name string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.ToLower(strings.TrimSpace(name))))
	return hex.EncodeToString(sum[:16])
}

// merge fills the fields of primary that are unknown from fallback, as Emby
// does with its metadata sources in order.
func merge(primary, fallback Info) Info {
	text := func(a *string, b string) {
		if *a == "" {
			*a = b
		}
	}
	text(&primary.Name, fallback.Name)
	text(&primary.OriginalTitle, fallback.OriginalTitle)
	text(&primary.SortName, fallback.SortName)
	text(&primary.Overview, fallback.Overview)
	text(&primary.Tagline, fallback.Tagline)
	text(&primary.OfficialRating, fallback.OfficialRating)
	if primary.Year == 0 {
		primary.Year = fallback.Year
	}
	if primary.Premiere.IsZero() {
		primary.Premiere = fallback.Premiere
	}
	if primary.Community == 0 {
		primary.Community = fallback.Community
	}
	if primary.Critic == 0 {
		primary.Critic = fallback.Critic
	}
	if len(primary.Genres) == 0 {
		primary.Genres = fallback.Genres
	}
	if len(primary.Studios) == 0 {
		primary.Studios = fallback.Studios
	}
	if len(primary.Tags) == 0 {
		primary.Tags = fallback.Tags
	}
	if len(primary.People) == 0 {
		primary.People = fallback.People
	}
	primary.ProviderIDs = union(fallback.ProviderIDs, primary.ProviderIDs)
	primary.Images = union(fallback.Images, primary.Images)
	return primary
}

// union returns the entries of both maps, b's winning; nil when both are empty.
func union[V any](a, b map[string]V) map[string]V {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	m := maps.Clone(a)
	maps.Copy(m, b)
	return m
}

// Directors lists the item's directors, in order.
func (i *Info) Directors() []string {
	names := []string{}
	for _, p := range i.People {
		if p.Type == "Director" && !slices.Contains(names, p.Name) {
			names = append(names, p.Name)
		}
	}
	return names
}

// Library is the published metadata of a catalogue by item ID. It is not
// changed after it is published.
type Library map[string]*Info
