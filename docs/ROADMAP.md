# Roadmap

Work order and completion criteria; requirements are in the [PRD](PRD.md), evidence in [VALIDATION](VALIDATION.md). Updated 2026-11-04.

Mandatory clients: Emby Web (first), Android/Android TV, iOS/Apple TV. Kodi and DLNA are out of scope.

|Milestone|Status|
|---|---|
|M0. Compatibility target|Done: contract captured, baseline measured, budgets approved (#15)|
|M1. Sign-in through Emby Web|Accepted 2026-09-19|
|M2. Library and Direct Play|Accepted 2026-10-03|
|M3. Playback on target devices|In progress: Emby Web done; Android and iOS not started (#26, #27)|
|M4. Resources, recovery, operations|In progress: open items below|
|M5. Compatibility expansion|In progress: [FEATURES](FEATURES.md)|
|M6. Custom frontend|Not started|

## M0. Compatibility target

Reference: Emby Server and Emby Web 4.10.0.40, captured fixtures in [fixtures/](fixtures/emby-4.10.0.40/), media set agreed (#16).

### Budgets

Each budget is Emby's figure on the [BASELINE](BASELINE.md) bench (small / average / large containers). Couch meets one when it is no higher on every profile, measured with `scripts/baseline.sh`.

|Phase|Measure|Budget (Emby)|Couch|
|---|---|---|---|
|Idle 60 s|anon. memory / RSS / CPU|67–70 MiB / 201–205 MiB / 0.13–0.36 s|met|
|Startup + scan, 18 files|wall / CPU / reads|3.4–5.1 s / 2.3–2.5 s / 25 MiB|met|
|API, seven endpoints|p95|0.8–4.8 ms|met|
|Direct Stream|CPU / memory / seek|0.53–0.74 s / 74–88 MiB / 1.9–2.7 ms|met|
|Remux, 120 s|CPU / memory / first segment / seek|1.9–2.0 s / 74–88 MiB / 0.32–0.34 s / 0.20–0.22 s|memory met; CPU +0.2–0.3 s (FFmpeg build); first segment over on small by 0.03 s; seek over on average and large by 0.01 s|
|Transcode HEVC 720p, 120 s|CPU / memory / reads / temp folder|33.7–34.8 s / 368 MiB / 39 MiB / 94–100 MiB|met|
||first segment / seek|0.47–1.39 s / 0.40–1.75 s|first segment met; seek over on large by 0.11 s (`libx264`) or 0.01 s (VA-API)|
|4K HEVC 10-bit|first segment / CPU / memory (large)|4.4 s / 91 s / 2602 MiB|met; also runs on small and average, where Emby runs out of memory|

Quality condition: a transcode change stays within 0.01 SSIM of the previous Couch encode on R06.

### Stability (G-3)

- Load: `COUCH_SOAK=4h COUCH_SOAK_RESCAN=75s`, 8 workers, 2 remux + 1 transcode at once, large profile.
- Pass: no request errors; no child processes, segments or files left (rotated logs aside); open files back to the start count plus at most 2 idle test-client connections; second-half RSS peak at most 5 % above the first half's. `TestSoak` enforces all of it. Met (#30).
- Acceptable progress loss after a forced stop: the last confirmed `Progress` report (every 10 s in Emby Web).

### Limits (NFR-02)

Scan: 10,000 videos, 100,000 entries, depth 64, 10 minutes. One FFprobe at a time, 15 s and 1 MiB output per file. 2 remux and 1 transcode sessions, then 503 `Retry-After`. 2 GiB of segments per session; 2 keyframe listings at once; idle sessions end after 60 s; 16 upstream requests for `-web-upstream`.

## M3. Playback on target devices

Done when every mandatory combination "client × container/codecs × tracks × delivery" passes start/seek/resume/stop, and errors and limit overruns are predictable.

- Emby Web: every file of the agreed set passes ([COMPATIBILITY](COMPATIBILITY.md)); the 4K source needs `-hwaccel vaapi` to play in real time (#97, #28).
- Open: Android/Android TV (#26), iOS/Apple TV (#27).

## M4. Resources, recovery and operations

Done when the budgets are met, recovery is verified, no blocking defects are open and comparison results are published.

- Met: recovery (#31, #122), backup/restore and upgrades (#32), sustained load (#30), diagnostics in logs (#84, #90, #93, #94, #113), administration in Emby Web (#33, #110–#116), container install on the target host (#114).
- Open: remux CPU and the small seek and first-segment gaps above; native install, reboot and LAN sign-in on the host (#117); a real power cut.

## M5. Compatibility expansion

Rank remaining features by real use (music, photos, collections, Live TV/DVR, DLNA, remote control, offline, external integrations, state migration). Each goes "record behaviour → implement → compare → client check". The result is [FEATURES](FEATURES.md).

## M6. Custom frontend

Starts once the API of the main M3 scenarios is stable. Done when sign-in, home, catalog, item page, player and settings pass the agreed scenarios in the custom frontend and the original Emby clients keep passing.

## Compatibility matrix rules

Each [COMPATIBILITY](COMPATIBILITY.md) row records client, version and platform, media, delivery method, the scenario result and status. Statuses: not investigated, implemented, verified, partial, blocked. Compatibility is claimed only for "verified". Contract first, then the real client; differences in types, required fields, event order, permissions and errors are never hidden.
