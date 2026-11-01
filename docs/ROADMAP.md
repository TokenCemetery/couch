# Roadmap

Status: draft, updated 2026-11-01. This is the work order and completion criteria, without unconfirmed calendar estimates. Requirements: [PRD](PRD.md); facts: [discovery](DISCOVERY.md).

M0 is partially done: the reference contract is captured (30 scrubbed fixtures) and the resource baseline is measured against Emby ([BASELINE](BASELINE.md)); the budgets are proposed and await approval (#15). M1 accepted: Emby Web works against Couch in the browser. M2 accepted 2026-10-03 on a synthetic H.264/AAC MP4: the browser passed "sign in → search → item page → play → seek → stop → restart → resume" for a movie and the TV series scenario. The media set is agreed since (#16) and its 18 real files are in the [matrix](COMPATIBILITY.md). M3 and M4 are in progress (see below); M5 has its first [feature list](FEATURES.md); M6 is not started. Verification boundaries: [VALIDATION.md](VALIDATION.md).

Mandatory clients (user's answer 2026-09-19): Emby Web (first), Android/Android TV, iOS/Apple TV. Kodi and DLNA are not part of the mandatory scope.

## M0. Pin down the compatibility target

Outcome: Emby/client versions, a list of scenarios, scrubbed exchange samples, a media set and baseline measurements.

- Connection verified: `http://192.168.1.11:8096/`, server/frontend version `4.10.0.40`. Public and authenticated responses captured; the browser scenario against Couch is verified.
- Choose the first client and the mandatory scenarios. Pin down how the web client connects to the new backend.
- Map requests against the Emby documentation and the Jellyfin sources. Record confirmed differences instead of assuming the APIs are identical.
- Measure RSS/CPU/I/O at idle, during scanning and during playback; API p50/p95 and start/seek time. Record the file set, cache, quality, acceleration and concurrency.
- Define resource budgets, load and the duration of the stability check.

Done when the scenario reproduces against the recorded contract, dependencies are known and numeric criteria are agreed. The contract and the baseline ([BASELINE](BASELINE.md), #14) are captured; the budgets below are proposed and not yet approved.

### Proposed budgets (2026-10-31, awaiting the owner's approval, #15)

Each budget is Emby 4.10.0.40's figure on the [BASELINE](BASELINE.md) bench: same host, the real media set, Emby Web's Chromium profile, and the small (1 CPU, 1 GiB), average (2 CPU, 2 GiB) and large (4 CPU, 4 GiB) containers. Couch meets a budget when its figure is no higher on every profile, measured with `scripts/baseline.sh`. Both columns are the sixth run (2026-11-01, Couch at b5c619e, Emby in the same run, no other load on the host; [BASELINE](BASELINE.md), "Sixth run"); ranges are over the three profiles, small / average / large where they differ.

| Phase | Measure | Budget (Emby) | Now (Couch) |
|---|---|---|---|
| Idle 60 s | anonymous memory / server RSS / CPU | 67–70 MiB / 201–205 MiB / 0.13–0.36 s | met: 4 MiB / 11–12 MiB / 0 s |
| Startup and scan of 18 files | wall / CPU / reads | 3.4–5.1 s / 2.3–2.5 s / 25 MiB | met: 1.2 s / 0.8 s / 18.6 MiB on small since FFprobe reads in 64 KiB blocks (BASELINE, "Scan reads"); 85 MiB before |
| API, seven endpoints | p95 per profile | Emby's p95, 0.8–4.8 ms | met since images are checked once per item (BASELINE, "List requests"); Movies on large (2.6 ms) and Latest on average (2.5 ms) equal Emby's at 0.1 ms resolution |
| API, 100 PlaybackInfo calls | reads | 0 MiB | not met: 185–192 MiB, the keyframe listing of R03, which needs a remux (#98) |
| Direct Stream | CPU / memory / seek to first byte | 0.53–0.74 s / 74–88 MiB / 1.9–2.7 ms | met: 0.32–0.33 s / 7 MiB / 0.6–0.7 ms |
| Remux, 120 s of video | CPU / memory / first segment / seek to 50 % | 1.9–2.0 s / 74–88 MiB / 0.32–0.34 s / 0.22 / 0.20 / 0.20 s | memory and first segment met (22 MiB, 0.24–0.34 s); CPU (2.1–2.2 s) and seek (0.31 / 0.21 / 0.21 s) not met, by up to 0.1 s |
| Transcode HEVC 720p, 120 s of video | CPU / memory | 33.7–34.8 s / 368 MiB | memory met (303–304 MiB); CPU not met (36.1–36.9 s, 4–9 % more) |
| | first segment / seek to 50 % | 1.39 / 0.79 / 0.47 s; 1.75 / 0.88 / 0.40 s | not met: 1.59 / 0.94 / 0.56 s; 2.02 / 1.08 / 0.66 s (#99) |
| | reads | 38.9–39.0 MiB | equal within 0.3 MiB: 39.0–39.2 MiB (#98) |
| | temp folder peak | 94–100 MiB | met: 3.9–4.9 MiB |
| 4K HEVC 10-bit, large | first segment / CPU / memory | 4.4 s / 91 s / 2602 MiB | CPU and memory met (91 s, 2584 MiB); first segment 5.0 s, within Emby Web's 10 s wait but not Emby's figure; plays in Emby Web below real time (#97) |

Picture quality is a condition, not a budget: a transcode change must stay within 0.01 SSIM of the previous Couch encode on R06, as measured in [VALIDATION](VALIDATION.md). The FFmpeg builds differ (Emby's 5.1, Couch's Alpine 8.1), so FFmpeg-bound budgets compare the whole container, which is what users pay for.

Stability (G-3), proposed:

- Load: the existing soak test (`COUCH_SOAK=8h COUCH_SOAK_RESCAN=75s`, 8 workers) with two remux sessions and one transcode running at once, which are Couch's limits, and a rescan every 75 s, on the large profile.
- Duration: 8 hours. Pass: no request errors, no child processes or segments left at the end, open files back to the start count, and RSS flat over the last 4 hours (no growth above the peak of the first 4).
- Acceptable progress loss after a forced stop: the last confirmed `Progress` report, which Emby Web sends every 10 s. This is what `forced-stop-keeps-confirmed-state` already checks.

Explicit limits (NFR-02), already in place: scan up to 10,000 videos, 100,000 entries, depth 64 and 10 minutes; one FFprobe at a time, 15 s and 1 MiB of output per file; 2 remux and 1 transcode sessions, then 503 `Retry-After`; 2 GiB of segments per session; 2 keyframe listings at once; idle sessions end after 60 s; 16 upstream requests for `-web-upstream`.

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

Status as of 2026-11-01:

- Delivery: remux and transcoding to H.264 over HLS with client profile constraints (size, bitrate, H.264 level with macroblock rate), quality and track selection keeping the position on change, subtitles (over HLS, inside the stream, as Emby does), advancing to the next episode. A restart from the middle matches a continuous pass, including for HEVC with open GOPs in MPEG-TS. In Emby Web every file of the agreed set that needs remux or HLS passes start, seek, stop and resume (#21), except the 4K source, whose transcode is too slow to start (#97).
- FFmpeg: limits of 2 remux + 1 transcode, cleanup on stop/logout/idle; an 8-hour sustained load with HLS and rescanning on the large profile, with no failures and no process, file or memory leaks (RSS 14.6–15.4 MiB throughout), [VALIDATION](VALIDATION.md); acceptance waits for #15 (#30).
- Events: UserDataChanged, LibraryChanged, UserConfigurationUpdated, ServerShuttingDown (#25).
- A walk through Emby Web screens (home, libraries with filters and sorting, item pages, search, player, menus, settings) with no API errors.
- The [matrix](COMPATIBILITY.md) contains Emby Web in Chromium on the synthetic set and on 18 real files from the agreed set (#16): 17 verified; the 4K HEVC source starts since 2026-11-01 on 4 CPUs but stalls, below real time (#97). Not started: Android/Android TV, iOS/Apple TV (#26, #27), hardware acceleration (#28).

## M4. Resources, recovery and operations

Depends on M2; the full media-load comparison depends on M3. Measurements are taken from M1 onward; this stage closes the overall criteria.

- Compare the Go server and Emby on the same test bench; show child processes and total cost separately.
- Check sustained load, repeated connections, scanning during play and no accumulation of processes/temporary files.
- Check forced shutdown, unavailable storage, full disk, backup/restore and data upgrades.
- Complete the required administrative scenarios and packaging for the chosen OS/NAS.

Done when the M0 budgets are met, recovery is verified, there are no open blocking defects and reproducible comparison results are published. Unverified scenarios are not declared stable.

Status as of 2026-11-01:

- Recovery ([summary](VALIDATION.md), #31): verified forced shutdown, storage error (read-only directory), corrupted state file, vanished and broken media file, FFprobe and FFmpeg failures, client disconnect. A library that is empty at startup keeps the saved catalog (#39). Open: power loss (needs a test bench).
- Backup/restore and data upgrades between versions are verified (#32).
- Sustained load: 8 hours on the large profile, 42.4 million requests with no failures and no process, file or memory leaks ([VALIDATION](VALIDATION.md), 2026-11-01). Against the proposed G-3 criteria the run rescanned hourly instead of every 75 s, and its RSS peak in the last 4 hours was 0.3 MiB above that of the first 4; the criteria wait for approval (#30, #15).
- Diagnostics: a playback refusal is logged with its reason (#84). Resource usage is logged separately: each FFmpeg job (#90), FFprobe per scan (#93), Couch itself and the sum of child processes at shutdown (#94). There is no separate metrics endpoint.
- Comparison with Emby on the same host, media and three container sizes ([BASELINE](BASELINE.md), #14, #29): Couch uses less at idle, in the API, Direct Stream and remux; transcoding CPU is within 10 % of Emby's and memory below it, but the first segment and seek are 2.5–3.5 times slower (#99), and Couch reads the source for its keyframe plan first (#98).
- Earlier Couch-only measurements ([VALIDATION](VALIDATION.md)): Couch up to 12 MiB RSS; 1080p remux 2 s CPU and 47 MiB, 1080p → 720p transcode 57 s CPU and 453 MiB per 2 minutes of video; FFprobe about 35 ms CPU per file. Limiting FFmpeg threads halves transcoding memory under a CPU quota; `-ffmpeg-threads` sets the cap, automatic by default (#92). Emby's x264 options cut transcoding CPU by a third at the same picture quality, for a larger file (#99).
- A container image with FFmpeg is built and checked in CI; administration and NAS packaging are not started (#33).

## M5. Compatibility expansion

Depends on the corresponding parts of M2–M4. Rank the remaining APIs, features and apps by real usage: music/photos, collections, Live TV/DVR, DLNA, device control, offline, external integrations and state migration.

Each capability goes through the cycle "record behavior → implement → compare → client check". The result is a versioned list of supported, partial and unsupported capabilities: [FEATURES](FEATURES.md), first version 2026-11-01 for Emby Web (#35). The percentage of implemented routes does not replace the result of user scenarios.

## M6. Custom frontend

Starts after the API of the main M3 scenarios stabilizes; full M5 is not a required dependency.

- Agree on the set of screens and the expected similarity to Emby.
- Implement sign-in, home, catalog, item page, player and settings through the existing backend.
- Check user scenarios, responsiveness and keyboard control; include TV navigation if that client format is chosen.

Done when the agreed scenarios pass in the custom frontend and the checks of the original Emby clients keep passing.

## Compatibility matrix rules

Each row records: reference server version, client/version/platform, scenario, media, requests/events, expected result, check and status. Statuses: not investigated, contract recorded, implemented, verified, partial, blocked. The public API/HTML were read from Emby; sign-in, settings and the initial catalog were verified by its API client against Couch. Authenticated comparison with the Emby server, screens and playback remain open.

The contract is checked first, then the real client. Comparison allows only explicitly described transformations of dynamic values: IDs with relationships preserved, times and test bench addresses. Differences in types, required fields, event order, permissions and errors must not be hidden.
