# Go media server with Emby compatibility

## Problem

The owner needs a media server that uses fewer resources and runs more reliably than Emby while staying compatible with its clients. Improvements count only when measured against Emby on the same hardware, media and load ([BASELINE](BASELINE.md)).

## Users

|User|Need|
|---|---|
|Server owner|Manage library, access and load; recover after a failure|
|Viewer|Sign in with a familiar client, find media, watch and resume|
|Developer|Reproduce differences from Emby and verify fixes automatically|

## Goals

|ID|Outcome|Criterion|
|---|---|---|
|G-1|Compatibility|Mandatory scenarios of the [matrix](COMPATIBILITY.md) pass on every declared client version; partial support is marked|
|G-2|Fewer resources|Budgets in [ROADMAP](ROADMAP.md#budgets) met on every profile|
|G-3|Stability|[Stability criterion](ROADMAP.md#stability-g-3) met|
|G-4|Custom frontend option|A custom interface passes the agreed scenarios through the same backend|

## Scope

Reference: Emby Server and Emby Web 4.10.0.40. Mandatory clients: Emby Web, Android/Android TV, iOS/Apple TV. Target host: AMD Ryzen 7 255 with Radeon 780M, Linux, Docker. License: GPL-2.0 (#11).

Not planned: working on Emby's database directly. Migration of Emby users and history, cloud features and Live TV/DVR are separate decisions.

## Functional requirements

P0 is the first complete scenario, P1 compatibility expansion, P2 after the need is confirmed.

|ID|Requirement|Priority|Acceptance|
|---|---|---|---|
|FR-01|Client connects and recognizes capabilities|P0|Stable ServerId across restarts; required prefixes and responses match the contract|
|FR-02|Sign-in, tokens, sign-out, access control|P0|Revoked token refused; no access to another user's library, images or streams|
|FR-03|Index local movies and series|P0|Rescan keeps IDs, makes no duplicates; one bad file does not abort the walk|
|FR-04|Libraries, item pages, seasons, episodes, search, sort, paging|P0|Target client shows the catalog; empty, missing and boundary cases checked|
|FR-05|Local metadata and images|P0|Covers show; missing image handled; no stale cache after update|
|FR-06|PlaybackInfo picks a working method|P0|Matches client profile; URLs work; unsupported format gives a clear error|
|FR-07|Direct Stream, seek, cancel|P0|Range/HEAD checked; cancelled request releases resources|
|FR-08|Session and user state persist|P0|Progress, resume, played, favorite survive restart; repeated and late events handled|
|FR-09|Client-required settings, capabilities, events|P0/P1|WebSocket messages and reconnection checked|
|FR-10|Remux, HLS, transcoding|P1|Agreed formats pass play/seek/stop; FFmpeg ends on cancel; its errors reach the client without stopping the server|
|FR-11|Audio tracks and subtitles|P1|Track and language selection, subtitles off, external and embedded formats|
|FR-12|Series scenarios and catalog expansion|P1|Next Up, recently added, collections, filters match Emby|
|FR-13|Administration and recovery|P1|Library and user settings, tasks, diagnostics, backup and restore, with permissions|
|FR-14|Other Emby clients|P1|Results recorded per version and platform|
|FR-15|Custom frontend|P2|Sign-in, catalog, item page, player, settings pass the same scenarios|

## Non-functional requirements

|ID|Requirement|Check|
|---|---|---|
|NFR-01|Protocol semantics are compatible|Contract checks of paths, headers, types, IDs, dates, ticks, null vs missing, errors, events|
|NFR-02|Bounded resources|Explicit limits ([ROADMAP](ROADMAP.md#limits-nfr-02)); a reached limit gives a controlled error|
|NFR-03|Durable state|Forced kill and restart corrupt nothing confirmed; backup restores|
|NFR-04|Error isolation|Unreadable folder, broken file, full disk, FFmpeg crash, disconnect, DB error: no false success|
|NFR-05|Data protection|Hashed passwords; no secrets in logs or fixtures; paths confined to libraries; streams and images access-checked|
|NFR-06|Diagnostics|Logs explain a failed play and resource use, without secrets|
|NFR-07|Reproducible deployment|Go, FFmpeg and OS versions pinned; install and recovery checked on the target host|

## Risks

- Client versions differ in API and capabilities; compatibility is claimed only for the verified matrix.
- FFmpeg dominates playback cost; HTTP and API improvements do not reduce transcoding cost.
- The original frontend's source and terms are not established; the custom frontend does not assume access to them.
