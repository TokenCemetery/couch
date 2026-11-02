package media

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// trackName splits a file name such as "03 - Title" or "1-03. Title" into
// its disc, track number and title.
var trackName = regexp.MustCompile(`^(?:(\d{1,2})[-.])?(\d{1,3})(?:\s*[-.]\s*|\s+)(\S.*)$`)

// classifyMusic marks the folder's audio files as tracks and groups them
// into albums. Tags decide first; a file without them takes its album from
// its folder, its album artist from the folder above that and its number and
// title from its name, as in Artist/Album/03 - Title.mp3. A file with
// neither an album tag nor a folder has no album.
func (s *source) classifyMusic() {
	albums := map[string]Item{}
	for n := range s.items {
		item := &s.items[n]
		if !isAudio(path.Ext(strings.ToLower(item.Path))) {
			continue
		}
		item.Kind = "Audio"
		tags := item.Tags
		dir := path.Dir(item.Path)
		if match := trackName.FindStringSubmatch(item.Name); match != nil {
			if tags.Track == 0 {
				tags.Track, _ = strconv.Atoi(match[2])
				if tags.Disc == 0 && match[1] != "" {
					tags.Disc, _ = strconv.Atoi(match[1])
				}
			}
			if tags.Title == "" {
				tags.Title = match[3]
			}
		}
		if tags.Title != "" {
			item.Name = tags.Title
		}
		album, albumArtist := tags.Album, tags.AlbumArtist
		if album == "" && dir != "." {
			album = path.Base(dir)
		}
		if albumArtist == "" && len(tags.Artists) > 0 {
			albumArtist = tags.Artists[0]
		}
		if albumArtist == "" && path.Dir(dir) != "." {
			albumArtist = path.Base(path.Dir(dir))
		}
		item.Tags = tags
		if album == "" {
			continue
		}
		id := stableID(s.key + "\x00album\x00" + strings.ToLower(albumArtist) + "\x00" + strings.ToLower(album))
		item.ParentID = id
		folder, exists := albums[id]
		if !exists {
			folder = Item{ID: id, Name: album, Kind: "MusicAlbum", Path: dir, src: s, Modified: item.Modified, Added: item.Added,
				Tags: Tags{Album: album, AlbumArtist: albumArtist}}
		}
		// The album's folder, for its cover, is its first track's.
		if dir < folder.Path {
			folder.Path = dir
		}
		folder.Modified = later(folder.Modified, item.Modified)
		folder.Added = later(folder.Added, item.Added)
		folder.RunTimeTicks += item.RunTimeTicks
		folder.Tags.Year = max(folder.Tags.Year, tags.Year)
		for _, artist := range tags.Artists {
			if !slices.Contains(folder.Tags.Artists, artist) {
				folder.Tags.Artists = append(folder.Tags.Artists, artist)
			}
		}
		for _, genre := range tags.Genres {
			if !slices.Contains(folder.Tags.Genres, genre) {
				folder.Tags.Genres = append(folder.Tags.Genres, genre)
			}
		}
		albums[id] = folder
	}
	for _, album := range albums {
		s.folders = append(s.folders, album)
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
