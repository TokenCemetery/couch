# Validation

What is verified, how, and where verification stops. How to run the checks: [README](../README.md#development). Formats and delivery per file: [COMPATIBILITY](COMPATIBILITY.md). Resource figures: [BASELINE](BASELINE.md).

Unless stated otherwise, browser checks use unmodified Emby Web 4.10.0.40 served by `-web-dir` in the built-in Chromium, against Couch on AMD Ryzen 7 255 / Linux.

## Reference contract

- Sanitized fixtures from the reference Emby 4.10.0.40: [fixtures/emby-4.10.0.40/](fixtures/emby-4.10.0.40/). Names, paths and opaque IDs are replaced by consistent aliases; structure, types, date formats and units are kept. No real ServerId, user names, NAS paths or tokens remain.
- Every capture is owner-authorized, read-only (GETs plus `PlaybackInfo`), on a dedicated device name, closed with `Sessions/Logout` and checked for token revocation (401). Later captures: `Localization/Cultures` (#55, served from `src/internal/server/cultures.json`), HDR fields (#125), collection images (#134), database settings from a local `emby/embyserver:4.10.0.40` (#129).
- The reference server now reports 4.10.2.0; Couch targets 4.10.0.40 until #126.

## Automated checks

|Check|Covers|
|---|---|
|`go test -race ./...`|Unit and integration tests, including FFmpeg-backed HLS tests (segment plan, restart equals continuous pass, open GOPs, IDR at each cut)|
|`make e2e`|Every `api/openapi.yaml` operation, strict known-failures map (`e2e/known_failures.go`), behaviour checks: HLS, WebVTT, Next Up, `forced-stop-keeps-confirmed-state`, `storage-error-is-reported`, `backup-restores-after-forced-stop`, multi-user|
|`scripts/check-emby-client.mjs`|Emby Web's own API modules (`apiclient.js`, fetch helper) against Couch: sign-in, catalog, search, PlaybackInfo, stream bytes and ranges, signed image URLs, WebSocket, restart|
|`TestSoak`|Stability criterion in [ROADMAP](ROADMAP.md#m0-compatibility-target)|
|CI|Lint, tests and e2e on FFmpeg 8.1, container build and `scripts/check-container.sh` on amd64 and arm64. The HLS tests also pass locally with FFmpeg 6.1 and 7.1|

E2E limits: Emby's schemas declare no `required`, so missing fields go unnoticed; only required parameters are sent; operations without a response schema are checked by status; WebSocket is outside the contract.

## Browser acceptance

All of these passed in Emby Web with no API response of 400 or above, unless noted.

|Area|Verified|Issues|
|---|---|---|
|Sign-in, session across restart, logout|M1 acceptance||
|Movie: search → item page → play → seek → stop → restart → resume|10-min H.264/AAC; seek loads only the needed ranges|M2|
|Series: season page, play, auto-advance, played marks, Next Up, mark series played||#40, #50, #53|
|Playback entry points: Resume, season Play, library Play/Shuffle, series Shuffle|||
|Screen walkthrough: home, libraries (all tabs, 32 sorts, filter dialog), item pages, search, settings pages||#47, #48, #76, #78, #79|
|Remux and transcode over HLS: start, seek, stop, resume, cleanup|Per file in [COMPATIBILITY](COMPATIBILITY.md)|#21, #22|
|Audio track switch, quality switch (position kept, reports under new IDs)||#24, #73|
|Subtitles: external SRT, embedded SubRip and `mov_text`, inside HLS, auto-selection, image subtitles burned in, undeliverable track skipped||#24|
|Rotated video (Direct Stream and transcode)||#22|
|WebSocket: `UserDataChanged` across tabs, `LibraryChanged` after rescan, `UserConfigurationUpdated`, `ServerShuttingDown`||#25|
|Rescan by `SIGHUP`: file added, removed, new library appears||#39, #75|
|Images: Primary, Backdrop, Logo, Thumb; series images on seasons and episodes; avatar upload and delete||#85, #86, #88|
|Metadata: `.nfo`, TMDb and TheTVDB live (en, ru); people from TheTVDB live|TMDb people and `<actor><thumb>` only against recorded answers|#101, #131|
|Collections, playlists, collection/playlist collages and uploaded images||#40, #103, #134|
|Music library, playback (incl. encoded ALAC/WMA/AIFF), Continue Listening, Instant Mix, embedded covers||#104, #119, #120|
|Home videos & photos, slideshow, photo playlists||#105|
|Home Screen settings: add, move, edit, remove, survive restart||#100|
|Database settings page||#129|
|`-hwaccel vaapi` and HDR tone mapping (libplacebo)||#28, #125|
|`connect-src 'self'` CSP blocks the Premiere check; the rest works||#89|

## Failure recovery (#31)

|Case|Evidence|Outcome|
|---|---|---|
|`SIGKILL` mid-write|e2e `forced-stop-keeps-confirmed-state`; `TestOpenRemovesInterruptedWrites`|Last confirmed position kept; interrupted temporary files removed at start|
|Read-only data directory|e2e `storage-error-is-reported`|500, change not published; writes succeed after recovery without restart|
|Real full disk (16 MiB tmpfs)|Container run|Writes answer 500, `/healthz` 200, state unchanged; survives `SIGKILL` while full; recovers without restart; no temp files left|
|Power loss|Write order audited under `strace` (#122)|Every file the database refers to is synced with its directory before the commit; caches before rename|
|Corrupted or newer state|`TestCorruptAndFutureStateAreNotReset`|Start fails; file not overwritten|
|Media file vanished after scan|Browser|404, "no compatible streams" in Emby Web, warning without path, state unchanged|
|Broken media file, FFprobe failure|`TestScanReadOnlyStableAndIsolated`, `TestProbeExecutionLimits`|File skipped, walk continues|
|Client disconnect mid-stream|`stream_test.go`|Handler exits, descriptor closed|
|FFmpeg error or `SIGKILL` in HLS|`TestFailedJobIsReported`, `TestKilledJobIsRestarted`|Waiting request gets 500; next request restarts the job; other sessions unaffected|
|Library empty or unmounted at start|`TestSavedCatalog`; container run|Saved catalog kept; first start without an index fails the scan|
|Backup, restore, upgrade|e2e `backup-restores-after-forced-stop`; Compose upgrade from 99c9d16 (#114)|Token, marks and avatar restored; an older database upgrades on start with token and marks kept|

## Sustained load (#30)

`TestSoak`, large profile (4 CPU, 4 GiB), 8 workers, remux ×2 and transcode ×1 at once, rescan every 75 s:

|Run|Requests / failures|Rescans|RSS after warm-up|2nd-half peak vs 1st|
|---|---|---|---|---|
|6 h (2026-11-02)|22.0 M / 0|288|20.6–21.2 MiB|+0.8 %|
|2 h (2026-11-02)|7.4 M / 0|96|20.6–21.5 MiB|+2.0 %|
|8 h, hourly rescan (2026-11-01)|42.4 M / 0|7|14.6–15.4 MiB|+1.8 %|

No child processes, segments or new files left; open files back to the start count plus the test client's idle connection.

## Install (#114, #117)

Docker Compose on the target host (Ubuntu 26.04, Docker 29.9): fresh install, upgrade from an older schema, `SIGKILL` restart, backup and restore, VA-API image, real client addresses on the LAN. Native unit: `systemd-analyze verify` passes, `security --offline` rates 1.8.

## Emby Web behaviour worth knowing

Found by running the real client; each shaped the implementation.

- `SupportsDirectPlay` means "read `MediaSource.Path` directly"; HTTP delivery is `SupportsDirectStream`. `DirectStreamUrl` must carry the token and no `/emby` prefix.
- Compatibility is decided from each track's `Profile`, `Level`, `PixelFormat` and frame rate; without them playback is refused client-side.
- JSON bodies arrive as `text/plain` with `reqformat=json`.
- Errors are parsed as JSON only with exactly `Content-Type: application/json`; Couch answers `text/plain`, as Emby does.
- Item lists ask for `Limit` up to 5003; Couch reduces it to 1000 instead of refusing.
- Home rows come from `Sections/{id}/Items`, filled by the server; Continue Watching is driven by position, not `Played`.
- The TV Suggestions tab and series page call `Items/Resume` without `IncludeNextUp`; Emby still adds next episodes.
- After the socket closes, Emby Web reconnects lazily: on a later request error or playback start.
- Image URLs carry no token, hence signed `ImageTags`.

## Not verified

- Other clients: Android, Android TV (#26), iOS, Apple TV (#27), other browsers, Emby Web on TVs.
- A real power cut, drives that ignore flushes, NAS file systems (NFS, SMB).
- Native install run on the host, host reboot, sign-in from another LAN host (#117).
- `qsv`, `nvenc`, `amf` on their hardware; Intel and Nvidia GPUs.
- `-tonemap software` and a burned-in subtitle with tone mapping in Emby Web; Dolby Vision profile 5 on a real file.
- Real music collections, gapless playback; real camera photos, HEIC, RAW.
