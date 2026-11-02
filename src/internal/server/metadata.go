package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
)

// metadataIndex is a published metadata.Library with the genres, studios,
// tags and people it names, by their derived IDs.
type metadataIndex struct {
	library  metadata.Library
	entities map[string]entity
}

// entity is a genre, studio, tag or person: an item page Emby Web opens
// from an item's links, listing the items that name it.
type entity struct {
	Type, Name string
}

// SetMetadata publishes the metadata of the catalogue's items.
func (s *Server) SetMetadata(library metadata.Library) {
	index := &metadataIndex{library: library, entities: map[string]entity{}}
	for _, info := range library {
		for _, kind := range []struct {
			kind   string
			values []string
		}{{"Genre", info.Genres}, {"Studio", info.Studios}, {"Tag", info.Tags}} {
			for _, name := range kind.values {
				index.entities[metadata.ID(kind.kind, name)] = entity{kind.kind, name}
			}
		}
		for _, person := range info.People {
			index.entities[metadata.ID("Person", person.Name)] = entity{"Person", person.Name}
		}
	}
	s.metadata.Store(index)
}

// SetMetadataImages names the directory of downloaded provider images.
// Call it before serving.
func (s *Server) SetMetadataImages(dir string) { s.metadataImages = dir }

var noInfo = &metadata.Info{}

// info returns the item's metadata, a track's or album's from its tags; it
// is empty, never nil, when unknown.
func (s *Server) info(id string) *metadata.Info {
	if index := s.metadata.Load(); index != nil {
		if info := index.library[id]; info != nil {
			return info
		}
	}
	if music := s.music.Load(); music != nil {
		if info := music.info[id]; info != nil {
			return info
		}
	}
	return noInfo
}

func (s *Server) entity(id string) (entity, bool) {
	if index := s.metadata.Load(); index != nil {
		if e, found := index.entities[id]; found {
			return e, true
		}
	}
	if music := s.music.Load(); music != nil {
		if e, found := music.entities[id]; found {
			return e, true
		}
	}
	return entity{}, false
}

// displayName is the item's title: from its metadata, else its file or
// folder name.
func (s *Server) displayName(item media.Item) string {
	if name := s.info(item.ID).Name; name != "" {
		return name
	}
	return item.Name
}

func (s *Server) sortName(item media.Item) string {
	if name := s.info(item.ID).SortName; name != "" {
		return name
	}
	return s.displayName(item)
}

// sortKey orders items by name, and episodes by their numbers first, as
// Emby's episode sort names do: a titled episode keeps its place.
func (s *Server) sortKey(item media.Item) string {
	name := strings.ToLower(s.sortName(item))
	if item.Type() == "Episode" {
		return fmt.Sprintf("%04d %04d %s", item.SeasonNumber, item.EpisodeNumber, name)
	}
	return name
}

func (s *Server) seriesName(item media.Item) string {
	if name := s.info(item.SeriesID).Name; name != "" {
		return name
	}
	return item.SeriesName
}

func (s *Server) seasonName(item media.Item) string {
	if name := s.info(item.SeasonID).Name; name != "" {
		return name
	}
	return "Season " + itoa(item.SeasonNumber)
}

// metadataNames lists an item's genres, studios, tags or people by the
// Emby type of the list.
func metadataNames(info *metadata.Info, kind string) []string {
	switch kind {
	case "Genre":
		return info.Genres
	case "Studio":
		return info.Studios
	case "Tag":
		return info.Tags
	case "Person":
		names := []string{}
		for _, p := range info.People {
			names = append(names, p.Name)
		}
		return names
	case "OfficialRating":
		return nonEmpty(info.OfficialRating)
	case "Year":
		if info.Year > 0 {
			return []string{strconv.Itoa(info.Year)}
		}
	}
	return nil
}

// addMetadata adds the item's descriptive metadata to its DTO in the
// reference Emby's field shapes.
func addMetadata(dto object, info *metadata.Info) {
	if info.OriginalTitle != "" && info.OriginalTitle != dto["Name"] {
		dto["OriginalTitle"] = info.OriginalTitle
	}
	if info.Overview != "" {
		dto["Overview"] = info.Overview
	}
	if info.Tagline != "" {
		dto["Taglines"] = []string{info.Tagline}
	}
	if info.Year > 0 {
		dto["ProductionYear"] = info.Year
	}
	if !info.Premiere.IsZero() {
		dto["PremiereDate"] = embyDate(info.Premiere)
	}
	if info.Community > 0 {
		dto["CommunityRating"] = info.Community
	}
	if info.Critic > 0 {
		dto["CriticRating"] = info.Critic
	}
	if info.OfficialRating != "" {
		dto["OfficialRating"] = info.OfficialRating
	}
	named := func(kind string, values []string) []object {
		items := make([]object, 0, len(values))
		for _, v := range values {
			items = append(items, object{"Name": v, "Id": metadata.ID(kind, v)})
		}
		return items
	}
	dto["Genres"] = append([]string{}, info.Genres...)
	dto["GenreItems"] = named("Genre", info.Genres)
	dto["Studios"] = named("Studio", info.Studios)
	dto["TagItems"] = named("Tag", info.Tags)
	people := make([]object, 0, len(info.People))
	for _, p := range info.People {
		person := object{"Name": p.Name, "Id": metadata.ID("Person", p.Name), "Type": p.Type}
		if p.Role != "" {
			person["Role"] = p.Role
		}
		people = append(people, person)
	}
	dto["People"] = people
	tmdbPath := "movie/"
	if dto["Type"] == "Series" {
		tmdbPath = "tv/"
	}
	ids, urls := object{}, []object{}
	for _, provider := range []struct{ key, name, url string }{
		{"Imdb", "IMDb", "https://www.imdb.com/title/"},
		{"Tmdb", "TheMovieDb", "https://www.themoviedb.org/" + tmdbPath},
		{"Tvdb", "TheTVDB", "https://thetvdb.com/?tab=series&id="},
	} {
		id := info.ProviderIDs[provider.key]
		if id == "" {
			continue
		}
		ids[provider.key] = id
		if dto["Type"] != "Episode" {
			urls = append(urls, object{"Name": provider.name, "Url": provider.url + id})
		}
	}
	dto["ProviderIds"] = ids
	dto["ExternalUrls"] = urls
}

// entityDTO is the item page of a genre, studio, tag, person or artist.
func entityDTO(id string, e entity, serverID string) object {
	dto := baseFields(id, e.Name, e.Type, serverID)
	dto["DateCreated"] = zeroDate
	dto["IsFolder"] = e.Type != "Person"
	dto["UserData"] = folderUserData()
	return dto
}

// imageInfos reports the item's images by type: local files first, then
// images downloaded from a provider.
func (s *Server) imageInfos(catalog *media.Catalog, item media.Item) map[string]media.Image {
	images := catalog.ImageInfos(item)
	for kind, image := range s.info(item.ID).Images {
		if _, local := images[kind]; !local {
			images[kind] = media.Image{Revision: image.Revision(), Width: image.Width, Height: image.Height, MIME: image.MIME}
		}
	}
	return images
}

// openImage opens the item's image of a type, as imageInfos chooses it.
func (s *Server) openImage(catalog *media.Catalog, item media.Item, kind string) (*os.File, media.Image, error) {
	file, info, err := catalog.OpenImage(item, kind)
	if err == nil || s.metadataImages == "" {
		return file, info, err
	}
	for name, image := range s.info(item.ID).Images {
		if !strings.EqualFold(name, kind) || filepath.Base(image.File) != image.File {
			continue
		}
		file, err := os.Open(filepath.Join(s.metadataImages, image.File))
		if err != nil {
			break
		}
		return file, media.Image{Revision: image.Revision(), Width: image.Width, Height: image.Height, MIME: image.MIME}, nil
	}
	return nil, media.Image{}, os.ErrNotExist
}
