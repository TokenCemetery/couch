package media

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Only an explicit Show/Season NN/name.SnnEnn.ext layout is classified as TV.
// Ambiguous names remain movies; no external metadata or filename guessing.
var seasonDirectory = regexp.MustCompile(`(?i)^season[ ._-]*(\d{1,3})$`)
var episodeName = regexp.MustCompile(`(?i)(?:^|[ ._-])s(\d{1,3})e(\d{1,4})(?:$|[ ._-])`)

// SeriesLibraryID returns the ID of the TV Shows library of a catalogue built
// without libraries.
func (c *Catalog) SeriesLibraryID() string { return stableID("series\x00" + c.ID) }

// Type returns the Emby item type; items without a Kind are movies.
func (i Item) Type() string {
	if i.Kind == "" {
		return "Movie"
	}
	return i.Kind
}

// IsFolder reports whether the item is a series, season, music album or
// photo album.
func (i Item) IsFolder() bool {
	return i.Kind == "Series" || i.Kind == "Season" || i.Kind == "MusicAlbum" || i.Kind == "PhotoAlbum"
}

// Parent returns the parent item ID: the library for a movie or series.
func (c *Catalog) Parent(item Item) string {
	if item.ParentID != "" {
		return item.ParentID
	}
	return c.ID
}

// classify marks the episodes of the folder and builds their series and
// seasons, and the tracks with their albums; every other video stays a movie. Series IDs derive from the folder
// as the TV Shows library of a one-directory catalogue did, so they do not
// change; assemble sets their parent library.
func (s *source) classify() {
	seriesLibrary := stableID("series\x00" + s.key)
	folders := map[string]Item{}
	for n := range s.items {
		item := &s.items[n]
		item.src = s
		if ext := strings.ToLower(path.Ext(item.Path)); isAudio(ext) || isPhoto(ext) {
			continue
		}
		seasonPath := path.Dir(item.Path)
		seriesPath := path.Dir(seasonPath)
		seasonMatch := seasonDirectory.FindStringSubmatch(path.Base(seasonPath))
		episodeMatch := episodeName.FindStringSubmatch(item.Name)
		if seriesPath == "." || len(seasonMatch) == 0 || len(episodeMatch) == 0 {
			continue
		}
		season, _ := strconv.Atoi(seasonMatch[1])
		episodeSeason, _ := strconv.Atoi(episodeMatch[1])
		if season != episodeSeason {
			continue
		}
		episode, _ := strconv.Atoi(episodeMatch[2])
		seriesID := stableID(seriesLibrary + "\x00" + seriesPath)
		seasonID := stableID(seriesID + "\x00" + seasonPath)
		seriesName := path.Base(seriesPath)
		item.Kind, item.ParentID = "Episode", seasonID
		item.SeriesID, item.SeriesName, item.SeasonID = seriesID, seriesName, seasonID
		item.SeasonNumber, item.EpisodeNumber = season, episode
		for _, folder := range []Item{
			{ID: seriesID, Name: seriesName, Kind: "Series", Path: seriesPath, src: s},
			{ID: seasonID, Name: "Season " + strconv.Itoa(season), Kind: "Season", Path: seasonPath, ParentID: seriesID, SeriesID: seriesID, SeriesName: seriesName, SeasonNumber: season, src: s},
		} {
			folder.Modified, folder.Added = item.Modified, item.Added
			if old, exists := folders[folder.ID]; exists {
				if old.Modified.After(folder.Modified) {
					folder.Modified = old.Modified
				}
				if old.Added.After(folder.Added) {
					folder.Added = old.Added
				}
			}
			folders[folder.ID] = folder
		}
	}
	s.folders = make([]Item, 0, len(folders))
	for _, folder := range folders {
		s.folders = append(s.folders, folder)
	}
	s.classifyMusic()
	s.classifyPhotos()
	slices.SortFunc(s.folders, func(a, b Item) int { return strings.Compare(a.ID, b.ID) })
}
