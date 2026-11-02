package state

import (
	"errors"
	"slices"
	"strings"
	"time"
)

// Limits on collections, which administrators create from Emby Web.
const (
	maxCollections     = 1000
	maxCollectionItems = 10000
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
type Collection struct {
	ID    string
	Name  string
	Added time.Time
	Items []string
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
		c.Items = slices.Clone(c.Items)
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
		return nil
	})
	return added, err
}

// RemoveFromCollection removes the items from the collection.
func (s *Store) RemoveFromCollection(id string, items []string) error {
	return s.changeCollection(id, func(c *Collection) error {
		c.Items = slices.DeleteFunc(c.Items, func(item string) bool { return slices.Contains(items, item) })
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
func (s *Store) DeleteCollections(ids []string) error {
	return s.update("", nil, func(d *Data) error {
		for _, id := range ids {
			if _, ok := d.Collection(id); !ok {
				return ErrCollectionNotFound
			}
		}
		d.Collections = slices.DeleteFunc(slices.Clone(d.Collections), func(c Collection) bool { return slices.Contains(ids, c.ID) })
		return nil
	})
}
