package metadata

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// A year in parentheses or brackets, or a bare one after the title.
	nameYear = regexp.MustCompile(`[(\[]((?:18|19|20|21)\d{2})[)\]]|(?:^|[ ._-])((?:18|19|20|21)\d{2})(?:$|[ ._-])`)
	// Release tags that end the title when no year does.
	releaseTag = regexp.MustCompile(`(?i)(?:^|[ ._-])(?:2160p|1080p|1080i|720p|576p|480p|4k|uhd|bluray|blu-ray|bdrip|brrip|dvdrip|webrip|web-dl|webdl|hdtv|hdrip|remux|x264|x265|h264|h265|hevc|xvid|divx)(?:$|[ ._-])`)
	brackets   = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	spaces     = regexp.MustCompile(`\s+`)
)

// lookupName turns a file or folder name into a title and year to search for,
// as Emby cleans names: "The.Movie.2010.1080p" is "The Movie" from 2010.
func lookupName(name string) (string, int) {
	year := 0
	end := len(name)
	// The last year wins, so a title that starts with a year keeps it.
	if all := nameYear.FindAllStringSubmatchIndex(name, -1); len(all) > 0 {
		m := all[len(all)-1]
		group := 2
		if m[group] < 0 {
			group = 4
		}
		if m[0] > 0 {
			year, _ = strconv.Atoi(name[m[group]:m[group+1]])
			end = m[0]
		}
	}
	if m := releaseTag.FindStringIndex(name[:end]); m != nil && m[0] > 0 {
		end = m[0]
	}
	title := brackets.ReplaceAllString(name[:end], " ")
	if !strings.Contains(title, " ") {
		title = strings.NewReplacer(".", " ", "_", " ").Replace(title)
	}
	title = strings.Trim(spaces.ReplaceAllString(title, " "), " -(")
	if title == "" {
		return name, 0
	}
	return title, year
}
