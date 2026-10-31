---
status: DRAFT
documentType: PRD
phase: delivery
createdAt: "2026-09-18"
updatedAt: "2026-09-18"
related:
  - docs/DISCOVERY.md
  - docs/ROADMAP.md
---

# Go media server with Emby compatibility

## Problem

The user needs a Go media server that uses fewer resources and runs more reliably than Emby while staying compatible with its frontend and, where possible, other clients. Specific failures and baseline resource usage have not been measured yet. Without a reproducible comparison, an improvement cannot be considered proven.

Jellyfin serves as an accessible source of knowledge about media servers. Compatibility requirements are defined by the target Emby and client versions. Facts and sources are collected in the [discovery](DISCOVERY.md).

## Users

| User | Need |
|---|---|
| Server owner | Manage the library, access and load; restore the server after a failure |
| Viewer | Sign in with a familiar client, find media, watch and resume from the saved position |
| Developer | Reproduce differences from Emby and verify fixes automatically |

## Goals and measurement

| ID | Outcome | Baseline | Criterion |
|---|---|---|---|
| G-1 | Compatibility | The Emby server and the Emby Web HTML report `4.10.0.40`; other clients are not installed | All mandatory scenarios of the agreed matrix pass on every declared client version; partial support is marked explicitly |
| G-2 | Fewer resources | No Emby measurements | On the same hardware, media set and load, usage is compared by RSS, CPU, I/O and temporary files; numeric budgets are approved after measurement and before optimization |
| G-3 | Stability | Frequency and causes of Emby failures are unknown | Recovery checks and sustained load pass; duration, concurrency and acceptable progress loss are fixed before the test |
| G-4 | Option for a custom frontend | Emby Web scenarios are not recorded yet | The custom interface passes the agreed user scenarios through the same backend |

Rewriting in Go does not by itself satisfy G-2/G-3. Resources of the Go process, FFmpeg and their sum are measured separately; encoding quality and hardware acceleration in the comparison are recorded.

## Scope

Confirmed by the user: requirements, roadmap, a Go backend for the Emby frontend, maximum compatibility and, where possible, reproducing the frontend.

First client confirmed by the user: Emby Web. Initial media `[assumed]`: local movies and TV series; the first playback scenario is Direct Play. These constraints set the verification order, not the final product boundary. Connection, sign-in and an empty library are implemented before the media slice. Client connection is verified without changing its code; the first adapter fetches `/web/` from its own Emby instance and serves the API in Go.

Later expansion: Direct Stream/remux, transcoding, tracks and subtitles, the remaining target clients, administration, a custom frontend.

Inventory for later stages: music, photos, collections, playlists, Live TV/DVR, DLNA, remote control, offline/download, plugins, external providers and cloud integrations. They are not excluded from maximum compatibility, but their priority is not confirmed. Migrating the Emby database and configuration is a separate decision.

## Functional requirements

Priorities: P0 — the first complete scenario; P1 — compatibility expansion; P2 — after the need is confirmed. Any dependency of a mandatory scenario is raised to that scenario's priority.

| ID | Requirement | Priority | Acceptance check |
|---|---|---|---|
| FR-01 | The client discovers/connects to the server and recognizes its capabilities | P0 | Connection by address, stable ServerId after restart; the prefixes and responses actually required match the contract |
| FR-02 | Local sign-in, tokens, sign-out, access control | P0 | Successful and failed sign-in; a revoked token does not work; a user does not get another user's library, images and streams |
| FR-03 | Indexing of local movies and TV series | P0 | A rescan creates no duplicates and keeps the IDs of unchanged items; an error in one file does not abort the whole walk |
| FR-04 | Libraries, item pages, seasons, episodes, search, sorting and pagination | P0 | The target client shows the test catalog; empty library, missing item, page boundaries and access restrictions are checked |
| FR-05 | Local metadata and images | P0 | Titles, duration and technical data are available; covers display, a missing image is handled; the cache does not serve a stale version after an update |
| FR-06 | PlaybackInfo chooses an available playback method | P0 | The response matches the client profile and actual capabilities; returned sources and URLs work; an unsupported format gives a clear error |
| FR-07 | Direct Play, seeking and cancellation | P0 | A supported test file plays, seek works; Range/HEAD and range boundaries are checked; a cancelled request releases resources |
| FR-08 | Session and user state are persisted | P0 | Play/progress/stop, resume, played and favorite work after restart; a repeated event does not corrupt state; behavior for late events is defined |
| FR-09 | Client-required settings, capabilities and events | P0/P1 | All dependencies of the first scenario are implemented; WebSocket messages and reconnection are checked if the client uses them |
| FR-10 | Remux, HLS and transcoding | P1 | The agreed set of containers/codecs passes play/seek/stop; processes terminate on cancellation; an FFmpeg error is visible to the client and does not stop the server |
| FR-11 | Audio tracks and subtitles | P1 | Track and language selection, disabling subtitles, external text and the required embedded formats work on the agreed clients |
| FR-12 | TV series scenarios and catalog expansion | P1 | Next up, recently added, collections and the needed filters match the agreed Emby scenarios |
| FR-13 | Administration and recovery | P1 | Library/user settings, background tasks, diagnostics, backup and restore are checked; operations require the corresponding permissions |
| FR-14 | Other Emby clients | P1 | Scenarios and results are recorded for each version and platform; network discovery and remote control are enabled as the client requires |
| FR-15 | Custom frontend | P2 | Sign-in, catalog, item page, player and settings pass the same scenarios; visual requirements are agreed separately |

Initial Direct Play does not count as full playback compatibility. Changing a priority requires updating the matrix and the criterion of the corresponding stage.

## Non-functional requirements

| ID | Requirement | Check |
|---|---|---|
| NFR-01 | Compatibility includes protocol semantics | Contract checks of methods, paths, headers, types, IDs, dates, ticks, null/missing fields, errors and significant events; differences are documented |
| NFR-02 | Controlled resource usage | Explicit limits for scanning, probing, transcoding and cache; when a limit is reached, a controlled queue/error; the server and child processes are measured |
| NFR-03 | State durability | After a forced kill and restart, saved users, catalog and confirmed state are not corrupted; a backup restores |
| NFR-04 | Error isolation | Inaccessible folder, broken file, full disk, FFmpeg crash, client disconnect, DB error are checked; the server does not report false success |
| NFR-05 | Data protection | Passwords are not stored in plain text; tokens/passwords do not end up in logs and fixtures; paths are restricted to allowed libraries; access checks also apply to streams and images |
| NFR-06 | Diagnostics | Logs and metrics are enough to determine the cause of a failed play and resource usage; diagnostic data is scrubbed of secrets |
| NFR-07 | Reproducible deployment | Go, FFmpeg and OS versions and the configuration are pinned; installation and recovery are checked on the target platform once it is chosen |

## Dependencies and risks

- The public API and HTML of the real Emby are available at `http://192.168.1.11:8096/` outside the sandbox. Authenticated scenarios are not checked yet; public documentation does not replace that check.
- A fixed test media set and an agreed test account are needed; source media are not modified during scanning.
- Client versions may differ in API and capabilities. A compatibility claim applies only to the verified matrix.
- FFmpeg and hardware acceleration affect overall load; HTTP/API improvements may not reduce transcoding cost.
- Availability of the original frontend's source and its terms of use are not established yet. The custom interface plan does not assume access to Emby sources.
- Migrating state requires explicit ID mapping rules. Working directly with the original Emby database is not planned.

## Open questions and assumptions

| Type | Question / assumption | How it is resolved |
|---|---|---|
| Confirmed | Mandatory clients: Emby Web, Android/Android TV, iOS/Apple TV. Kodi/DLNA are not mandatory | Direct answer from the user 2026-09-19; specific app versions are not pinned yet |
| Confirmed | The first client is the built-in Emby Web | Direct answer from the user 2026-09-18; browser acceptance passed 2026-09-19 |
| `[assumed]` | The first media are local movies and TV series | Checking real user scenarios |
| Question | What hardware, OS, library size and concurrency? | Environment inventory before choosing budgets |
| Question | Which specific failures and costs must be eliminated? | Measurements and reproducible cases before optimization |
| Question | Is migration of history/users and cloud features needed? | Separate assessment of scope and priority |
| Question | How is the product distributed and what code may be included? | License chosen: GPL-2.0 (#11). Component provenance is checked before inclusion |

Delivery order and stage verification: [ROADMAP.md](ROADMAP.md).
