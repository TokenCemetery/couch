# Roadmap

Status: draft, updated 2026-10-31. This is the work order and completion criteria, without unconfirmed calendar estimates. Requirements: [PRD](PRD.md); facts: [discovery](DISCOVERY.md).

M0 is partially done: the reference contract is captured (30 scrubbed fixtures), resource measurements remain open. M1 accepted: Emby Web works against Couch in the browser. M2 accepted 2026-10-03 on a synthetic H.264/AAC MP4: the browser passed "sign in → search → item page → play → seek → stop → restart → resume" for a movie and the TV series scenario. The media set is not agreed, the full local UI is not verified. M3 is in progress (see below), M4–M6 are not started. Verification boundaries: [VALIDATION.md](VALIDATION.md).

Mandatory clients (user's answer 2026-09-19): Emby Web (first), Android/Android TV, iOS/Apple TV. Kodi and DLNA are not part of the mandatory scope.

## M0. Pin down the compatibility target

Outcome: Emby/client versions, a list of scenarios, scrubbed exchange samples, a media set and baseline measurements.

- Connection verified: `http://192.168.1.11:8096/`, server/frontend version `4.10.0.40`. Public and authenticated responses captured; the browser scenario against Couch is verified.
- Choose the first client and the mandatory scenarios. Pin down how the web client connects to the new backend.
- Map requests against the Emby documentation and the Jellyfin sources. Record confirmed differences instead of assuming the APIs are identical.
- Measure RSS/CPU/I/O at idle, during scanning and during playback; API p50/p95 and start/seek time. Record the file set, cache, quality, acceleration and concurrency.
- Define resource budgets, load and the duration of the stability check.

Done when the scenario reproduces against the recorded contract, dependencies are known and numeric criteria are agreed. The contract is captured; baseline measurements and resource budgets are not defined yet.

## M1. Connection and sign-in through the Emby frontend

Depends on M0. Outcome: the first working Go server that the target client connects to.

- Minimal project structure, configuration, persistent storage, migrations, HTTP and diagnostic logs.
- System info, public users, sign-in, tokens, logout, the required capabilities/settings.
- Contract checks per M0 and a client scenario for sign-in, sign-out and reconnection.

Done when the unmodified target client signs in, shows a correct empty state, restores a valid session after restart and loses access after logout. Any auxiliary APIs are implemented according to observed need.

Accepted 2026-09-19. Unmodified Emby Web 4.10.0.40 in the browser connects to Couch, signs in, restores the session after restart and loses access after logout. A basic WebSocket was added later; browser check 2026-10-04: UserDataChanged reaches a second tab and after a Couch restart, client reconnection is lazy ([VALIDATION](VALIDATION.md)).

## M2. Library and first Direct Play

Depends on M1. Outcome: a vertical slice fit for limited use.

- Scanning a test folder, stable IDs, FFprobe, metadata and images.
- Library, item pages, search, movies/episodes and the required filters.
- PlaybackInfo, a stream with Range, play/seek/stop, saving the position and resume.
- Checks for permissions, an empty catalog, a missing/broken file, stream cancellation and restart.

Done when the client passes "sign in → search → item page → play → seek → stop → server restart → resume" for the agreed files. Direct serving works without reading the whole file into memory. This is not completion of full media compatibility.

Done for movies 2026-09-19: home screen (HomeSections and Sections/{id}/Items), library, item page, PlaybackInfo with negotiation by DeviceProfile, Direct Stream via `http.ServeContent` with Range/HEAD/416, progress, "played" and "favorite" marks, Continue Watching. The loop "sign in → library → item page → play → stop → server restart → resume" is verified; the position survives a forced restart.

Not done in this stage: localization of home screen strings, browser acceptance of TV series, of the fixed search and of full seek/resume on a long video. WebSocket supports UserDataChanged for watch state and favorites; other events are not implemented. Queues, logout, connection shutdown and event delivery to the original API client are verified. The catalog is in memory, there is no persistent index, DateCreated is taken from mtime. Seek is confirmed at the HTTP Range level; it was not checked in the interface on a long file.

Audit 2026-09-20–21: [gaps and results](M2-AUDIT.md). Fixed the Played/Favorite filters, Latest and Resume pagination, and position loss without PositionTicks; added a bounded durable replay window for PlaySessionId. The API client verified the Series/Season/Episode hierarchy, signed covers with revocation and full getSearchResults. The browser showed sign-in, home and a cover through `-web-upstream`; further acceptance was stopped by the tool's access limit. M2 is not closed: HTTP integration does not replace player acceptance.

Accepted 2026-10-03. Unmodified Emby Web through `-web-upstream`: search finds a movie and a TV series; on a 10-minute H.264/AAC file, seeking to 5:00 loads only the needed ranges; after stop and a Couch restart the movie resumes from the saved position 5:15. The TV series page shows the season and episodes, "Play" plays both episodes in a row, progress and the played mark are saved. Acceptance found and fixed `IsSpecialSeason` for seasons and the episode query parameters when playing a TV series. Open: the "recent searches" row (`SearchedItems`, `WasSearched`) implemented and verified in the browser 2026-10-04; home screen headings localized 2026-10-04 (ru, de, fr, es, it; #18); full local UI; media set. Details: [VALIDATION](VALIDATION.md).

Images (2026-10-31): besides Primary, local Backdrop, Logo and Thumb; seasons and episodes get the TV series images (`Parent*`, `SeriesPrimaryImageTag`), as in the reference responses. Verified in the browser on the container image (#85, #86, #88); image lookup does not slow down the screens of a large library (#87).

Additionally: local Emby Web via `-web-dir`; PlaybackInfo takes into account query/JSON, limits, source, audio track and basic CodecProfiles. Integration of API modules runs without a live Emby via `COUCH_WEB_DIR`. A standalone browser run with `-web-dir` was confirmed 2026-10-06, after four missing files were added to the copy (#19).

## M3. Playback on target devices

Depends on M2. Outcome: an agreed matrix of formats and clients.

- Remux/Direct Stream, HLS, transcoding, audio and subtitle selection.
- FFmpeg lifecycle management, job limits, releasing resources on stop/seek/disconnect.
- Required WebSocket events, reconnections and advancing to the next episode.
- Verification of the remaining target clients; hardware acceleration after the hardware is confirmed.

Done when every mandatory combination "client × container/codecs × tracks × delivery method" passes start/seek/resume/stop, and errors and limit overruns are predictable.

Status as of 2026-10-11:

- Delivery: remux and transcoding to H.264 over HLS with client profile constraints (size, bitrate, H.264 level with macroblock rate), quality and track selection keeping the position on change, subtitles (over HLS, inside the stream, as Emby does), advancing to the next episode. A restart from the middle matches a continuous pass, including for HEVC with open GOPs in MPEG-TS.
- FFmpeg: limits of 2 remux + 1 transcode, cleanup on stop/logout/idle; 30-minute sustained load with HLS and rescanning, with no process, file or memory leaks (RSS plateau at 25–27 MiB); the acceptance duration is not agreed (#15, #30).
- Events: UserDataChanged, LibraryChanged, UserConfigurationUpdated, ServerShuttingDown (#25).
- A walk through Emby Web screens (home, libraries with filters and sorting, item pages, search, player, menus, settings) with no API errors.
- The [matrix](COMPATIBILITY.md) contains only Emby Web in Chromium on a synthetic set. Not started: Android/Android TV, iOS/Apple TV (#26, #27), an agreed media set (#16), resource budgets (#15), hardware acceleration (#28).

## M4. Resources, recovery and operations

Depends on M2; the full media-load comparison depends on M3. Measurements are taken from M1 onward; this stage closes the overall criteria.

- Compare the Go server and Emby on the same test bench; show child processes and total cost separately.
- Check sustained load, repeated connections, scanning during play and no accumulation of processes/temporary files.
- Check forced shutdown, unavailable storage, full disk, backup/restore and data upgrades.
- Complete the required administrative scenarios and packaging for the chosen OS/NAS.

Done when the M0 budgets are met, recovery is verified, there are no open blocking defects and reproducible comparison results are published. Unverified scenarios are not declared stable.

Status as of 2026-10-31 (comparison with Emby not started, #29):

- Recovery ([summary](VALIDATION.md), #31): verified forced shutdown, storage error (read-only directory), corrupted state file, vanished and broken media file, FFprobe and FFmpeg failures, client disconnect. Open: power loss (needs a test bench) and a library unavailable at startup (#39).
- Backup/restore and data upgrades between versions are verified (#32).
- Sustained load: 30 minutes with no process, file or memory leaks; the acceptance duration is not agreed (#30, #15).
- Diagnostics: a playback refusal is logged with its reason (#84). Resource usage is logged separately: each FFmpeg job (#90), FFprobe per scan (#93), Couch itself and the sum of child processes at shutdown (#94). There is no separate metrics endpoint.
- First Couch measurements without comparison to Emby ([VALIDATION](VALIDATION.md)): Couch up to 12 MiB RSS; 1080p remux 2 s CPU and 47 MiB, 1080p → 720p transcode 57 s CPU and 453 MiB per 2 minutes of video; FFprobe about 35 ms CPU per file. Limiting FFmpeg threads halves transcoding memory under a CPU quota; `-ffmpeg-threads` sets the cap, automatic by default (#92).
- A container image with FFmpeg is built and checked in CI; administration and NAS packaging are not started (#33).

## M5. Compatibility expansion

Depends on the corresponding parts of M2–M4. Rank the remaining APIs, features and apps by real usage: music/photos, collections, Live TV/DVR, DLNA, device control, offline, external integrations and state migration.

Each capability goes through the cycle "record behavior → implement → compare → client check". The result is a versioned list of supported, partial and unsupported capabilities. The percentage of implemented routes does not replace the result of user scenarios.

## M6. Custom frontend

Starts after the API of the main M3 scenarios stabilizes; full M5 is not a required dependency.

- Agree on the set of screens and the expected similarity to Emby.
- Implement sign-in, home, catalog, item page, player and settings through the existing backend.
- Check user scenarios, responsiveness and keyboard control; include TV navigation if that client format is chosen.

Done when the agreed scenarios pass in the custom frontend and the checks of the original Emby clients keep passing.

## Compatibility matrix rules

Each row records: reference server version, client/version/platform, scenario, media, requests/events, expected result, check and status. Statuses: not investigated, contract recorded, implemented, verified, partial, blocked. The public API/HTML were read from Emby; sign-in, settings and the initial catalog were verified by its API client against Couch. Authenticated comparison with the Emby server, screens and playback remain open.

The contract is checked first, then the real client. Comparison allows only explicitly described transformations of dynamic values: IDs with relationships preserved, times and test bench addresses. Differences in types, required fields, event order, permissions and errors must not be hidden.
