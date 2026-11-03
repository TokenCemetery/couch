package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// lookupVersion changes whenever what a lookup fetches does, so stored
// online metadata is fetched again; nfoVersion likewise for what is read
// from .nfo files.
const (
	lookupVersion = "2"
	nfoVersion    = "2"
)

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

// Retry intervals after a refresh that did not finish, doubling from the
// first to the last; and how often a running lookup publishes what it has.
// Tests shorten them.
var (
	retryFirst   = 5 * time.Minute
	retryMax     = time.Hour
	publishEvery = 10 * time.Second
)

// metadataStore keeps the records; *state.Store is one.
type metadataStore interface {
	Metadata(ctx context.Context) (map[string]state.MetadataRecord, error)
	SaveMetadata(ctx context.Context, records []state.MetadataRecord, remove []string) error
}

// Refresher keeps the metadata of the published catalogue up to date and
// publishes it after each change.
type Refresher struct {
	store    metadataStore
	cfg      Config
	lang     language
	publish  func(Library)
	catalogs chan *media.Catalog
	requests chan struct{}

	mu      sync.Mutex
	running bool
	last    *Result
	// changed is called when a refresh starts or ends.
	changed func()
	// forced holds the items an administrator asked to refresh, and
	// identify the identifications to store, nil to remove one; the next
	// refresh takes both.
	forced   map[string]bool
	identify map[string]map[string]string
}

// Result is how the last refresh went: Complete is false when it stopped
// early, as when a provider failed or a newer catalogue replaced it.
type Result struct {
	Start, End time.Time
	Complete   bool
}

// NewRefresher checks cfg. publish receives every new Library.
func NewRefresher(store metadataStore, cfg Config, publish func(Library)) (*Refresher, error) {
	lang, ok := languages[cfg.Language]
	if !ok {
		return nil, fmt.Errorf("metadata language must be one of %s", strings.Join(SupportedLanguages(), ", "))
	}
	return &Refresher{store: store, cfg: cfg, lang: lang, publish: publish, catalogs: make(chan *media.Catalog, 1), requests: make(chan struct{}, 1)}, nil
}

// RefreshNow asks Run to refresh the current catalogue: changed .nfo files,
// items not found or looked up yet, missing images and, with a Refresh
// interval, what has aged. It returns at once.
func (r *Refresher) RefreshNow() {
	select {
	case r.requests <- struct{}{}:
	default:
	}
}

// Refresh asks Run to read the .nfo of these items again and, with an
// online provider, to fetch their online metadata again, however recently
// it was fetched. It returns at once.
func (r *Refresher) Refresh(ids []string) {
	r.mu.Lock()
	if r.forced == nil {
		r.forced = map[string]bool{}
	}
	for _, id := range ids {
		r.forced[id] = true
	}
	r.mu.Unlock()
	r.RefreshNow()
}

// Identify asks Run to look the item up by these provider IDs from now on,
// as an administrator chose them, and to refresh it; nil ids remove the
// identification, so the item is looked up by its .nfo or file name again.
// It returns at once.
func (r *Refresher) Identify(id string, ids map[string]string) {
	r.mu.Lock()
	if r.identify == nil {
		r.identify = map[string]map[string]string{}
	}
	r.identify[id] = ids
	r.mu.Unlock()
	r.Refresh([]string{id})
}

// takeRequests returns and clears what Refresh and Identify asked for.
func (r *Refresher) takeRequests() (map[string]bool, map[string]map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	forced, identify := r.forced, r.identify
	r.forced, r.identify = nil, nil
	if forced == nil {
		forced = map[string]bool{}
	}
	return forced, identify
}

// requeue hands back what a refresh that stopped early did not do, unless
// a newer request replaced it.
func (r *Refresher) requeue(forced map[string]bool, identify map[string]map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range forced {
		if r.forced == nil {
			r.forced = map[string]bool{}
		}
		r.forced[id] = true
	}
	for id, ids := range identify {
		if _, newer := r.identify[id]; !newer {
			if r.identify == nil {
				r.identify = map[string]map[string]string{}
			}
			r.identify[id] = ids
		}
	}
}

// OnChange sets the function called when a refresh starts or ends.
func (r *Refresher) OnChange(changed func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changed = changed
}

// Status reports whether a refresh runs and how the last one ended.
func (r *Refresher) Status() (running bool, last *Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running, r.last
}

func (r *Refresher) setStatus(running bool, last *Result) {
	r.mu.Lock()
	r.running = running
	if last != nil {
		r.last = last
	}
	changed := r.changed
	r.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// Online reports whether any online provider has a key.
func (r *Refresher) Online() bool { return r.cfg.TMDbKey != "" || r.cfg.TVDbKey != "" }

// Update hands Run a newly published catalogue. A run in progress stops and
// starts again with it when its items changed, keeping what it fetched;
// otherwise the new catalogue is refreshed once that run ends.
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
// Refresh interval and an online provider, hourly for what has aged. A
// refresh that stopped early, as when a provider keeps failing or the
// metadata cannot be saved, is retried with backoff. Run returns when ctx
// is done.
func (r *Refresher) Run(ctx context.Context) {
	var tick <-chan time.Time
	if r.cfg.Refresh > 0 && r.Online() {
		ticker := time.NewTicker(min(r.cfg.Refresh, time.Hour))
		defer ticker.Stop()
		tick = ticker.C
	}
	var current *media.Catalog
	retry := time.NewTimer(0)
	retry.Stop()
	defer retry.Stop()
	backoff := retryFirst
	pending := false
	for {
		if !pending {
			select {
			case <-ctx.Done():
				return
			case current = <-r.catalogs:
			case <-tick:
			case <-retry.C:
			case <-r.requests:
			}
		}
		pending = false
		if current == nil {
			continue
		}
		run, cancel := context.WithCancel(ctx)
		done := make(chan bool, 1)
		go func(catalog *media.Catalog) {
			start := time.Now().UTC()
			r.setStatus(true, nil)
			complete := r.refresh(run, described(catalog), catalog)
			r.setStatus(false, &Result{Start: start, End: time.Now().UTC(), Complete: complete})
			done <- complete
		}(current)
		complete := false
	wait:
		for {
			select {
			case complete = <-done:
				break wait
			case next := <-r.catalogs:
				if !sameItems(current, next) {
					cancel()
				}
				current, pending = next, true
			case <-ctx.Done():
				cancel()
				<-done
				return
			}
		}
		cancel()
		retry.Stop()
		switch {
		case complete:
			backoff = retryFirst
		case !pending:
			retry.Reset(backoff)
			backoff = min(2*backoff, retryMax)
		}
	}
}

// described lists the catalogue's movies, series, seasons and episodes: the
// items .nfo files and the providers describe. Music is described by its
// tags alone, and home videos and photos by their files.
func described(catalog *media.Catalog) []media.Item {
	return slices.DeleteFunc(slices.Concat(catalog.Folders, catalog.Items), func(item media.Item) bool {
		switch item.Type() {
		case "Audio", "MusicAlbum", "Photo", "PhotoAlbum", "Video":
			return true
		}
		return false
	})
}

// sameItems reports whether two catalogues hold the same items and folders.
func sameItems(a, b *media.Catalog) bool {
	ids := func(items []media.Item) []string {
		list := make([]string, len(items))
		for n, item := range items {
			list[n] = item.ID
		}
		return list
	}
	return slices.Equal(ids(a.Items), ids(b.Items)) && slices.Equal(ids(a.Folders), ids(b.Folders))
}

// sidecars reads the files next to the media; *media.Catalog is one.
type sidecars interface {
	ReadSidecar(item media.Item, name string, limit int64) ([]byte, fs.FileInfo, error)
}

// run is one refresh: the stored records and their decoded metadata.
type run struct {
	*Refresher
	files   sidecars
	records map[string]state.MetadataRecord
	nfo     map[string]Info
	online  map[string]Info
	changed map[string]bool
	// forced lists the items to read and fetch again whatever their state;
	// an item leaves it once fetched.
	forced map[string]bool
}

// refresh brings the metadata of the catalogue's items up to date and
// reports whether it finished.
func (r *Refresher) refresh(ctx context.Context, items []media.Item, files sidecars) (complete bool) {
	forced, identify := r.takeRequests()
	saved := false
	defer func() {
		if !complete {
			if saved {
				identify = nil
			}
			r.requeue(forced, identify)
		}
	}()
	records, err := r.store.Metadata(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Metadata not loaded", "error", err)
		}
		return false
	}
	x := &run{Refresher: r, files: files, records: records, nfo: map[string]Info{}, online: map[string]Info{}, changed: map[string]bool{}, forced: forced}
	for id, record := range records {
		x.nfo[id] = decode(record.NFO)
		x.online[id] = decode(record.Online)
	}
	items = sortItems(items)
	read, bad := 0, 0
	for _, item := range items {
		parsed, err := x.readNFO(item)
		if ctx.Err() != nil {
			return false
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
	for _, p := range x.people(items, nil) {
		known[p.id] = true
	}
	remove := []string{}
	for id := range records {
		if !known[id] {
			remove = append(remove, id)
			delete(x.records, id)
		}
	}
	// A new or removed identification drops what the old one fetched.
	for id, ids := range identify {
		if !known[id] {
			continue
		}
		record := x.records[id]
		record.ItemID, record.Identified, record.OnlineKey, record.Online = id, nil, "", nil
		if ids != nil {
			record.Identified, _ = json.Marshal(ids)
		}
		x.records[id], x.online[id], x.changed[id] = record, Info{}, true
	}
	if !x.save(ctx, remove) {
		return false
	}
	saved = true
	x.publishLibrary()
	if read > 0 || bad > 0 || len(remove) > 0 {
		slog.Info("Metadata files read", "nfo_read", read, "nfo_unreadable", bad, "removed", len(remove))
	}
	if r.Online() {
		return x.fetchAll(ctx, items)
	}
	return true
}

// sortItems puts series first, so seasons and episodes can use their
// series' provider IDs, then seasons, movies and episodes.
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
		b, info, err := x.files.ReadSidecar(item, name, maxNFOBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		revision = nfoVersion + "\x00" + name + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
		content = b
		break
	}
	record, found := x.records[item.ID]
	if found && record.NFORevision == revision && !x.forced[item.ID] {
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
// An item an administrator identified takes the providers' metadata first,
// with the chosen IDs.
func (x *run) info(id string) Info {
	if ids := x.identified(id); ids != nil {
		info := merge(x.online[id], x.nfo[id])
		info.ProviderIDs = union(info.ProviderIDs, ids)
		return info
	}
	return merge(x.nfo[id], x.online[id])
}

// identified returns the provider IDs an administrator chose for the item,
// or nil.
func (x *run) identified(id string) map[string]string {
	var ids map[string]string
	if body := x.records[id].Identified; len(body) > 0 {
		_ = json.Unmarshal(body, &ids)
	}
	return ids
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
	case "Season", "Episode":
		series := x.info(item.SeriesID).ProviderIDs
		if series["Tvdb"] == "" && series["Tmdb"] == "" {
			return lookup{}
		}
		key := prefix + idText(series) + "|" + strconv.Itoa(item.SeasonNumber)
		if item.Type() == "Episode" {
			key += "x" + strconv.Itoa(item.EpisodeNumber)
		}
		return lookup{key: key, series: series, season: item.SeasonNumber, episode: item.EpisodeNumber}
	}
	nfo := x.nfo[item.ID]
	l := lookup{ids: nfo.ProviderIDs, title: nfo.Name, year: nfo.Year}
	if l.title == "" {
		l.title, l.year = lookupName(item.Name)
	}
	// An identified item is looked up by the chosen IDs alone.
	if ids := x.identified(item.ID); ids != nil {
		l = lookup{ids: ids}
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
	if record.OnlineKey != key || x.forced[id] {
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

// fetchAll looks up the items that are due and reports whether every one
// was answered. A failed image download leaves its item to be fetched
// again, unless the image is missing or unusable at the provider.
func (x *run) fetchAll(ctx context.Context, items []media.Item) bool {
	var tm *tmdb
	var tv *tvdb
	if x.cfg.TMDbKey != "" {
		tm = newTMDb(x.cfg.TMDbKey, x.lang)
	}
	if x.cfg.TVDbKey != "" {
		tv = newTVDb(x.cfg.TVDbKey, x.lang)
	}
	images := &throttle{every: spacing.images}
	fetched, notFound, failed, failures, people := 0, 0, 0, 0, 0
	stopped := ""
	published := time.Now()
	forced := maps.Clone(x.forced)
	settle := func(id, key string, info Info, err error, now time.Time) bool {
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
			return false
		}
		if err != nil && !errors.Is(err, errNotFound) {
			return true
		}
		failures = 0
		if errors.Is(err, errNotFound) {
			notFound++
		} else {
			fetched++
		}
		delete(x.forced, id)
		record := x.records[id]
		record.ItemID, record.OnlineKey, record.FetchedAt = id, key, now
		record.Online, _ = json.Marshal(info)
		x.records[id], x.online[id], x.changed[id] = record, info, true
		if len(x.changed) >= 25 && !x.save(ctx, nil) {
			stopped = "metadata could not be saved"
			return false
		}
		// Rebuilding the library costs a pass over every item, so a long
		// lookup publishes what it has at intervals, not per batch.
		if time.Since(published) >= publishEvery {
			x.publishLibrary()
			published = time.Now()
		}
		return true
	}
	for _, item := range items {
		now := time.Now().UTC()
		l := x.lookupOf(item)
		if l.key == "" || !x.due(item.ID, l.key, now) {
			continue
		}
		info, urls, err := x.fetch(ctx, tm, tv, item, l)
		info, err = x.downloadImages(ctx, info, urls, err, images)
		if ctx.Err() != nil {
			x.save(ctx, nil)
			return false
		}
		if !settle(item.ID, l.key, info, err, now) {
			break
		}
	}
	for _, p := range x.people(items, forced) {
		if stopped != "" {
			break
		}
		now := time.Now().UTC()
		if !x.due(p.id, p.key, now) {
			continue
		}
		info, urls, err := x.fetchPerson(ctx, tm, tv, p)
		info, err = x.downloadImages(ctx, info, urls, err, images)
		if ctx.Err() != nil {
			x.save(ctx, nil)
			return false
		}
		if settle(p.id, p.key, info, err, now) && err == nil {
			people++
		}
	}
	if stopped == "" && !x.save(ctx, nil) {
		stopped = "metadata could not be saved"
	}
	x.publishLibrary()
	if stopped != "" {
		slog.Warn("Online metadata lookup stopped; it is tried again later", "reason", stopped)
	} else {
		x.removeUnusedImages()
	}
	if fetched > 0 || notFound > 0 || failed > 0 {
		slog.Info("Online metadata fetched", "items", fetched-people, "people", people, "not_found", notFound, "failed", failed)
	}
	return stopped == "" && failed == 0
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
	case "Season":
		if tv != nil && l.series["Tvdb"] != "" {
			sources = append(sources, func() (Info, map[string]string, error) { return tv.season(ctx, l.series["Tvdb"], l.season) })
		}
		if tm != nil && l.series["Tmdb"] != "" {
			sources = append(sources, func() (Info, map[string]string, error) { return tm.season(ctx, l.series["Tmdb"], l.season) })
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

// fetchPerson asks TMDb, then TheTVDB, for a person by the IDs their
// credits give. The photo of a credit wins over the providers', so a
// .nfo's thumb is used even when no provider knows the person.
func (x *run) fetchPerson(ctx context.Context, tm *tmdb, tv *tvdb, p personLookup) (Info, map[string]string, error) {
	type source func() (Info, string, error)
	sources := []source{}
	if tm != nil && p.tmdb != "" {
		sources = append(sources, func() (Info, string, error) { return tm.person(ctx, p.tmdb) })
	}
	if tv != nil && p.tvdb != "" {
		sources = append(sources, func() (Info, string, error) { return tv.person(ctx, p.tvdb) })
	}
	found := Info{}
	urls := map[string]string{"Primary": p.thumb}
	answered := p.thumb != ""
	for _, source := range sources {
		info, image, err := source()
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return Info{}, nil, err
		}
		answered = true
		found = merge(found, info)
		if urls["Primary"] == "" {
			urls["Primary"] = image
		}
	}
	if !answered {
		return Info{}, nil, errNotFound
	}
	found.ProviderIDs = p.ids()
	return found, urls, nil
}

// downloadImages saves the images of a lookup that succeeded into info. An
// image missing or unusable at the provider is left out; another failure
// fails the lookup.
func (x *run) downloadImages(ctx context.Context, info Info, urls map[string]string, err error, pace *throttle) (Info, error) {
	for _, kind := range []string{"Primary", "Backdrop"} {
		if err != nil || urls[kind] == "" {
			continue
		}
		var image Image
		image, err = download(ctx, x.cfg.ImageDir, urls[kind], pace)
		switch {
		case err == nil:
			if info.Images == nil {
				info.Images = map[string]Image{}
			}
			info.Images[kind] = image
		case errors.Is(err, errNotFound), errors.Is(err, errUnusable):
			err = nil
		}
	}
	return info, err
}

// personLookup is what to ask the providers for a person: the provider IDs
// and the photo URL of their credits, and the key of the answer.
type personLookup struct {
	id, key    string
	tmdb, tvdb string
	thumb      string
	forced     bool
	// ambiguous is set when credits of this name give different IDs of a
	// provider: people who share a name, whom Couch tells apart by name
	// alone.
	ambiguous bool
}

func (p personLookup) ids() map[string]string {
	ids := map[string]string{}
	if p.tmdb != "" {
		ids["Tmdb"] = p.tmdb
	}
	if p.tvdb != "" {
		ids["Tvdb"] = p.tvdb
	}
	return ids
}

// people lists the people the items credit, once each, with the provider
// IDs and the first https photo URL of their credits in item order: a
// .nfo's before a provider's. A person whose credits give neither, or give
// different IDs of a provider, is left out. People credited by an item of
// forced are marked to fetch again.
func (x *run) people(items []media.Item, forced map[string]bool) []personLookup {
	list := []personLookup{}
	index := map[string]int{}
	for _, item := range items {
		for _, credit := range x.credits(item.ID) {
			id := ID("Person", credit.Name)
			n, seen := index[id]
			if !seen {
				n, index[id] = len(list), len(list)
				list = append(list, personLookup{id: id})
			}
			p := &list[n]
			for _, pair := range []struct {
				have   *string
				credit string
			}{{&p.tmdb, credit.TmdbID}, {&p.tvdb, credit.TvdbID}} {
				switch {
				case *pair.have == "":
					*pair.have = pair.credit
				case pair.credit != "" && pair.credit != *pair.have:
					p.ambiguous = true
				}
			}
			if p.thumb == "" && strings.HasPrefix(credit.Thumb, "https://") {
				p.thumb = credit.Thumb
			}
			p.forced = p.forced || forced[item.ID]
		}
	}
	list = slices.DeleteFunc(list, func(p personLookup) bool {
		return p.ambiguous || (p.tmdb == "" && p.tvdb == "" && p.thumb == "")
	})
	prefix := strings.Join([]string{lookupVersion, "Person", x.cfg.Language, x.providers()}, "|") + "|"
	for n := range list {
		list[n].key = prefix + idText(list[n].ids()) + "|" + list[n].thumb
		if list[n].forced {
			x.forced[list[n].id] = true
		}
	}
	return list
}

// credits returns the item's cast and crew as info merges them, without
// merging the rest.
func (x *run) credits(id string) []Person {
	first, second := x.nfo[id].People, x.online[id].People
	if len(x.records[id].Identified) > 0 {
		first, second = second, first
	}
	if len(first) > 0 {
		return first
	}
	return second
}

// removeUnusedImages deletes downloaded images no item or person uses any
// more.
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
