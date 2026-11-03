package state

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
)

// Limits on collections, which administrators create from Emby Web and
// SyncSets from movie sets; a set becomes a collection once this many movies
// name it.
const (
	maxCollections     = 1000
	maxCollectionItems = 10000
	minSetItems        = 2
)

// Errors returned by the collection methods of Store.
var (
	ErrCollectionNotFound = errors.New("collection not found")
	ErrCollectionName     = errors.New("collection name must contain 1–128 bytes")
)

func collectionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", ErrCollectionName
	}
	return name, nil
}

// Collection is a named, ordered list of catalogue item IDs shared by all
// users: Emby's BoxSet. Items keeps IDs whose files are gone, so an item
// comes back when its path does.
//
// Set identifies the movie set an automatic collection follows, as
// setIdents names it; it is empty for one an administrator created.
// SetItems are the members SyncSets added, which leave with the set;
// Declined are members an administrator removed, which are not added again
// while they stay in the set.
type Collection struct {
	ID       string
	Name     string
	Added    time.Time
	Items    []string
	Set      string
	SetItems []string
	Declined []string
}

// MovieSet is the set a movie's metadata names: by name, from a .nfo or
// TMDb, and by TMDb collection ID. Pending is true while the movie waits
// for its online metadata, which may still name a set: it can join one
// then, but not leave one.
type MovieSet struct {
	Name, TmdbID string
	Pending      bool
}

// setIdents lists what identifies a movie set: its TMDb ID, which does not
// change with the metadata language, then its name, ignoring case. Movies
// that share either belong to the same set.
func setIdents(m MovieSet) []string {
	var idents []string
	if id := strings.TrimSpace(m.TmdbID); id != "" {
		idents = append(idents, "tmdb:"+id)
	}
	if name := strings.ToLower(strings.TrimSpace(m.Name)); name != "" {
		idents = append(idents, "name:"+name)
	}
	return idents
}

// Collection returns the collection with this ID.
func (d Data) Collection(id string) (Collection, bool) {
	for _, c := range d.Collections {
		if c.ID == id {
			return c, true
		}
	}
	return Collection{}, false
}

// appendNew appends the IDs of add that items lacks, in order, and returns how
// many it appended.
func appendNew(items, add []string) ([]string, int) {
	n := len(items)
	for _, id := range add {
		if id != "" && !slices.Contains(items, id) {
			items = append(items, id)
		}
	}
	return items, len(items) - n
}

// changeCollection replaces the collection id with the result of fn, which
// works on a copy.
func (s *Store) changeCollection(id string, fn func(*Collection) error) error {
	return s.update("", nil, func(d *Data) error {
		i := slices.IndexFunc(d.Collections, func(c Collection) bool { return c.ID == id })
		if i < 0 {
			return ErrCollectionNotFound
		}
		c := d.Collections[i]
		c.Items, c.SetItems, c.Declined = slices.Clone(c.Items), slices.Clone(c.SetItems), slices.Clone(c.Declined)
		if err := fn(&c); err != nil {
			return err
		}
		if len(c.Items) > maxCollectionItems {
			return ErrStateLimit
		}
		d.Collections = slices.Clone(d.Collections)
		d.Collections[i] = c
		return nil
	})
}

// CreateCollection stores a new collection with the items, without duplicates.
func (s *Store) CreateCollection(name string, items []string) (Collection, error) {
	name, err := collectionName(name)
	if err != nil {
		return Collection{}, err
	}
	c := Collection{ID: randomID(), Name: name, Added: time.Now().UTC()}
	c.Items, _ = appendNew([]string{}, items)
	if len(c.Items) > maxCollectionItems {
		return Collection{}, ErrStateLimit
	}
	err = s.update("", nil, func(d *Data) error {
		if len(d.Collections) >= maxCollections {
			return ErrStateLimit
		}
		d.Collections = append(slices.Clone(d.Collections), c)
		return nil
	})
	return c, err
}

// AddToCollection appends the items the collection lacks and returns how many.
func (s *Store) AddToCollection(id string, items []string) (int, error) {
	added := 0
	err := s.changeCollection(id, func(c *Collection) error {
		c.Items, added = appendNew(c.Items, items)
		c.Declined = slices.DeleteFunc(c.Declined, func(item string) bool { return slices.Contains(items, item) })
		return nil
	})
	return added, err
}

// RemoveFromCollection removes the items from the collection. An automatic
// collection declines them, so that SyncSets does not add them again.
func (s *Store) RemoveFromCollection(id string, items []string) error {
	return s.changeCollection(id, func(c *Collection) error {
		removed := func(item string) bool { return slices.Contains(items, item) }
		if c.Set != "" {
			for _, item := range c.Items {
				if removed(item) {
					c.Declined = append(c.Declined, item)
				}
			}
		}
		c.Items = slices.DeleteFunc(c.Items, removed)
		c.SetItems = slices.DeleteFunc(c.SetItems, removed)
		return nil
	})
}

// RenameCollection changes the collection's name.
func (s *Store) RenameCollection(id, name string) error {
	name, err := collectionName(name)
	if err != nil {
		return err
	}
	return s.changeCollection(id, func(c *Collection) error {
		c.Name = name
		return nil
	})
}

// DeleteCollections removes the collections; their items are not touched.
// The set of an automatic collection is dismissed.
func (s *Store) DeleteCollections(ids []string) error {
	return s.update("", nil, func(d *Data) error {
		d.DismissedSets = slices.Clone(d.DismissedSets)
		for _, id := range ids {
			c, ok := d.Collection(id)
			if !ok {
				return ErrCollectionNotFound
			}
			if c.Set != "" && !slices.Contains(d.DismissedSets, c.Set) {
				d.DismissedSets = append(d.DismissedSets, c.Set)
			}
		}
		slices.Sort(d.DismissedSets)
		d.Collections = slices.DeleteFunc(slices.Clone(d.Collections), func(c Collection) bool { return slices.Contains(ids, c.ID) })
		return nil
	})
}

var errUnchanged = errors.New("unchanged")

// setGroups joins the identifiers of movie sets that a movie names together,
// as a .nfo name and a TMDb ID, and finds each identifier's group.
type setGroups map[string]string

func (g setGroups) find(ident string) string {
	for g[ident] != ident {
		g[ident] = g[g[ident]]
		ident = g[ident]
	}
	return ident
}

// root returns the group of ident, or "" when no movie names it.
func (g setGroups) root(ident string) string {
	if _, found := g[ident]; !found {
		return ""
	}
	return g.find(ident)
}

// movieSets is what SyncSets works from: the group of each movie and,
// per group, its movies, display name, identifiers and preferred identifier.
type movieSets struct {
	groups  setGroups
	movie   map[string]string
	members map[string][]string
	name    map[string]string
	idents  map[string][]string
	key     map[string]string
}

func groupSets(sets map[string]MovieSet) movieSets {
	m := movieSets{groups: setGroups{}, movie: map[string]string{}, members: map[string][]string{},
		name: map[string]string{}, idents: map[string][]string{}, key: map[string]string{}}
	for _, set := range sets {
		idents := setIdents(set)
		for _, ident := range idents {
			if _, found := m.groups[ident]; !found {
				m.groups[ident] = ident
			}
		}
		for _, ident := range idents[min(1, len(idents)):] {
			if a, b := m.groups.find(idents[0]), m.groups.find(ident); a != b {
				m.groups[max(a, b)] = min(a, b)
			}
		}
	}
	for ident := range m.groups {
		root := m.groups.find(ident)
		m.idents[root] = append(m.idents[root], ident)
	}
	for root, idents := range m.idents {
		slices.Sort(idents)
		m.key[root] = idents[0]
		if i := slices.IndexFunc(idents, func(ident string) bool { return strings.HasPrefix(ident, "tmdb:") }); i >= 0 {
			m.key[root] = idents[i]
		}
	}
	for id, set := range sets {
		idents := setIdents(set)
		if len(idents) == 0 {
			continue
		}
		root := m.groups.find(idents[0])
		m.movie[id] = root
		m.members[root] = append(m.members[root], id)
		if name := strings.TrimSpace(set.Name); name != "" && (m.name[root] == "" || name < m.name[root]) {
			m.name[root] = name
		}
	}
	for _, ids := range m.members {
		slices.Sort(ids)
	}
	return m
}

// SyncSets brings the automatic collections in line with sets, the movie
// set of every item the metadata describes (a zero MovieSet for none), and
// returns the collections it created. An automatic collection adds its
// set's new members, unless they are in it already or declined, and drops
// the members it added once they name another set or none; an item missing
// from sets, as when its file is gone, is left as it is. A set that
// minSetItems movies name gets a collection unless it has one or was
// dismissed.
func (s *Store) SyncSets(sets map[string]MovieSet) ([]Collection, error) {
	m := groupSets(sets)
	// left reports whether an item no longer belongs to the set root, ""
	// when no movie names the set.
	left := func(id, root string) bool {
		set, known := sets[id]
		return known && !set.Pending && (root == "" || m.movie[id] != root)
	}
	var created []Collection
	err := s.update("", nil, func(d *Data) error {
		changed := false
		collections := slices.Clone(d.Collections)
		have := map[string]bool{}
		for i, c := range collections {
			root := m.groups.root(c.Set)
			if c.Set == "" || have[root] {
				// A second collection of the same set is left alone.
				continue
			}
			if root != "" {
				have[root] = true
				if c.Set != m.key[root] {
					c.Set, changed = m.key[root], true
				}
			}
			gone := func(id string) bool { return left(id, root) }
			if slices.ContainsFunc(c.SetItems, gone) || slices.ContainsFunc(c.Declined, gone) {
				leaving := map[string]bool{}
				for _, id := range c.SetItems {
					leaving[id] = gone(id)
				}
				c.Items = slices.DeleteFunc(slices.Clone(c.Items), func(id string) bool { return leaving[id] })
				c.SetItems = slices.DeleteFunc(slices.Clone(c.SetItems), gone)
				c.Declined = slices.DeleteFunc(slices.Clone(c.Declined), gone)
				changed = true
			}
			if root != "" {
				skip := map[string]bool{}
				for _, id := range slices.Concat(c.Items, c.Declined) {
					skip[id] = true
				}
				var add []string
				for _, id := range m.members[root] {
					if !skip[id] && len(c.Items)+len(add) < maxCollectionItems {
						add = append(add, id)
					}
				}
				if len(add) > 0 {
					c.Items = slices.Concat(c.Items, add)
					c.SetItems = slices.Concat(c.SetItems, add)
					changed = true
				}
			}
			collections[i] = c
		}
		for _, root := range slices.Sorted(maps.Keys(m.members)) {
			ids := m.members[root]
			dismissed := slices.ContainsFunc(m.idents[root], func(ident string) bool { return slices.Contains(d.DismissedSets, ident) })
			if have[root] || dismissed || m.name[root] == "" || len(ids) < minSetItems || len(ids) > maxCollectionItems || len(collections) >= maxCollections {
				continue
			}
			c := Collection{ID: randomID(), Name: m.name[root], Added: time.Now().UTC(), Items: slices.Clone(ids), Set: m.key[root], SetItems: slices.Clone(ids)}
			collections = append(collections, c)
			created = append(created, c)
			changed = true
		}
		if !changed {
			return errUnchanged
		}
		d.Collections = collections
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return nil, nil
	}
	return created, err
}
