package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
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

// SetMetadata publishes the metadata of the catalogue's items and brings
// the collections of movie sets in line with it.
func (s *Server) SetMetadata(library metadata.Library) {
	index := &metadataIndex{library: library, entities: map[string]entity{}}
	sets := make(map[string]state.MovieSet, len(library))
	for id, info := range library {
		sets[id] = state.MovieSet{Name: info.Set, TmdbID: info.SetTmdbID, Pending: info.Pending}
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
	created, err := s.store.SyncSets(sets)
	if err != nil {
		slog.Warn("Movie set collections not updated", "error", err)
	}
	for _, c := range created {
		s.record(state.Activity{Type: "CollectionCreated", Name: "Collection " + c.Name + " created", Severity: "Info", ItemID: c.ID})
	}
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

func (s *Server) seriesSortName(item media.Item) string {
	if name := s.info(item.SeriesID).SortName; name != "" {
		return name
	}
	return s.seriesName(item)
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
// reference Emby's field shapes; the cast and crew only with people.
func (s *Server) addMetadata(dto object, info *metadata.Info, token string, people bool) {
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
	// Emby sends these IDs as integers (NameLongIdPair, #121).
	named := func(kind string, values []string) []object {
		items := make([]object, 0, len(values))
		for _, v := range values {
			items = append(items, object{"Name": v, "Id": json.Number(metadata.ID(kind, v))})
		}
		return items
	}
	dto["Genres"] = append([]string{}, info.Genres...)
	dto["GenreItems"] = named("Genre", info.Genres)
	dto["Studios"] = named("Studio", info.Studios)
	dto["TagItems"] = named("Tag", info.Tags)
	if people {
		list := make([]object, 0, len(info.People))
		for _, p := range info.People {
			id := metadata.ID("Person", p.Name)
			person := object{"Name": p.Name, "Id": id, "Type": p.Type}
			if p.Role != "" {
				person["Role"] = p.Role
			}
			if tag := s.personImageTag(token, id); tag != "" {
				person["PrimaryImageTag"] = tag
			}
			list = append(list, person)
		}
		dto["People"] = list
	}
	tmdbPath := "movie/"
	if dto["Type"] == "Series" {
		tmdbPath = "tv/"
	}
	ids, urls := object{}, []object{}
	for _, provider := range []struct{ key, name, url string }{
		{"Imdb", "IMDb", "https://www.imdb.com/title/"},
		{"Tmdb", "TheMovieDb", "https://www.themoviedb.org/" + tmdbPath},
		{"Tvdb", "TheTVDB", "https://thetvdb.com/?tab=series&id="},
		{"MusicBrainzReleaseGroup", "MusicBrainz Release Group", "https://musicbrainz.org/release-group/"},
		{"AudioDbAlbum", "TheAudioDb Album", "https://www.theaudiodb.com/album/"},
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

// addPerson adds what the providers know of a person or music artist to
// their item page: biography, birth date, photo and an artist's fanart.
func (s *Server) addPerson(dto object, id, token string) {
	info := s.info(id)
	if info.Overview != "" {
		dto["Overview"] = info.Overview
	}
	if !info.Premiere.IsZero() {
		dto["PremiereDate"] = embyDate(info.Premiere)
		dto["ProductionYear"] = info.Year
	}
	if len(info.ProviderIDs) > 0 {
		ids := map[string]string{}
		maps.Copy(ids, info.ProviderIDs)
		dto["ProviderIds"] = ids
	}
	if tag := s.personImageTag(token, id); tag != "" {
		dto["ImageTags"] = object{"Primary": tag}
	}
	if image, found := info.Images["Backdrop"]; found {
		if tag := s.store.ImageTag(token, id, image.Revision()); tag != "" {
			dto["BackdropImageTags"] = []string{tag}
		}
	}
}

// personImageTag is the ImageTag of a person's or artist's photo, or ""
// without one.
func (s *Server) personImageTag(token, id string) string {
	if image, found := s.info(id).Images["Primary"]; found {
		return s.store.ImageTag(token, id, image.Revision())
	}
	return ""
}

// personTypes is Emby's PersonType order, which credit tabs follow.
var personTypes = []string{"Actor", "Director", "Writer", "Producer", "GuestStar", "Composer", "Conductor", "Lyricist"}

// personCredits answers Persons/{Id}/Credits: the person's filmography by
// credit type, for the person page's credit tabs. Emby fills it from its
// metadata provider; Couch lists the items of the user's libraries that
// credit the person. An episode credits its series, once per type.
func (s *Server) personCredits(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	catalog := s.catalogFor(session)
	if catalog == nil {
		fail(w, 404, "NotFound")
		return
	}
	id := r.PathValue("item")
	if e, found := s.entityIn(catalog, id); !found || e.Type != "Person" {
		fail(w, 404, "NotFound")
		return
	}
	type credit struct {
		item   media.Item
		person metadata.Person
	}
	byType := map[string][]credit{}
	seen := map[string]bool{}
	// Folders first, so a series' own credit wins over its episodes'.
	for _, items := range [][]media.Item{catalog.Folders, catalog.Items} {
		for _, item := range items {
			for _, person := range s.info(item.ID).People {
				if metadata.ID("Person", person.Name) != id {
					continue
				}
				credited := item
				if item.Type() == "Episode" {
					if series, found := findItem(catalog, item.SeriesID); found {
						credited = series
					}
				}
				if key := person.Type + "\x00" + credited.ID; !seen[key] {
					seen[key] = true
					byType[person.Type] = append(byType[person.Type], credit{credited, person})
				}
			}
		}
	}
	// Newest first, as a filmography reads; undated items last.
	released := func(item media.Item) time.Time {
		info := s.info(item.ID)
		if !info.Premiere.IsZero() || info.Year == 0 {
			return info.Premiere
		}
		return time.Date(info.Year, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	types := slices.Sorted(maps.Keys(byType))
	slices.SortStableFunc(types, func(a, b string) int {
		rank := func(t string) int {
			if i := slices.Index(personTypes, t); i >= 0 {
				return i
			}
			return len(personTypes)
		}
		return rank(a) - rank(b)
	})
	lists := []object{}
	for _, personType := range types {
		credits := byType[personType]
		slices.SortStableFunc(credits, func(a, b credit) int {
			if c := released(b.item).Compare(released(a.item)); c != 0 {
				return c
			}
			return strings.Compare(s.sortKey(a.item), s.sortKey(b.item))
		})
		items := make([]object, 0, len(credits))
		for _, c := range credits {
			info := s.info(c.item.ID)
			ids := map[string]string{}
			maps.Copy(ids, info.ProviderIDs)
			dto := object{"Name": s.displayName(c.item), "Type": c.item.Type(), "PersonType": personType, "ProviderIds": ids}
			if c.person.Role != "" {
				dto["Role"] = c.person.Role
			}
			if info.OriginalTitle != "" && info.OriginalTitle != dto["Name"] {
				dto["OriginalTitle"] = info.OriginalTitle
			}
			if info.Year > 0 {
				dto["ProductionYear"] = info.Year
			}
			if !info.Premiere.IsZero() {
				dto["PremiereDate"] = embyDate(info.Premiere)
			}
			if info.Overview != "" {
				dto["Overview"] = info.Overview
			}
			items = append(items, dto)
		}
		lists = append(lists, object{"PersonType": personType, "Items": items})
	}
	respond(w, 200, lists)
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
func (s *Server) openImage(ctx context.Context, catalog *media.Catalog, item media.Item, kind string) (*os.File, media.Image, error) {
	file, info, err := catalog.OpenImage(item, kind)
	if err == nil {
		return file, info, nil
	}
	if strings.EqualFold(kind, "Primary") {
		if file, info, err := s.embeddedCover(ctx, catalog, item); err == nil {
			return file, info, nil
		}
	}
	if file, info, err := s.openProviderImage(item.ID, kind); err == nil {
		return file, info, nil
	}
	return nil, media.Image{}, err
}

// openProviderImage opens an item's or a person's image of a type that a
// provider supplied.
func (s *Server) openProviderImage(id, kind string) (*os.File, media.Image, error) {
	if s.metadataImages == "" {
		return nil, media.Image{}, os.ErrNotExist
	}
	for name, image := range s.info(id).Images {
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
