package media

import "sync"

// catalogIndex answers per-folder questions without scanning every item.
// A catalogue is not changed after it is built, so the index is built once,
// on first use.
type catalogIndex struct {
	once     sync.Once
	episodes map[string][]int // series or season ID → positions in Items
	children map[string]int   // parent ID → number of direct children
	folders  map[string]int   // folder ID → position in Folders
}

func (c *Catalog) built() *catalogIndex {
	c.index.once.Do(func() {
		c.index.episodes = map[string][]int{}
		c.index.children = map[string]int{}
		c.index.folders = map[string]int{}
		for n, item := range c.Items {
			if item.SeriesID != "" {
				c.index.episodes[item.SeriesID] = append(c.index.episodes[item.SeriesID], n)
			}
			if item.SeasonID != "" && item.SeasonID != item.SeriesID {
				c.index.episodes[item.SeasonID] = append(c.index.episodes[item.SeasonID], n)
			}
			c.index.children[c.Parent(item)]++
		}
		for n, folder := range c.Folders {
			c.index.children[c.Parent(folder)]++
			c.index.folders[folder.ID] = n
		}
	})
	return &c.index
}

// Episodes lists the episodes of a series or season, in catalogue order.
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

// Folder returns the series or season with this ID.
func (c *Catalog) Folder(id string) (Item, bool) {
	n, found := c.built().folders[id]
	if !found {
		return Item{}, false
	}
	return c.Folders[n], true
}
