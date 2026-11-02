package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

// musicIndex is what a catalogue's tracks and albums tell beyond their
// files: their year and genres from the tags, and the artists and genres
// they name, by their derived IDs.
type musicIndex struct {
	info     map[string]*metadata.Info
	entities map[string]entity
}

// newMusicIndex indexes the music of catalog, which may be nil.
func newMusicIndex(catalog *media.Catalog) *musicIndex {
	index := &musicIndex{info: map[string]*metadata.Info{}, entities: map[string]entity{}}
	if catalog == nil {
		return index
	}
	for _, items := range [][]media.Item{catalog.Items, catalog.Folders} {
		for _, item := range items {
			if !isMusic(item) {
				continue
			}
			index.info[item.ID] = &metadata.Info{Year: item.Tags.Year, Genres: item.Tags.Genres}
			for _, genre := range item.Tags.Genres {
				index.entities[metadata.ID("Genre", genre)] = entity{"Genre", genre}
			}
			for _, artist := range artistsOf(catalog, item, "Artist,AlbumArtist") {
				index.entities[metadata.ID("MusicArtist", artist)] = entity{"MusicArtist", artist}
			}
		}
	}
	return index
}

func isMusic(item media.Item) bool { return item.Type() == "Audio" || item.Type() == "MusicAlbum" }

// albumArtist returns the album artist of a track or album: its album's,
// or for a track without an album, its first artist.
func albumArtist(catalog *media.Catalog, item media.Item) string {
	if item.Type() == "Audio" {
		if album, ok := catalog.Folder(item.ParentID); ok {
			return album.Tags.AlbumArtist
		}
		if len(item.Tags.Artists) > 0 {
			return item.Tags.Artists[0]
		}
	}
	return item.Tags.AlbumArtist
}

// artistsOf lists the artists of a track or album by Emby's ArtistType:
// "AlbumArtist" names the album artist, "Artist" those who perform on it.
func artistsOf(catalog *media.Catalog, item media.Item, types string) []string {
	names := []string{}
	add := func(name string) {
		if name != "" && !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
			names = append(names, name)
		}
	}
	if member(types, "AlbumArtist") {
		add(albumArtist(catalog, item))
	}
	if member(types, "Artist") {
		for _, artist := range item.Tags.Artists {
			add(artist)
		}
	}
	return names
}

// namedItems lists names as Emby's NameIdPair items of a type.
func namedItems(kind string, names []string) []object {
	items := make([]object, 0, len(names))
	for _, name := range names {
		items = append(items, object{"Name": name, "Id": metadata.ID(kind, name)})
	}
	return items
}

// addMusic adds the album and artist fields of a track or album to its DTO.
func (s *Server) addMusic(dto object, catalog *media.Catalog, item media.Item, token string) {
	artists := artistsOf(catalog, item, "Artist")
	if len(artists) == 0 {
		artists = artistsOf(catalog, item, "AlbumArtist")
	}
	dto["Artists"] = artists
	dto["ArtistItems"] = namedItems("MusicArtist", artists)
	if name := albumArtist(catalog, item); name != "" {
		dto["AlbumArtist"] = name
		dto["AlbumArtists"] = namedItems("MusicArtist", []string{name})
	} else {
		dto["AlbumArtists"] = []object{}
	}
	if item.Type() == "MusicAlbum" {
		if item.RunTimeTicks > 0 {
			dto["RunTimeTicks"] = item.RunTimeTicks
		}
		return
	}
	if item.Tags.Track > 0 {
		dto["IndexNumber"] = item.Tags.Track
	}
	if item.Tags.Disc > 0 {
		dto["ParentIndexNumber"] = item.Tags.Disc
	}
	album, ok := catalog.Folder(item.ParentID)
	if !ok {
		return
	}
	dto["Album"], dto["AlbumId"] = album.Name, album.ID
	// A track without its own cover shows its album's.
	if _, own := dto["ImageTags"].(object)["Primary"]; !own {
		if info, found := s.imageInfos(catalog, album)["Primary"]; found {
			if tag := s.store.ImageTag(token, album.ID, info.Revision); tag != "" {
				dto["AlbumPrimaryImageTag"] = tag
			}
		}
	}
}

// failsMusicFilters reports whether item is excluded by the artist and album
// filters in query, whose keys are lower case. Only tracks and albums have
// artists and albums. A contributing artist performs on a track or album
// without being its album artist. No item has a composer, as Couch reads no
// composer tag, so the artist page's "composed by" row is empty.
func failsMusicFilters(catalog *media.Catalog, item *media.Item, query map[string]string) bool {
	if query["composerartistids"] != "" {
		return true
	}
	named := func(key string, names []string) bool {
		return slices.ContainsFunc(names, func(name string) bool { return member(query[key], metadata.ID("MusicArtist", name)) })
	}
	for key, types := range map[string]string{"artistids": "Artist,AlbumArtist", "albumartistids": "AlbumArtist", "contributingartistids": "Artist"} {
		if query[key] != "" && !named(key, artistsOf(catalog, *item, types)) {
			return true
		}
	}
	if query["contributingartistids"] != "" && named("contributingartistids", artistsOf(catalog, *item, "AlbumArtist")) {
		return true
	}
	return query["albumids"] != "" && (item.Type() != "Audio" || !member(query["albumids"], item.ParentID))
}

// artistList answers Artists and Artists/AlbumArtists: the distinct artists
// of the tracks and albums in scope (ParentId), of the ArtistType asked for
// ("all" or none means both), sorted by name in SortOrder. Every artist is in
// a music library, so CollectionTypes without music, as the home Favorites
// tab's audiobook authors row sends, leaves none.
func (s *Server) artistList(albumArtists bool) authenticated {
	return func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		query, err := catalogQuery(r)
		if err != nil {
			fail(w, 400, "InvalidQuery")
			return
		}
		start, limit, ok := pageBounds(query["startindex"], query["limit"])
		if !ok {
			fail(w, 400, "InvalidPagination")
			return
		}
		types := query["artisttype"]
		if albumArtists {
			types = "AlbumArtist"
		} else if types == "" || strings.EqualFold(types, "all") {
			types = "Artist,AlbumArtist"
		}
		user := userOf(s.store.Snapshot(), session)
		favorites := member(query["filters"], "IsFavorite") || strings.EqualFold(query["isfavorite"], "true")
		catalog := s.catalogFor(session)
		found, seen := []string{}, map[string]bool{}
		if catalog != nil && (query["collectiontypes"] == "" || member(query["collectiontypes"], media.Music)) {
			for _, items := range [][]media.Item{catalog.Items, catalog.Folders} {
				for i := range items {
					item := &items[i]
					if !isMusic(*item) || !inMetadataScope(catalog, item, query["parentid"], "") {
						continue
					}
					for _, name := range artistsOf(catalog, *item, types) {
						id := metadata.ID("MusicArtist", name)
						if seen[id] || (favorites && !user.Items[id].IsFavorite) || !matchesSearch(query["searchterm"], name) ||
							(query["namestartswithorgreater"] != "" && strings.ToLower(name) < strings.ToLower(query["namestartswithorgreater"])) {
							continue
						}
						seen[id] = true
						found = append(found, name)
					}
				}
			}
		}
		slices.SortFunc(found, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
		if strings.EqualFold(query["sortorder"], "Descending") {
			slices.Reverse(found)
		}
		from := min(start, len(found))
		serverID := s.store.Snapshot().ServerID
		items := []object{}
		for _, name := range found[from : from+min(limit, len(found)-from)] {
			id := metadata.ID("MusicArtist", name)
			dto := entityDTO(id, entity{"MusicArtist", name}, serverID)
			dto["UserData"] = itemUserData(user.Items[id], 0)
			items = append(items, dto)
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": len(found)})
	}
}

func (s *Server) musicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /artists", s.protect(s.artistList(false)))
	mux.HandleFunc("GET /artists/albumartists", s.protect(s.artistList(true)))
	// Emby Web offers Instant Mix on every track, album and artist. Until it
	// is implemented, its error dialog says so instead of "not found".
	notYet := s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		failText(w, 400, "Instant Mix is not available in Couch yet.")
	})
	for _, path := range []string{"/items/{item}/instantmix", "/albums/{item}/instantmix", "/songs/{item}/instantmix", "/playlists/{item}/instantmix",
		"/artists/instantmix", "/musicgenres/instantmix", "/musicgenres/{name}/instantmix"} {
		mux.HandleFunc("GET "+path, notYet)
	}
}

// maxCovers covers read from audio files are cached; the least recently
// used goes first.
const maxCovers = 1000

// SetCoverCache names the directory that caches covers read from audio
// files. Call it before serving.
func (s *Server) SetCoverCache(dir string) { s.coverDir = dir }

// embeddedCover opens the cover embedded in item's audio file, or its first
// track's for an album, from the cache, reading it with FFmpeg the first time.
func (s *Server) embeddedCover(ctx context.Context, catalog *media.Catalog, item media.Item) (*os.File, media.Image, error) {
	track, info, found := catalog.EmbeddedCover(item)
	if !found || s.coverDir == "" {
		return nil, media.Image{}, os.ErrNotExist
	}
	name := filepath.Join(s.coverDir, info.Revision)
	if file, err := os.Open(name); err == nil { //nolint:gosec // name is a revision hash under the data directory
		now := time.Now()
		_ = os.Chtimes(name, now, now)
		return file, info, nil
	}
	data, err := catalog.ExtractCover(ctx, track)
	if err != nil {
		return nil, media.Image{}, err
	}
	if err := os.MkdirAll(s.coverDir, 0700); err != nil {
		return nil, media.Image{}, err
	}
	temp, err := os.CreateTemp(s.coverDir, ".cover-*")
	if err != nil {
		return nil, media.Image{}, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, err = temp.Write(data)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), name)
	}
	if err != nil {
		return nil, media.Image{}, err
	}
	pruneCovers(s.coverDir)
	file, err := os.Open(name) //nolint:gosec // name is a revision hash under the data directory
	return file, info, err
}

// pruneCovers removes the least recently used cached covers beyond
// maxCovers.
func pruneCovers(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxCovers {
		return
	}
	type cached struct {
		name string
		used time.Time
	}
	files := []cached{}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && !strings.HasPrefix(entry.Name(), ".") {
			files = append(files, cached{entry.Name(), info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b cached) int { return b.used.Compare(a.used) })
	for _, f := range files[min(maxCovers, len(files)):] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}
