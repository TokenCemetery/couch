# Design: library rescan without restart (#39)

Status: stages 1–3 implemented 2026-10-06, stage 5 (`LibraryChanged`) on 2026-10-07; stage 4 deferred. Decisions on the "Questions" section (2026-10-06): periodic scanning is off by default; a saved index is not needed for now; publishing an empty volume is cancelled; the date added is the time the path was first seen in the current process (at startup, mtime, until an index exists).

## Current state

- `src/cmd/coach/main.go` scans `-media-dir` before starting HTTP: sequentially, FFprobe up to 15 s per file, up to 10 minutes in total; on error the server does not start.
- The catalog (`media.Catalog`) lives in memory and is passed to `server.New`. Handlers read the `s.media` field 41 times in 6 files (`catalog.go`, `dto.go`, `home.go`, `images.go`, `playback.go`, `series.go`), sometimes several times per request.
- `Catalog` holds an open `os.Root` of the media directory; streams and posters are opened through it.
- An item ID is a hash of the absolute library path and the file's relative path; series and season IDs are a hash of the folder path. Rescanning the same paths yields the same IDs; user state (`state.json`) is stored by ID and does not depend on the catalog.
- `DateCreated` equals the file's mtime.

## Requirements (from #39)

1. Added, changed and deleted files become visible without a restart, without duplicates.
2. IDs of unchanged items are preserved; one broken file does not abort the traversal.
3. The catalog survives a forced stop.
4. Scanning during playback is separate, #30.

## Proposed solution

### 1. Catalog snapshot instead of a field

`Server` holds an `atomic.Pointer[media.Catalog]`. Each function takes one snapshot (`catalog := s.catalog()`) and uses only it, so a loop never indexes a swapped slice. Implemented per function rather than per request: a request that coincides with a publication may take two adjacent snapshots in different functions and briefly show inaccurate counts; this causes no errors. Catalogs are immutable after publication.

`os.Root` is opened once at startup and shared by all snapshots: the library path does not change, so the root need not be closed when a snapshot is replaced, and already open streams do not depend on which snapshot opened them.

This is a mechanical replacement of 41 accesses; behavior does not change. A separate stage with the existing tests.

### 2. Incremental scanning

`media.Rescan(ctx, previous)` traverses the directory the same way as now, but for a file with the same relative path, size and mtime it takes the FFprobe result from the previous snapshot instead of calling FFprobe. New and changed files are probed; missing ones drop out. IDs are computed as now, so unchanged items keep their IDs, and there are no duplicates by construction: the key is the path.

Broken files, as now, are skipped and counted. Traversal errors (unreadable root or subdirectory, exceeded limits, timeout) cancel the publication: the previous snapshot remains, and the reason is logged.

### 3. When to scan

- At startup: if a saved index exists (item 4), the server starts immediately with it and the scan runs in the background; without an index, as now, before starting HTTP.
- Periodically: the `-rescan-interval` flag (for example, `30m`; `0` means off).
- Manually: `SIGHUP` to the process. No HTTP route is proposed: Emby Web 4.10.0.40 does not call `Library/Refresh` (#34), library scanning there lives on the admin pages (#33), and Coach has a single user without those rights.
- At most one scan runs at a time; a scan request during an ongoing scan is deferred until it ends.

Watching file system events (inotify/kqueue) is not proposed in the first version: on NAS network volumes (SMB/NFS) it is unreliable and requires a dependency.

### 4. Saved index

After each successful publication the catalog is written to `<data>/catalog.json` atomically (temporary file, fsync, rename, like `state.json`). The format is versioned; an incompatible or corrupted version is ignored and leads to a full scan, not to a startup failure. The index stores FFprobe results and the time each path was first seen.

This closes requirement 3, speeds up startup and gives a real date added: `DateCreated` = the time the path was first seen, not mtime (Latest is sorted by date added, as in Emby). For files that already exist, the first run with an index uses mtime so the order does not flip.

### 5. Protection against a vanished volume

If the `-media-dir` root is readable but the new traversal finds no videos while the previous snapshot was non-empty, the publication is cancelled with a warning: this is what an unmounted NAS volume looks like, and deleting the whole library would be a mistake. User state is left untouched in any case.

### 6. Client notification

After publication Emby sends `LibraryChanged` over WebSocket (added, changed, removed IDs). Emby Web uses it to refresh open screens. Proposed as the last stage together with #25; without it, changes become visible after navigating between screens or reloading.

## Stages

| # | Stage | Verification |
|---|---|---|
| 1 | Catalog snapshot via `atomic.Pointer`, shared `os.Root` | existing tests, `-race` |
| 2 | `Rescan` reusing FFprobe results | unit tests with a fake probe: addition, change, deletion, ID preservation, failure on traversal error |
| 3 | Background scanning: interval, `SIGHUP`, one at a time, empty-volume protection | tests; browser: an added file appears without a restart |
| 4 | `catalog.json`: write, load at startup, first-seen date | tests for a corrupted and an outdated index; restart starts without waiting for FFprobe |
| 5 | `LibraryChanged` over WebSocket | browser: an open library refreshes (#25) |

Each stage is a separate commit that works on its own.

## Questions

1. Default interval: off (only `SIGHUP` and startup) or, for example, 30 minutes?
2. Is the saved index needed now (stage 4), or are stages 1–3 enough?
3. Empty-volume protection (item 5): cancel the publication, as proposed, or publish an empty catalog?
4. Date added: switch to the first-seen time (item 4) or keep mtime?
