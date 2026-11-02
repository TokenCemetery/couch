package media

import "sync"

// catalogIndex answers per-folder questions without scanning every item.
// A catalogue is not changed after it is built, so the index is built once,
// on first use.
type catalogIndex struct {
	once     sync.Once
	episodes map[string][]int // series, season, album or photo album ID → positions in Items
	children map[string]int   // parent ID → number of direct children
	folders  map[string]int   // folder ID → position in Folders
	items    map[string]int   // video ID → position in Items
}

func (c *Catalog) built() *catalogIndex {
	c.index.once.Do(func() {
		c.index.episodes = map[string][]int{}
		c.index.children = map[string]int{}
		c.index.folders = map[string]int{}
		c.index.items = map[string]int{}
		for n, folder := range c.Folders {
			c.index.children[c.Parent(folder)]++
			c.index.folders[folder.ID] = n
		}
		for n, item := range c.Items {
			c.index.items[item.ID] = n
			if item.SeriesID != "" {
				c.index.episodes[item.SeriesID] = append(c.index.episodes[item.SeriesID], n)
			}
			if item.SeasonID != "" && item.SeasonID != item.SeriesID {
				c.index.episodes[item.SeasonID] = append(c.index.episodes[item.SeasonID], n)
			}
			if f, album := c.index.folders[item.ParentID]; album && (item.Type() == "Audio" || c.Folders[f].Type() == "PhotoAlbum") {
				c.index.episodes[item.ParentID] = append(c.index.episodes[item.ParentID], n)
			}
			c.index.children[c.Parent(item)]++
		}
	})
	return &c.index
}

// Episodes lists the episodes of a series or season, the tracks of an album,
// or the photos and videos directly in a photo album, in catalogue order.
func (c *Catalog) Episodes(folderID string) []Item {
	positions := c.built().episodes[folderID]
	episodes := make([]Item, len(positions))
	for i, n := range positions {
		episodes[i] = c.Items[n]
	}
	return episodes
}

// ChildCount returns the number of items and folders directly under id.
func (c *Catalog) ChildCount(id string) int {
	return c.built().children[id]
}

// Folder returns the series, season or album with this ID.
func (c *Catalog) Folder(id string) (Item, bool) {
	n, found := c.built().folders[id]
	if !found {
		return Item{}, false
	}
	return c.Folders[n], true
}

// Lookup returns the video, track, series, season or album with this ID. The catalogue is
// not changed after it is built, so the item must not be modified.
func (c *Catalog) Lookup(id string) (*Item, bool) {
	if n, found := c.built().items[id]; found {
		return &c.Items[n], true
	}
	if n, found := c.built().folders[id]; found {
		return &c.Folders[n], true
	}
	return nil, false
}
