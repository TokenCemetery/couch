package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// lookupVersion changes whenever what a lookup fetches does, so stored
// online metadata is fetched again.
const lookupVersion = "1"

// Config selects the online providers and how long their metadata is kept.
type Config struct {
	// An empty key turns its provider off: Couch then sends it nothing.
	TMDbKey, TVDbKey string
	// Language is one of SupportedLanguages.
	Language string
	// Refresh fetches online metadata again once it is this old; 0 never does.
	Refresh time.Duration
	// ImageDir holds downloaded provider images.
	ImageDir string
}

// Refresher keeps the metadata of the published catalogue up to date and
// publishes it after each change.
type Refresher struct {
	store    *state.Store
	cfg      Config
	lang     language
	publish  func(Library)
	catalogs chan *media.Catalog
}

// NewRefresher checks cfg. publish receives every new Library.
func NewRefresher(store *state.Store, cfg Config, publish func(Library)) (*Refresher, error) {
	lang, ok := languages[cfg.Language]
	if !ok {
		return nil, fmt.Errorf("metadata language must be one of %s", strings.Join(SupportedLanguages(), ", "))
	}
	return &Refresher{store: store, cfg: cfg, lang: lang, publish: publish, catalogs: make(chan *media.Catalog, 1)}, nil
}

// Online reports whether any online provider has a key.
func (r *Refresher) Online() bool { return r.cfg.TMDbKey != "" || r.cfg.TVDbKey != "" }

// Update hands Run a newly published catalogue. A run in progress stops and
// starts again with it; what it already fetched is kept.
func (r *Refresher) Update(catalog *media.Catalog) {
	for {
		select {
		case r.catalogs <- catalog:
			return
		default:
		}
		select {
		case <-r.catalogs:
		default:
		}
	}
}

// Run refreshes metadata for each catalogue Update hands it and, with a
// Refresh interval and an online provider, hourly for what has aged. It
// returns when ctx is done.
func (r *Refresher) Run(ctx context.Context) {
	var tick <-chan time.Time
	if r.cfg.Refresh > 0 && r.Online() {
		ticker := time.NewTicker(min(r.cfg.Refresh, time.Hour))
		defer ticker.Stop()
		tick = ticker.C
	}
	var current *media.Catalog
	pending := false
	for {
		if !pending {
			select {
			case <-ctx.Done():
				return
			case current = <-r.catalogs:
			case <-tick:
			}
		}
		pending = false
		if current == nil {
			continue
		}
		run, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func(catalog *media.Catalog) {
			defer close(done)
			r.refresh(run, slices.Concat(catalog.Folders, catalog.Items), catalog)
		}(current)
		select {
		case <-done:
		case current = <-r.catalogs:
			pending = true
			cancel()
			<-done
		case <-ctx.Done():
			cancel()
			<-done
			return
		}
		cancel()
	}
}

// sidecars reads the files next to the media; *media.Catalog is one.
type sidecars interface {
	ReadSidecar(name string, limit int64) ([]byte, fs.FileInfo, error)
}

// run is one refresh: the stored records and their decoded metadata.
type run struct {
	*Refresher
	files   sidecars
	records map[string]state.MetadataRecord
	nfo     map[string]Info
	online  map[string]Info
	changed map[string]bool
}

// refresh brings the metadata of the catalogue's items up to date.
func (r *Refresher) refresh(ctx context.Context, items []media.Item, files sidecars) {
	records, err := r.store.Metadata(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Metadata not loaded", "error", err)
		}
		return
	}
	x := &run{Refresher: r, files: files, records: records, nfo: map[string]Info{}, online: map[string]Info{}, changed: map[string]bool{}}
	for id, record := range records {
		x.nfo[id] = decode(record.NFO)
		x.online[id] = decode(record.Online)
	}
	items = sortItems(items)
	read, bad := 0, 0
	for _, item := range items {
		parsed, err := x.readNFO(item)
		if ctx.Err() != nil {
			return
		}
		if parsed {
			read++
		}
		if err != nil {
			bad++
		}
	}
	known := map[string]bool{}
	for _, item := range items {
		known[item.ID] = true
	}
	remove := []string{}
	for id := range records {
		if !known[id] {
			remove = append(remove, id)
			delete(x.records, id)
		}
	}
	if !x.save(ctx, remove) {
		return
	}
	x.publishLibrary()
	if read > 0 || bad > 0 || len(remove) > 0 {
		slog.Info("Metadata files read", "nfo_read", read, "nfo_unreadable", bad, "removed", len(remove))
	}
	if r.Online() {
		x.fetchAll(ctx, items)
	}
}

// sortItems puts series first, so episodes can use their series' provider
// IDs, then seasons, movies and episodes.
func sortItems(items []media.Item) []media.Item {
	rank := map[string]int{"Series": 0, "Season": 1, "Movie": 2, "Episode": 3}
	items = slices.Clone(items)
	slices.SortStableFunc(items, func(a, b media.Item) int { return rank[a.Type()] - rank[b.Type()] })
	return items
}

// nfoNames lists where an item's .nfo may be, in search order.
func nfoNames(item media.Item) []string {
	switch item.Type() {
	case "Series":
		return []string{path.Join(item.Path, "tvshow.nfo")}
	case "Season":
		return []string{path.Join(item.Path, "season.nfo")}
	}
	names := []string{strings.TrimSuffix(item.Path, path.Ext(item.Path)) + ".nfo"}
	if dir := path.Dir(item.Path); item.Type() == "Movie" && dir != "." {
		names = append(names, path.Join(dir, "movie.nfo"))
	}
	return names
}

// readNFO reads the item's .nfo when it changed since it was stored, and
// reports whether it read one. An unreadable file keeps what was stored.
func (x *run) readNFO(item media.Item) (bool, error) {
	revision := ""
	var content []byte
	for _, name := range nfoNames(item) {
		b, info, err := x.files.ReadSidecar(name, maxNFOBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		revision = name + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
		content = b
		break
	}
	record, found := x.records[item.ID]
	if found && record.NFORevision == revision {
		return false, nil
	}
	record.ItemID, record.NFORevision, record.NFO = item.ID, revision, nil
	info := Info{}
	var err error
	if content != nil {
		if info, err = parseNFO(content); err == nil {
			record.NFO, _ = json.Marshal(info)
		}
	}
	x.records[item.ID], x.nfo[item.ID], x.changed[item.ID] = record, info, true
	return content != nil, err
}

func decode(body []byte) Info {
	var info Info
	if len(body) > 0 {
		_ = json.Unmarshal(body, &info)
	}
	return info
}

// save writes the changed records and removes those of remove. A failed
// write is retried with the next refresh.
func (x *run) save(ctx context.Context, remove []string) bool {
	if len(x.changed) == 0 && len(remove) == 0 {
		return true
	}
	records := make([]state.MetadataRecord, 0, len(x.changed))
	for id := range x.changed {
		records = append(records, x.records[id])
	}
	if err := x.store.SaveMetadata(context.WithoutCancel(ctx), records, remove); err != nil {
		slog.Warn("Metadata not saved", "error", err)
		return false
	}
	clear(x.changed)
	return true
}

// info is the item's metadata: its .nfo, completed by online providers.
func (x *run) info(id string) Info {
	return merge(x.nfo[id], x.online[id])
}

func (x *run) publishLibrary() {
	library := Library{}
	for id := range x.records {
		info := x.info(id)
		library[id] = &info
	}
	x.publish(library)
}

// lookup describes what to ask the providers for an item, and the key that
// identifies the answer. An empty key means nothing can be looked up.
type lookup struct {
	key             string
	ids             map[string]string
	title           string
	year            int
	series          map[string]string
	season, episode int
}

func (x *run) providers() string {
	names := []string{}
	if x.cfg.TVDbKey != "" {
		names = append(names, "tvdb")
	}
	if x.cfg.TMDbKey != "" {
		names = append(names, "tmdb")
	}
	return strings.Join(names, "+")
}

func idText(ids map[string]string) string {
	parts := []string{}
	for _, key := range []string{"Tvdb", "Tmdb", "Imdb"} {
		if ids[key] != "" {
			parts = append(parts, key+":"+ids[key])
		}
	}
	return strings.Join(parts, ",")
}

func (x *run) lookupOf(item media.Item) lookup {
	prefix := strings.Join([]string{lookupVersion, item.Type(), x.cfg.Language, x.providers()}, "|") + "|"
	switch item.Type() {
	case "Season":
		return lookup{}
	case "Episode":
		series := x.info(item.SeriesID).ProviderIDs
		if series["Tvdb"] == "" && series["Tmdb"] == "" {
			return lookup{}
		}
		return lookup{key: prefix + idText(series) + "|" + strconv.Itoa(item.SeasonNumber) + "x" + strconv.Itoa(item.EpisodeNumber),
			series: series, season: item.SeasonNumber, episode: item.EpisodeNumber}
	}
	nfo := x.nfo[item.ID]
	l := lookup{ids: nfo.ProviderIDs, title: nfo.Name, year: nfo.Year}
	if l.title == "" {
		l.title, l.year = lookupName(item.Name)
	}
	if ids := idText(l.ids); ids != "" {
		l.key = prefix + ids
	} else {
		l.key = prefix + "q:" + strings.ToLower(l.title) + "|" + strconv.Itoa(l.year)
	}
	return l
}

// due reports whether an item's online metadata must be fetched.
func (x *run) due(id, key string, now time.Time) bool {
	record := x.records[id]
	if record.OnlineKey != key {
		return true
	}
	if x.cfg.Refresh > 0 && now.Sub(record.FetchedAt) >= x.cfg.Refresh {
		return true
	}
	for _, image := range x.online[id].Images {
		if _, err := os.Stat(filepath.Join(x.cfg.ImageDir, image.File)); err != nil {
			return true
		}
	}
	return false
}

// maxFailures stops a run after this many failed requests in a row, as the
// provider or the network is then likely down.
const maxFailures = 5

func (x *run) fetchAll(ctx context.Context, items []media.Item) {
	var tm *tmdb
	var tv *tvdb
	if x.cfg.TMDbKey != "" {
		tm = newTMDb(x.cfg.TMDbKey, x.lang)
	}
	if x.cfg.TVDbKey != "" {
		tv = newTVDb(x.cfg.TVDbKey, x.lang)
	}
	images := &throttle{every: 20 * time.Millisecond}
	fetched, notFound, failed, failures := 0, 0, 0, 0
	stopped := ""
	for _, item := range items {
		now := time.Now().UTC()
		l := x.lookupOf(item)
		if l.key == "" || !x.due(item.ID, l.key, now) {
			continue
		}
		info, urls, err := x.fetch(ctx, tm, tv, item, l)
		if ctx.Err() != nil {
			x.save(ctx, nil)
			return
		}
		switch {
		case errors.Is(err, errAuth):
			stopped = "an API key was rejected"
		case err != nil && !errors.Is(err, errNotFound):
			failed++
			failures++
			if failures >= maxFailures {
				stopped = "requests keep failing"
			}
		}
		if stopped != "" {
			break
		}
		if err != nil && !errors.Is(err, errNotFound) {
			continue
		}
		failures = 0
		if errors.Is(err, errNotFound) {
			notFound++
		} else {
			fetched++
		}
		for _, kind := range []string{"Primary", "Backdrop"} {
			if urls[kind] == "" {
				continue
			}
			image, err := download(ctx, x.cfg.ImageDir, urls[kind], images)
			if ctx.Err() != nil {
				x.save(ctx, nil)
				return
			}
			if err == nil {
				if info.Images == nil {
					info.Images = map[string]Image{}
				}
				info.Images[kind] = image
			}
		}
		record := x.records[item.ID]
		record.ItemID, record.OnlineKey, record.FetchedAt = item.ID, l.key, now
		record.Online, _ = json.Marshal(info)
		x.records[item.ID], x.online[item.ID], x.changed[item.ID] = record, info, true
		if len(x.changed) >= 25 {
			x.save(ctx, nil)
			x.publishLibrary()
		}
	}
	x.save(ctx, nil)
	x.publishLibrary()
	if stopped != "" {
		slog.Warn("Online metadata lookup stopped; it is tried again with the next rescan", "reason", stopped)
	} else {
		x.removeUnusedImages()
	}
	if fetched > 0 || notFound > 0 || failed > 0 {
		slog.Info("Online metadata fetched", "items", fetched, "not_found", notFound, "failed", failed)
	}
}

// fetch asks each provider with a key, TheTVDB before TMDb for series as
// Emby does, and merges their answers in that order. TMDb looks a series up
// by the IDs TheTVDB found.
func (x *run) fetch(ctx context.Context, tm *tmdb, tv *tvdb, item media.Item, l lookup) (Info, map[string]string, error) {
	type source func() (Info, map[string]string, error)
	sources := []source{}
	var found Info
	switch item.Type() {
	case "Movie":
		if tm != nil {
			sources = append(sources, func() (Info, map[string]string, error) { return tm.movie(ctx, l.ids, l.title, l.year) })
		}
	case "Series":
		if tv != nil {
			sources = append(sources, func() (Info, map[string]string, error) { return tv.series(ctx, l.ids, l.title, l.year) })
		}
		if tm != nil {
			sources = append(sources, func() (Info, map[string]string, error) {
				return tm.series(ctx, union(found.ProviderIDs, l.ids), l.title, l.year)
			})
		}
	case "Episode":
		if tv != nil && l.series["Tvdb"] != "" {
			sources = append(sources, func() (Info, map[string]string, error) { return tv.episode(ctx, l.series["Tvdb"], l.season, l.episode) })
		}
		if tm != nil && l.series["Tmdb"] != "" {
			sources = append(sources, func() (Info, map[string]string, error) { return tm.episode(ctx, l.series["Tmdb"], l.season, l.episode) })
		}
	}
	urls := map[string]string{}
	answered := false
	for _, source := range sources {
		info, images, err := source()
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return Info{}, nil, err
		}
		answered = true
		found = merge(found, info)
		for kind, url := range images {
			if urls[kind] == "" {
				urls[kind] = url
			}
		}
	}
	if !answered {
		return Info{}, nil, errNotFound
	}
	return found, urls, nil
}

// removeUnusedImages deletes downloaded images no item uses any more.
func (x *run) removeUnusedImages() {
	used := map[string]bool{}
	for id := range x.records {
		for _, image := range x.online[id].Images {
			used[image.File] = true
		}
	}
	entries, err := os.ReadDir(x.cfg.ImageDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && !used[entry.Name()] {
			_ = os.Remove(filepath.Join(x.cfg.ImageDir, entry.Name()))
		}
	}
}
