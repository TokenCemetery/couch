# Validation of the implemented slice

Date: 2026-09-19. Environment: Go 1.27.1, macOS arm64, FFmpeg 9.0.1. The declared minimum Go 1.25 was not run separately. The Linux binary was not run.

## Lifted constraints

Two constraints from the previous session no longer apply:

- The reference Emby `http://192.168.1.11:8096/` **is reachable from the sandbox**. Previously the connection could not be established.
- The built-in browser **runs unmodified Emby Web 4.10.0.40**. Previously the browser runtime failed, and browser acceptance was impossible.

This made it possible, for the first time, to check the client as a whole rather than only the HTTP contract.

## Reference contract

The user authorized an authenticated capture (reply 2026-09-19, account `oleg`; all accounts have `HasPassword=false`). Only GET requests and one POST `PlaybackInfo` were made; sessions were closed via `/Sessions/Logout`, and token revocation was confirmed by a 401 response. The capture ran in three short sessions with the device `Couch Contract Capture`.

30 sanitized fixtures: [docs/fixtures/emby-4.10.0.40/](fixtures/emby-4.10.0.40/). Sanitization replaces names, paths and opaque identifiers with consistent aliases; structure, types, date formats and numeric units are preserved. Verified that no real ServerId, user names, NAS paths or tokens remain.

## Browser acceptance Emby Web → Couch

The unmodified client, served through the `/web/` adapter, works against Couch:

| Scenario | Result |
|---|---|
| Sign-in screen, user selection, password sign-in | passed |
| Home screen: My Media, Continue Watching, Latest Movies | passed |
| Movies library: tabs and movie cards | passed |
| Movie page: duration, video/audio tracks, container, size | passed |
| Playback: Direct Stream, 1280×720, `readyState=4`, to the end of the file | passed |
| Position saving, Continue Watching | passed |

This closes the M1 condition "the unmodified client shows correct state", which had remained open.

## Contract mismatches found and fixed

Each was found by the real client, not by assumption:

1. `GET /Users/{id}/HomeSections` was missing — the home screen was not built.
2. `GET /Users/{id}/Sections/{section}/Items` was missing — home screen rows are filled by the server, not the client.
3. `/Branding/Css.css` was served as `/branding/css` — the browser rejected the stylesheet by MIME.
4. The DTO lacked `Subviews`; `videos.js` calls `this.item.Subviews.includes(...)` unguarded, and the library screen crashed. `Subviews` is present only in the `/Users/{id}/Items/{id}` response, not in `/Views`.
5. `Policy.EnableMediaPlayback=false` — the client refused playback before contacting the server.
6. `SupportsDirectPlay=true` means the client reads the file via `MediaSource.Path`; for HTTP delivery the correct flag is `SupportsDirectStream`. With `SupportsDirectPlay=true` and no `Path`, the client was left without a URL ("no compatible streams").
7. `DirectStreamUrl` must not contain the `/emby` prefix: the client passes it through `apiClient.getUrl`, otherwise the result is `/emby/emby/...`.
8. The token must be in `DirectStreamUrl`: the media element does not send headers. The reference does the same and sets `AddApiKeyToDirectStreamUrl=false`.
9. The client decides compatibility by the track's `Profile`, `Level`, `PixelFormat` and frame rate; without them playback was rejected.
10. The Continue Watching row is driven by the saved position, not by the `Played` flag.

## HTTP-level checks

- PlaybackInfo: negotiation by `DeviceProfile`, `PlaySessionId`, `DirectStreamUrl`.
- Stream: full GET 200, HEAD 200, `Range` 206 with correct `Content-Range` at the start and middle of the file, out-of-range request 416, no token 401.
- Progress: `Sessions/Playing`, `/Progress`, `/Stopped` → 204; unknown item → 404.
- Durability: a 25% position survives a forced process restart; the session remains valid; the Resume row is filled after the restart.
- Completion: stopping at ≥90% marks the item played and resets the position; the item leaves Resume.
- Permissions: `PlayedItems`/`FavoriteItems` with another user's `userId` → 403.
- `go test -race ./...`, `go vet ./...` — passed.

## Limitations of the 2026-09-19 slice

Historical list; current results are in the 2026-09-21 checkpoint below.

- The browser acceptance above predates the WebSocket implementation. `/embywebsocket` now supports the handshake, KeepAlive, ping/pong and UserDataChanged; automated tests pass, but the Emby Web screens were not rechecked.
- Images (`/Items/{id}/Images/...`) are not implemented: cards show placeholders.
- Home screen row titles are returned in English; the reference localizes them by `X-Emby-Language`.
- The service worker is not registered (`/web/serviceworker.js` is served by the adapter, scope `/web/`).
- Seeking through the UI on a long file: the mechanism is confirmed at the HTTP `Range` level, but in the UI it was checked on a 20-second clip that finished too quickly.
- Series, transcoding, subtitles, other clients (Android/Android TV, iOS/Apple TV), resource comparison with Emby, sustained load and target NAS hardware.

## Automated recheck: HTTP stream (2026-09-19)

Two file delivery defects were fixed:

- A global `WriteTimeout=30s` limited the entire transfer. The deadline is now renewed before each block; a stalled write is limited to 30 seconds. Settings of the other HTTP routes are unchanged.
- The ETag depended only on the movie ID and survived content replacement. Size and mtime are now read from the open file; the ETag is weak, not a content hash. `If-Range` with such an ETag returns the full file, not a partial response based on an unreliable validator.

The behavior relies on [ResponseController.SetWriteDeadline](https://pkg.go.dev/net/http#ResponseController.SetWriteDeadline) and [ServeContent](https://pkg.go.dev/net/http#ServeContent). Replacing/modifying a file during transfer is not supported; matching size and mtime do not guarantee identical content.

Checked in this run:

- `go test -race -cover ./...`: passed; coverage media 73.9%, server 60.9%, state 70.8%, cmd 0%. This is Go test coverage, excluding the external Node scenario.
- `go vet ./...`, native macOS arm64 build and Linux amd64 cross-build (`CGO_ENABLED=0`): passed. The Linux binary was not run.
- Stream regressions: GET/HEAD, ranges from the start/middle/end, 416, 304, validator update after file change, safe refusal of a weak `If-Range`.
- A loopback test with slowed writing exceeds the 100 ms global HTTP timeout without losing bytes. Separately checked: deadline renewal on each block, deadline-setting error, and timeout of a blocked write via `net.Pipe`. Timeouts were reduced for test speed; this is not a sustained load check.
- `COUCH_CHECK_MEDIA=1 node scripts/check-emby-client.mjs`: passed with Emby Web 4.10.0.40. The original API modules check sign-in, catalog and settings; additional HTTP checks cover PlaybackInfo, SHA-256 of the served video, HEAD/Range/416/401, position/Resume after a normal restart, and playback completion.

The integration test used temporary Couch data and a synthetic video. Only static resources were read from Emby, without authentication and without modifying its data. The browser UI was not checked in this run.

## PlaybackInfo and local Emby Web (2026-09-19)

Added `-web-dir` for the provided copy `../emby-webui`. The apiclient/fetchhelper/querystring API modules match the 4.10.0.40 reference by SHA-256. The original index.html receives data-appversion only in the HTTP response; UI files are neither modified nor copied into Couch.

Checked:

- Local static files: HTML/JS MIME, GET/HEAD/Range, loader version, integrity of the original index.html, API working alongside static files. Rejected: path traversal, symlinks pointing outside, hidden files, FIFOs, directories, unsupported extensions and methods.
- PlaybackInfo: query and JSON, case of query names, conflicting/repeated parameters, invalid numbers and encoding, another user's UserId, source, track validity, choosing the default audio instead of the first track, Direct Stream disallowed, bitrate and channel limits.
- Profile: empty format list, codec/container conditions, ApplyConditions, H.264 level/profile, resolution, fps, audio channels, sample rate, IsSecondaryAudio and unknown required/optional metadata. Boolean/string IsRequired and numeric/string Value from Emby Web are supported.
- Integration: `COUCH_WEB_DIR=../emby-webui COUCH_CHECK_MEDIA=1 node scripts/check-emby-client.mjs` passes without requests to the production Emby. The original getPlaybackInfo checks the positive scenario and NoCompatibleStream with Direct Stream disallowed, low bitrate, width limit and empty profile. Checks of video bytes, Range, restart, Resume and playback completion are retained.
- `go vet ./...`, macOS arm64 and Linux amd64 builds: passed. The Linux binary was not run.
- Final `go test -race -cover ./...`: passed; media 73.9%, server 75.6%, state 70.8%, cmd 0%.

Limitations: this is not a full model of the Emby DeviceProfile. HDR and other missing metadata are not inferred from assumptions; rotation is read from FFprobe since 2026-10-11 (#22). Without a profile, discovery mode remains; an average bitrate from size/duration does not guarantee the peak limit is respected. A non-first audio track that the client profile does not accept in the source file (`IsSecondaryAudio`) is served via remux to HLS; without FFmpeg such a choice does not play. Selected subtitles that Couch cannot serve as WebVTT (image-based, no FFmpeg) are skipped: the video plays without them.

Browser acceptance of the new changes was not performed: the browser skill could not connect to the built-in browser, and the diagnostics interface was also unavailable. API module checks do not confirm rendering, the service worker, track switching or playback in the browser. The historical acceptance above refers to the previous slice.

## UserDataChanged events (2026-09-20)

The contract was checked against the [Emby WebSocket documentation](https://dev.emby.media/doc/restapi/Web-Socket.html) and the provided UI: apiclient.js passes the message to handlers, common/input/api.js checks UserId and parses UserDataList, the state buttons and itemsrefresher.js match entries by ItemId. The reference Emby was not modified for the sake of events; broadcast semantics were not recaptured on it.

Broadcasting is implemented after a successful save of favorites, played state and progress. Checked:

- Two sessions of the same user receive updates; a synthetic recipient with a different UserId is excluded. This is a routing check, not declared multi-user support.
- Concurrent updates are delivered in write order. A save error or a forbidden request publishes no event.
- A slow queue is limited to 32 messages; overflow closes only that connection and does not affect another recipient.
- Logout closes all connections of its session while keeping the other one. An expired session does not receive a pending event. Stopping the API closes the sockets; handlers exit. A repeated Close is safe.
- The original ApiClient 4.10.0.40 opens a real WebSocket, receives favorite/progress/playback completion events, reconnects after a restart, and loses the connection after logout from another client instance with the same token.
- Targeted Go event tests passed three times. The final `go test -race -cover ./...` passed: media 73.9%, server 78.8%, state 70.8%, cmd 0%. `go vet ./...`, macOS arm64/Linux amd64 builds and Node integration with the local UI passed; the Linux binary was not run.

Boundaries: DOM/screens and the player were not checked in this run. Other server events, remote control and redelivery after reconnect are not implemented. The integration used only a temporary Couch, the local UI and a synthetic video; the production Emby was not involved.

## Checkpoint after the M2 audit (2026-09-21)

Automated checks of the current slice:

- `go test -race -cover ./...`: passed; media 83.1%, server 82.2%, state 71.7%, cmd 0%. Coverage excludes the external Node scenario.
- `go vet ./...`, native macOS arm64 build, Linux amd64 cross-build (`CGO_ENABLED=0`) and `node --check scripts/check-emby-client.mjs`: passed. The Linux binary was not run.
- `COUCH_WEB_DIR=../emby-webui COUCH_CHECK_MEDIA=1 node scripts/check-emby-client.mjs`: passed, without the production Emby. The original ApiClient checks the full getSearchResults with ItemTypes, series/seasons/episodes, PlaybackInfo, WebSocket and signed cover URLs. HTTP checks confirm video/cover bytes, Range, denial of access without a signature, link persistence after restart and revocation after logout.
- Go regressions: filters by saved UserData, Resume pagination, missing/negative position, PlaySessionId window robustness to repeats and restart, series hierarchy; ItemTypes respects search/filters but not pagination, requires authentication and rejects invalid boolean parameters.
- Image checks: limits on files/headers/sizes, symlinks pointing outside and FIFOs, forged signatures/signatures transferred to another object, expiry/logout, revision change, HTTP cache. Separate targeted image tests passed without skips.
- The real HTTP stream cancellation test passed three times: the handler exits and closes the file; a missing file returns 404. A 64 MiB sparse file was used, not loading the whole file into RAM.

Browser check:

- The installed Chrome is not connected to the tool; the built-in browser was used.
- In `-web-dir` mode loading stops: the provided copy lacks `modules/apphost.js`. UI files were not modified. Three API modules matching the reference does not prove the distribution is complete.
- Via `-web-upstream`, unmodified Emby Web signs in to a temporary Couch and shows Movies/TV Shows, Latest Episodes and the local movie cover via a signed ImageTag. The production Emby served only static resources; authentication and the catalog were handled by Couch.
- Search revealed a missing ItemTypes and the rejection of GroupProgramsBySeries. The backend was fixed, and getSearchResults passed API integration. The page was not reloaded after the fix: the tool's automatic permission check hit its usage limit. This is a tool limitation, not a search acceptance result.
- The player was not checked in this run. The full "search → item page → play → seek → stop → restart → resume" scenario on a long H.264/AAC file and the series scenario remain open. The temporary browser server was stopped.

M2 remains open. There are no comparative resource measurements, no acceptance of all codecs, and no standalone run of the full local UI. The historical results above refer to their own slices; the current summary is the [M2 audit](M2-AUDIT.md).

## M2 browser acceptance (2026-10-03)

Environment: Go 1.27.1, macOS arm64, FFmpeg 9.0.1, built-in browser. Couch was started with `-web-upstream http://192.168.1.11:8096`, a separate data directory and a temporary media folder; the production Emby served only static resources. Media are synthetic: `Long Movie.mp4` (600 s, H.264 640×360 + AAC, 134 MB, faststart) with a poster, and `Test Show/Season 01` with two 60-second episodes.

Passed in unmodified Emby Web 4.10.0.40:

- Sign-in, home (Movies/TV Shows, Latest Movies with a local poster, Latest Episodes), session persistence after each Couch restart.
- Search: suggestions and the full page with tabs find the movie, the series and the episodes.
- Series: TV Shows library → item page (season, episodes) → "Play". Episode 1 plays via Direct Stream (206); after it ends, the client moves on to episode 2. PlaybackInfo 200, start/progress/stop — 204. Episode 1 is marked played, episode 2 appeared in Continue Watching.
- Movie: search → item page (codecs, size, duration) → play → seek to 5:00. The browser buffer contains only [0–23] and [300–331] s, i.e. the file is not downloaded in full. Stop saved position 5:15.
- Couch restart → Continue Watching shows the movie with progress → "Resume" continues from 5:15.

Found and fixed:

- `GET /Shows/{id}/Seasons?IsSpecialSeason=false` was rejected (`UnsupportedQuery`), and the series page did not open. The parameter now filters out Season 00.
- "Play" on a series requests `Items` with `Filters=IsNotFolder`, `IsStandaloneSpecial=false`, `ExcludeLocationTypes=Virtual`, `CollapseBoxSetItems=false` and received 400. Folder and special filters are implemented (without data on special placement, each Season 00 episode counts as standalone). Excluding Virtual/Offline/Remote and collapsing collections are no-ops, since Couch has no such objects; other values are rejected.

Not implemented: search history. `POST /Users/{id}/SearchedItems` returns 404, and `Items?WasSearched=true&SortBy=DateLastSearched` returns 400. The client does not wait for the first request; the empty search page stays empty without an error message. On 2026-10-04 search history was implemented and checked in the browser (`-web-upstream`, synthetic files): opening a movie from search results sends `SearchedItems` (204), the empty search page shows it under "Recently Searched", and the "Clear" button (`POST /Users/{id}/RecentlySearched/Delete`) clears the list; before this route was added the client showed a 404 error. Also noted there: the TV Shows "Suggestions" tab requests `Items/Resume?IncludeItemTypes=Episode&ParentId=…` rather than `Shows/NextUp`, so Next Up is not visible in this client; the premieres row (`MinPremiereDate`, `IsUnaired`) received 400; it now receives an empty 200 response, since Couch items have no premiere date, and is still hidden.

Automated checks after the fixes: `go test -race -cover ./...` passed (media 83.1%, server 82.4%, state 71.7%); `go vet ./...`, gofmt, Linux amd64 cross-build and `COUCH_WEB_DIR=../emby-webui COUCH_CHECK_MEDIA=1 node scripts/check-emby-client.mjs` passed. The Linux binary was not run.

Boundaries: one synthetic container and codec set; seek was performed by setting the video element position, not by dragging the slider; local `-web-dir` mode, other clients and resource measurements were not checked.

## Continue Watching and Next Up in the browser (2026-10-04)

Environment: unmodified Emby Web 4.10.0.40 via `-web-upstream`, Chromium, Russian UI (`X-Emby-Language=ru`), a synthetic movie and series Show S01E01–E03, a new local user.

- Home fills the row from `Sections/resume/Items`, not from `Items/Resume?IncludeNextUp=true`. Before the fix, after marking S01E01 played there was no Continue Watching row; after it, the row shows S1:E2.
- The "…" menu of a row card requests `Sections/resume/Items?Limit=5000` and received 400 with an `InvalidPagination` dialog; now `Limit` is reduced to 1000 and the menu opens without error. The menu contains "Download" and "Remove from "Continue Watching"".
- "Remove from "Continue Watching"" on the next episode sends `HideFromResume?Hide=true` (200); after a reload the row is gone.
- Home row titles stayed in English with the Russian UI (#18). Fixed the same day: with `X-Emby-Language=ru` home shows "Мои медиаданные", "Продолжение просмотра", "Новые эпизоды".

Avatar (2026-10-04, same setup): on the profile page a PNG file was passed to the file picker field via a `change` event, then the regular "Upload" button: `POST /Users/{id}/Images/Primary` — 204, then `GET .../Images/Primary?height=400&tag=…` without a token — 200, the avatar is shown. "Delete Image" sends `POST .../Images/primary/Delete` — 204; after a reload a placeholder is shown.

Not checked in the browser: file download (saving in the browser requires a separate permission), return of a hidden episode after replay (covered by a unit test), password change via the profile form: checked 2026-10-04 after the #46 decision — a request with an empty `CurrentPw` receives 400, the client shows "Sign-in error: Invalid username or password", and the user stays signed in.

## Screen walkthrough in the browser (2026-10-04)

Same setup. A `fetch` interceptor recording responses of 400 and above was installed on the page; walked through: home, movie playback, movie, series, season and episode pages, all tabs of the Movies and TV Shows libraries.

Found and fixed ([#47](https://github.com/TokenCemetery/couch/issues/47)): `IsSpecialEpisode` (400, series specials row), `LiveTv/Programs` (404), `Movies/Recommendations` (404, "Suggestions" tab), `SortBy=IsFolder,Filename` (400, the "Folders" tab was empty) and `SortBy=SeriesSortName,ParentIndexNumber,IndexNumber,SortName` (400, favorite episodes). After the fix the same route produced no responses of 400 and above; the "Folders" tabs show the movie and the series.

User settings pages ([#48](https://github.com/TokenCemetery/couch/issues/48)): "Display" requested `web/configurationpages` (404), "Notifications" requested `Notifications/Services/Configured` and then `Notifications/Types` (404). All three now respond with an empty list (200): there are no plugins or notification services. "Home Screen", "Playback" and "Subtitles" produced no errors.

The remaining screens produced no errors. The `#!/search` page, opened directly, stayed empty; search was accepted earlier via the header field and was not rechecked here.

## Movie playback in the browser (2026-10-04)

Same setup, a 20 s H.264/AAC movie. From the movie page, "Play" starts direct delivery; `PlaybackInfo` 200, `Sessions/Playing` and `Playing/Progress` 204, the video plays. Going back sends `Playing/Stopped` with `PositionTicks` and the same `PlaySessionId` (204); the 43% position is saved, and the page shows "Resume", a progress bar and "0:11 remaining". Playing again after resetting the position starts from 0. There were no responses of 400 and above.

The first attempt at the same scenario did not save the position (0 with `Stopped` 204); the request body was not recorded, and two retries did not reproduce the error: [#49](https://github.com/TokenCemetery/couch/issues/49).

## WebSocket in the browser (2026-10-04)

Same setup, two tabs of the same user on the movie page; "favorite" changes were sent both from a tab and from a separate client via the API (`POST /Users/{id}/FavoriteItems/{item}` and `.../Delete`).

- An open socket (`ApiClient.isWebSocketOpen()`) receives `UserDataChanged`: the favorite button in the second tab changes without a reload, including after a change made outside the browser.
- After a Couch restart, the open tab loses its socket and does not reconnect within 40 s. This is Emby Web 4.10.0.40 behavior: when the socket closes, the client only clears it, and `ensureWebSocket()` is called later — after connectivity is restored on a request error, or when playback starts. Calling `ensureWebSocket()` opens a socket with the saved session, and the next change reaches the tab again.
- A tab loaded before the server restart may therefore not receive events until it starts playback or is reloaded.

## Episode from its page and moving to the next (2026-10-04)

Same setup, 20 s episodes. "Play" on the S01E01 page hung on the loading indicator: the queue is built by a `Shows/{id}/Episodes?IsVirtualUnaired=false&IsMissing=false` request, which Couch rejected with 400 ([#50](https://github.com/TokenCemetery/couch/issues/50)). After the fix the episode plays; at the end the client sends `Playing/Stopped` with a position equal to the duration, requests `PlaybackInfo` for S01E02 and starts it on its own (`Sessions/Playing` for S01E02, 204).

## Playback entry points (2026-10-04)

Same setup, after the #50 fix. Each entry point was checked via the `Sessions/Playing` report and the video `currentTime`; there were no responses of 400 and above.

| From | Result |
|---|---|
| "Resume" on the movie page | starts from the saved position (8.7 s) |
| "Play" on the season page | S01E01 from the beginning |
| "Play" and "Shuffle" in the Movies library | the movie; from the saved position if there is one |
| "Shuffle" in the series menu | an episode from a random queue (the client shuffles) |
| Series page | "Resume" offers the started episode S1:E2 with the remaining time |

The TV Shows library has no "Play"/"Shuffle" buttons. "View missing episodes" was not opened; `Shows/Missing` is not implemented (#40).

## Error format (2026-10-04, #52)

For anonymous GETs without a token (user permission, without sign-in), the reference Emby responds with `text/plain`: 401 — `Access token is invalid or expired.`, 404 — a file-not-found message localized to the server language. Emby Web (`modules/common/responsehelper.js`) parses JSON only when `Content-Type: application/json` exactly, otherwise it shows the response text as is. Couch responds the same way: `text/plain` with a clear English message. In the browser, Emby Web's `getErrorInfo` for a Couch 401 gives the title "Sign-in error" and the text `Access token is invalid or expired.`.

## E2E against the OpenAPI contract (2026-10-04)

Environment: Go 1.27.1, macOS arm64, FFmpeg 9.0.2, Allure CLI 3.14.3. `make e2e` passes in about 20 seconds. 532 tests in total: 528 `api/openapi.yaml` operations and 4 behavioral checks.

- **92 passed.**
- **440 marked as expected failures** in `e2e/known_failures.go`. Almost all of them are 404 on unimplemented routes. Besides those:
  - 4 administrative operations `/Users/Query|Prefixes|ItemAccess|CopyDataOptions` give 403;
  - 2 behavioral checks fail for their own reasons: HLS and WebVTT — 404 (`Resume?IncludeNextUp` passes since 2026-10-04; WebVTT and 4 `Videos/…/Subtitles/…/Stream.{Format}` operations pass with embedded subtitle extraction, #24);
  - the first run found 4 implemented operations diverging from the contract ([#44](https://github.com/TokenCemetery/couch/issues/44)): the `OwnerId` type in ThemeMedia, the `SearchedItems/` route with a trailing slash, the bodies of `POST /UserSettings/{id}` and `/Users/{id}/Configuration/Partial`. In all four cases Emby Web 4.10.0.40 (`modules/emby-apiclient/apiclient.js`, `modules/thememediaplayer.js`) behaves like Couch, so the contract was fixed with an `x-couch-source` annotation.

Strictness of the map was checked manually:

- a removed entry turns an expected failure into a red test;
- an entry for a passing operation (`getSystemInfo`) fails the run, requiring its removal;
- an unknown key is caught by `TestKnownFailuresExist`;
- an artificial `/healthz` regression (a number instead of the `version` string) fails `getHealthz`.

`make e2e-report` built an Allure report; results have titles from `summary`, epic, feature and story labels, and steps with request and response attachments. Each expected failure is linked to an issue: #21, #22, #24, #32, #33, #34 or #40. The link is visible in Allure.

**Check boundaries:**

- Emby schemas have no `required`, so missing fields are not detected;
- requests fill in only required parameters; optional filters are not checked;
- 299 operations without a response schema are checked by status only;
- WebSocket is not part of the contract.

## "Next Up" on the series page (2026-10-04, #40)

Emby Web requests `Items/Resume?ParentId=<series>&IncludeItemTypes=Episode` without `IncludeNextUp`; the reference Emby adds next episodes in this case too. After Couch started doing the same, on the setup with S1:E1 and S1:E2 played, the series page shows "S1:E3 - Show.S01E03". The reference behavior with `IncludeNextUp=false` was not captured; in that case Couch does not add next episodes.

## "Mark played" on a series (2026-10-04, #53)

Same setup. The mark button on the series page in Emby Web: before the click, episodes are `true,false,false`, the series is not played, `UnplayedItemCount=2`; after it, all three episodes are played, the series is played, `UnplayedItemCount=0`; a second click unmarks all episodes (`UnplayedItemCount=3`). `Shows/Missing` via `ApiClient.getMissingEpisodes` returns an empty page; the "View missing episodes" menu item itself was not opened: the browser pane was hidden, and a scripted click on the menu item did not work.

## Forced stop (2026-10-04, #31)

The behavioral check `forced-stop-keeps-confirmed-state` in `make e2e` sends `Sessions/Playing/Progress` with an increasing position back to back, kills the process with `SIGKILL` after 300 ms and starts it on the same data. After the restart the server starts (the state file is read), the token is valid, and the saved position is not less than the last one confirmed by a 204 response and not greater than the last one sent. During debugging, 34–38 reports were confirmed within 300 ms; in two runs out of three `SIGKILL` landed during a write in progress, and that write turned out to be saved. The check does not simulate a power loss: the OS cache is not lost on `SIGKILL`, so it does not verify `fsync`.

## File vanished after scan (2026-10-04, #31)

`-web-upstream` setup; the movie file was temporarily renamed after a scan (as with an unmounted NAS volume). PlaybackInfo still offers a direct stream, the stream request receives 404 `The media file is unavailable.`, Emby Web shows "Playback error: there are currently no compatible streams…" and does not hang. The saved position (7.6 s) and the "played" mark did not change. The server logs `WARN Media file unavailable item=<id>` without the file path. The file was put back.

## State write error (2026-10-04, #31)

The behavioral check `storage-error-is-reported` in `make e2e` makes the data directory read-only (as with a full disk or a read-only remount) and sets a "favorite" mark: the response is 500 `StorageError`, and the item does not become a favorite. After permissions are restored, the same mark succeeds with 200 and is saved; the server keeps running without a restart. Run as root, the check is meaningless (directory permissions are ignored) and reports that as an error. A real ENOSPC is reproduced in the section "Full disk" below.

## Failure recovery: #31 summary (2026-10-04)

| Case | Checked by | Outcome |
|---|---|---|
| Forced stop | e2e `forced-stop-keeps-confirmed-state`; `TestOpenRemovesInterruptedWrites` | the confirmed position is kept, temporary files of interrupted writes are removed at startup |
| Storage error / full disk | e2e `storage-error-is-reported`; `TestFailedWriteDoesNotPublishState`, `TestStateSizeLimitDoesNotChangeCommittedData` | 500 without publishing the change; after recovery, writes succeed without a restart. Real ENOSPC: section "Full disk" (2026-11-02) |
| Corrupted state file | `TestCorruptAndFutureStateAreNotReset` | startup fails, the file is not overwritten |
| File vanished after scan | browser, section above | 404, playback error in Emby Web, log warning, state unchanged |
| Broken media file | `TestScanReadOnlyStableAndIsolated` | skipped with a counter, the walk continues |
| FFprobe failure | `TestProbeExecutionLimits`: output overflow, hang, killed by signal | probe error, the file is skipped |
| Client disconnect during a stream | `stream_test.go`: a canceled stream releases the file | the handler exits, the descriptor is closed |
| FFmpeg failure in HLS | `TestFailedJobIsReported`: FFmpeg error; `TestKilledJobIsRestarted`: killed by `SIGKILL` (2026-10-11) | the request waiting for a segment receives 500, the next request restarts the job, other sessions keep working |
| FFmpeg failure during subtitle extraction | `TestEmbeddedSubtitleExtraction`: no track, all jobs busy | the subtitle request receives 404 and a log warning; the video stream is a separate request and is not affected |
| Power loss | write order audited and traced with `strace`, section "Write order for a power loss" (2026-11-02, #122) | every file the database refers to is synced with its directory before the commit; caches are synced before the rename. Not run on a VM or hardware: `SIGKILL` does not lose the OS cache |
| Library unavailable at startup | `TestSavedCatalog`; container run, section "Saved catalog index" (2026-11-01) | with a saved index, an empty volume keeps the saved catalog published and the index unchanged; without an index (first start) or with a missing directory, startup still fails with a scan error |

## Full disk (2026-11-02, #31)

Couch built from the working tree, run in the `couch:dev` image as root with `-data` on a 16 MiB tmpfs and one 5 s H.264 movie. After sign-in and a "favorite" mark, `dd` filled `/data` to 0 bytes free.

- With the disk full, "played", a `Sessions/Playing/Progress` report, an avatar upload and a sign-in each answered 500. `/healthz` answered 200, and the item stayed a favorite and unplayed. The log showed `ERROR state persistence failed`.
- `SIGKILL` with the disk still full: the restart loaded the database and the saved catalog, and the old token still worked. The rescan logged `Media index not saved … no space left on device`, and the activity log entry was dropped with `database or disk is full`.
- After the filler was removed, "played" answered 200 and the avatar upload 204, without a restart. After another `SIGKILL`, the item was a favorite and played, and the avatar was served.
- No temporary `.user-image-*` or `.catalog-*` file was left in `/data`.

## Write order for a power loss (2026-11-02, #31, #122)

`SIGKILL` keeps the OS page cache, so it cannot show what a power loss does. Instead, every write under `-data` was checked for the order that keeps a file's data on disk before any name or database row points at it:

| Data | Order |
|---|---|
| Database `couch.db` | SQLite with `journal_mode=DELETE` and `synchronous=FULL`: the journal and the database are synced at every commit |
| Catalog index, `catalog` table of `couch.db` (in `catalog.json` before #128) | the database commit, as above |
| Avatar `user-image-<rev>` | temporary file, fsync, rename, directory fsync, then the database commit |
| Metadata images `metadata/<hex>.{jpg,png}` | temporary file, fsync, rename, directory fsync, then the database commit |
| Embedded cover cache `covers/<rev>` | temporary file, fsync, rename. A lost rename is a cache miss |
| AAC audio cache `transcode/audio/<key>.m4a` | FFmpeg's `.part` output, fsync, rename. A lost rename is a cache miss |

Before #122, the last four paths had no directory fsync or no file fsync. The database could then refer to an avatar or image whose rename a power loss took back, or a cache could keep an empty file under its final name. The order was confirmed by running each path's tests under `strace -f -e trace=fsync,renameat`: `TestUserImageFailedChangeKeepsCommittedFile`, the `internal/metadata` tests, `TestEmbeddedCovers` and `TestAudioEncodesOnceAndKeepsRecentTracks`.

Not verified: a real power cut on a VM or hardware, a drive cache that ignores flushes, and NAS file systems (NFS, SMB), which may not honor a directory fsync.

## Data backup and upgrade (2026-10-11, #32)

The behavioral check `backup-restores-after-forced-stop` in `make e2e` follows the procedure from the README. After a "favorite" mark, Couch is stopped with `SIGTERM`, the data directory files are copied, and Couch is started. Then "favorite" is removed, "played" is set, the avatar is deleted, and the process is killed with `SIGKILL`. After that Couch is stopped again, the directory files are replaced with the copy, and it is started. The session token from the copy is valid, the movie is a favorite again and not played, and the avatar is served (200 `image/png`). If the replacement is skipped, the check fails on both conditions.

Upgrade: the first release (172bee0) was built from `git archive`, and used to run `-init`, sign-in, "favorite" and a position report. The current binary on this directory accepted the same token, showed the position and "favorite", and wrote a change. The written file is in `src/internal/state/testdata/state-172bee0.json`; `TestOpenFirstReleaseState` opens it, signs in, writes and opens it again. This check catches a rename of the `PositionTicks` field in JSON. There is no other schema version yet: a v1 → v2 migration will need to be checked on this same file.

## External subtitles in the browser (2026-10-04, #24)

`-web-upstream` setup; `Sample Movie.ru.srt` (CRLF, Cyrillic) was temporarily placed next to `Sample Movie.mp4`. After a scan, the item page shows a `ru SRT` track (`IsExternal=true`). Playback with the track selected: PlaybackInfo with `SubtitleStreamIndex=2` gives a direct stream, Emby Web adds a `<track>` linking to `stream.vtt`, loads both cues (0.5–5 s and 5.5–9 s) and shows "Проверка субтитров Couch" from 0.5 to 4.7 s; after 5 s the text disappears. The subtitle file was then removed.

## Embedded subtitles in the browser (2026-10-04, #24)

`-web-upstream` setup; `Subbed.mp4` with a `mov_text` track (rus, "Встроенные субтитры Couch", 0.5–5 s) was temporarily added to the media directory. Playback with `SubtitleStreamIndex=2`: direct stream, no error dialogs; Emby Web shows the cue from about 1 to 4.7 s; after 5 s the text disappears. The first appearance later than 0.5 s corresponds to FFmpeg extraction time. In `make e2e` the behavioral check `subtitle-webvtt` and 4 `Videos/…/Subtitles/…/Stream.{Format}` operations pass. The file was then removed.

## Language list for settings (2026-10-04, #55)

With the user's permission, the `GET /Localization/Cultures` response of the reference Emby 4.10.0.40 was captured: sign-in as `oleg` with a separate device `couch-capture-cultures`, one GET, `Sessions/Logout` (204), token revocation confirmed by a 401 response. The response (196 entries, only ISO 639 language codes and names) is stored unchanged in `src/internal/server/cultures.json` and served by Couch. In the browser, the "Subtitles" page shows 196 languages; selecting Russian saves `SubtitleLanguagePreference=ru`. The "Playback" page uses the same call for the audio language.

## Remux to HLS (2026-10-06, #21)

`-web-upstream` setup, Chromium. A synthetic MKV H.264 + AC3 5.1 (60 s) and an MKV H.264 + AC3 mono (10 minutes), keyframes every 2.6 s; added to the library via `SIGHUP`. The Emby Web profile allows MKV only for download, so PlaybackInfo returns `TranscodingUrl`; Emby Web loads `master.m3u8`, `main.m3u8` and `hls1/main/{n}.ts` segments (all 200), hls.js shows the full duration (59.94 and 600 s) and plays. After seeking to 420 s, the timestamp in the testsrc2 frame (00:07:05.920) matched the player position (425.93 s); starting from a saved position (~446 s) immediately restarts FFmpeg from the middle of the file. After `Sessions/Playing/Stopped` the `transcode` directory is empty and there are no FFmpeg processes; a session idle for a minute is removed too. AC3 is encoded to AAC (mono/stereo), video is copied.

Found and fixed: a segment request canceled by the player that reached the server after the request for the new position restarted FFmpeg backwards, and the new segment request received 500 (Emby Web closed the player). Now only the newest request triggers a restart; a unit test reproduces the ordering and catches a reverted check. Directory polling could not pause FFmpeg in time on a fast disk; readiness and pausing now follow the segment list that FFmpeg writes to a pipe.

Not confirmed: smooth picture after seeking. The browser pane was hidden, and Chromium does not render video frames in a hidden tab: the frame counter stood still while the position advanced. This was observed both before the fix and on a fully loaded buffer without restarts, so the hidden pane, not the stream, is considered the cause; to be checked with the pane visible. Not checked: files on a network volume (FFprobe reads the whole file on first start), HEVC, other clients (#26, #27). In `make e2e`, `hls-master-playlist` (FFprobe reads the whole stream) and 5 operations `Videos/{id}/master.m3u8`, `main.m3u8`, `hls1/...` pass.

## External subtitles with remux to HLS (2026-10-06, #21, #24)

`-web-dir ../emby-webui` setup, Chromium, browser pane visible. A synthetic `Subbed.mkv` (H.264 + AC3 mono, 60 s, keyframes every 2 s) with `Subbed.srt` next to it: 30 cues "Couch cue N" of 1.5 s each, starting every 2 s. On the item page the `SRT` track is selected, "Play": the MKV plays via remux to HLS (hls.js, `blob:` source), the client adds a `<track>` with `subtitles/2/stream.vtt` and loads all 30 cues; Emby Web draws them in its own `.videoSubtitles` layer. Sync was checked against the timestamp in the `testsrc2` frame: at 14.4 s cue 8 (14–15.5 s) is active; after seeking to 42.2 s — frame 46.200, `currentTime` 46.17, cue 24 (46–47.5 s); after seeking to 50.3 s the screenshot shows frame 51.280 and the text "Couch cue 26" (50–51.5 s). Seeking restarts FFmpeg; there is no subtitle offset. Playback reached the end; no errors in the Couch log.

This closes the open item of the "Remux to HLS" entry: with the pane visible, frames are rendered after seeking (the timestamps in the frame advance together with `currentTime`).

Not checked: subtitles inside HLS (`ManifestSubtitles`, the `subtitles.m3u8` playlist) — Couch did not serve them then, and the client used external WebVTT. They are served since 2026-10-11, see "Subtitles inside HLS".

Embedded subtitles: `Embedded.mkv` with the same 30 cues as a SubRip track (`rus`) inside the MKV. The track is selected, playback via remux to HLS; Couch extracts the track with FFmpeg to WebVTT, the client loads 30 cues; after seeking to 30.3 s the screen shows "Couch cue 16" (30–31.5 s).

Observed once and not reproduced: after seeking from 9.8 to 30.3 s, the picture froze on frame 9.840 (`getVideoPlaybackQuality` — 251 frames per session), while `currentTime` ran about 2.7 times faster than real time with a buffer of 0–59.94 s; seeking back 3 s restored frame output. Repeated in three new sessions (7 seeks, including from 9.9 to 30.3 s, back by 10 s and to 48 s) — each time about 25 frames per second and normal time progression. The cause was not found; it resembles the earlier observation in the "Remux to HLS" entry.

## Subtitles inside HLS (2026-10-11, #21)

Reference Emby 4.10.0.40 (read-only): with HLS, text subtitles for Emby Web come with `DeliveryMethod=Hls`, `TranscodingUrl` contains `SubtitleStreamIndexes` and `ManifestSubtitles=vtt`, `master.m3u8` contains an `EXT-X-MEDIA:TYPE=SUBTITLES` group linking to `subtitles.m3u8`, and WebVTT segments are 6 s each with `X-TIMESTAMP-MAP`.

`-web-dir ../emby-webui` setup, Chromium, matrix set: `MKV H264 AC3.mkv` (`testsrc2` 25 fps) and `MKV H264 AC3.srt` — cues "Cue N" of 1.5 s each, starting every 2 s. On the item page the SRT track is selected, "Resume": PlaybackInfo with `SubtitleStreamIndex=2`, then `master.m3u8` with `SubtitleStreamIndexes=2&ManifestSubtitles=vtt`, `subtitles.m3u8` and `hls1/subs/0.vtt` (all 200); hls.js created an `SRT` track with 60 cues. Frame 39.000 shows "Cue 20" (38–39.5 s). On the player timeline cue 20 starts at 37.943, and at `currentTime` 40.000 frame 40.040 is rendered: video and cues are shifted equally, within a frame. In the player's "Subtitles" menu, "Off" removed the text and selecting SRT brought it back ("Cue 37" on frame 1:13.280); no new PlaybackInfo or playlists were requested. No errors in the Couch log. In `make e2e` the behavioral check `hls-subtitle-playlist` and the `Videos/{id}/subtitles.m3u8` operation pass.

Not checked: several tracks in one playlist in the browser (ordering is covered by a unit test), embedded subtitles via HLS in the browser, files with a non-zero `start_time` in the browser (the formula is covered by a test on MPEG-TS), other clients (#26, #27).

## Audio track switching in the browser (2026-10-06, #24)

`-web-dir ../emby-webui` setup, Chromium. A synthetic MP4 H.264 + two mono AAC tracks (`eng` 440 Hz default, `rus` 880 Hz), 60 s. Playback starts with the direct stream `stream.mp4`. Chromium without `AudioTrack` does not switch tracks within a file, so selecting `rus` in the "Audio" menu sends PlaybackInfo with `AudioStreamIndex=2` and the current position. The Emby Web profile for AAC contains the condition `IsSecondaryAudio Equals false`, so the direct stream is rejected and Couch returns a `TranscodingUrl` with `AudioStreamIndex=2`. The client calls `POST Videos/ActiveEncodings/Delete` (204), loads `master.m3u8` and `main.m3u8` (200), and hls.js continues playback from the switch point. The audio frequency in the served segment is 880 Hz, i.e. the selected track is playing.

Found: before `Videos/ActiveEncodings` was supported (#21), this request received 404, the client aborted the switch and continued the previous stream; on a second switch the player closed.

Not confirmed: by ear (sound in the pane was not checked).

Later the same day: the selected track was encoded to AAC even though it is the only one in the segments. The HLS profile is now evaluated against the video and the selected track, so the track is copied: `TranscodingUrl` without `MaxAudioChannels`, the segment is AAC mono, as in the source, 880 Hz (checked by requests to Couch with the same `IsSecondaryAudio` condition, without a browser).

## Container image (2026-10-06, #62)

Podman 6.1.3, machine applehv, linux/arm64. `podman build` from `Dockerfile`: Go 1.27.1 on `golang:1.27-alpine3.24`, final image `alpine:3.24` with FFmpeg 8.1.2 (`fd` protocol, `libx264`, `aac`), 127 MB. State and the library were kept in named volumes, the library mounted read-only. `-init` via stdin created a user; `/data` files are owned by UID 10001 and have 0600 permissions. The log has no "FFmpeg not found" line, so HLS is enabled. A synthetic MKV H.264 + AC3 (30 s): FFprobe inside the container detected `h264`/`ac3`. For a profile with DirectPlay of only `h264`/`aac` and an HLS profile `ts`, PlaybackInfo returned `SupportsDirectPlay=false` and a `TranscodingUrl` to `master.m3u8`. FFprobe read the stream as `h264` + `aac`, and segments appeared in `/data/transcode`. `podman stop` (SIGTERM) ended the container with code 0 in 0.2 s, and the `transcode` directory was removed. In a second run, Emby Web from a directory mounted at `/web` (`-web-dir /web`) served `index.html` with `data-appversion="4.10.0.40"`; after adding a file, `podman kill -s HUP` produced `Media rescan complete videos=1`.

Not checked: the linux/amd64 image, a Docker build, the browser scenario against the container, `make e2e` inside Linux. Video transcoding (#22) is not yet implemented in Couch: the image only provides FFmpeg with `libx264` for it.

## Rescan without restart (2026-10-06, #39)

`-web-upstream` setup, `-media-dir` with the synthetic e2e set (4 videos, 2 series folders). `Rescanned Movie.mp4` was copied into the directory and `SIGHUP` was sent to the Couch process: the log shows `Rescanning media directory` and `Media rescan complete videos=5`. Without a restart, Emby Web shows the movie first in "Latest Movies", the item page shows "Added 06.10.2026, 09:20" (scan time), and the movie plays. The file was deleted, another `SIGHUP`: `videos=4`, the API no longer returns the movie, and after a page reload it is gone from home; before the reload the open home page still showed it (there is no `LibraryChanged` notification). The process keeps running after the signals. Unit tests cover FFprobe reuse, keeping ID and date, deletion, walk error and an empty volume; `src/cmd/couch` — publishing on signal and refusing to publish an empty directory.

## Local Emby Web via `-web-dir` (2026-10-06, #19)

With the user's permission, four missing files (`modules/apphost.js`, `modules/input/keyboard.js`, `modules/virtual-scroller/virtual-scroller.js` and `.css`) were downloaded via anonymous GETs from `/web/` of the reference Emby 4.10.0.40 (all 200) into `../emby-webui`; they are not part of the Couch repository. Couch with `-web-dir ../emby-webui`, without `-web-upstream`: the client loads; sign-in, home, the episode page, episode playback (video plays), the series page and "Settings" open; all 229 loaded `/web/` files are served without errors, no server errors. The console shows one 404 and two `Uncaught (in promise)` — the same was observed the same day on the `-web-upstream` setup, i.e. unrelated to the UI copy. Not checked: administrative pages (#33), Live TV and music.

## Transcoding to H.264 (2026-10-07, #22)

`-web-dir ../emby-webui` setup, Chromium, browser pane visible, a separate data directory and a new user. Synthetic files of 120 s each, `testsrc2` 640×360, 25 fps: `Mpeg4.avi` (MPEG-4 Part 2, keyframes every 2 s, MP3) and `Hevc10.mkv` (HEVC 10-bit, keyframes every 2 s, AC3 mono). The Emby Web profile accepts neither video even in HLS, so PlaybackInfo returns a `TranscodingUrl` with `VideoCodec=h264` and `VideoBitrate`; `master.m3u8`, `main.m3u8` and segments — 200, hls.js shows the full duration (119.9 s) and renders frames. The timestamp in the frame matches the player position: AVI — 11.200 at 11.12 s, after seeking to 80 s — 1:24.040 at 83.94 s; resuming from 89.5 s (`StartTimeTicks`) starts FFmpeg from the middle of the file — frame 1:34.960 at 94.88 s; HEVC — 6.560 at 6.49 s. MP3 is copied, AC3 is encoded to AAC mono. After `Sessions/Playing/Stopped` the `transcode` directory is empty, there are no FFmpeg processes; no warnings in the Couch log.

Unit test with FFmpeg (HEVC 10-bit 161×121 with closed GOPs in MKV and MPEG-TS): segments are H.264 `yuv420p` 160 wide, every segment except the first starts exactly on plan, a job restarted from segments 3 and 1 writes the same timestamps, a second transcoding session is refused, while remux remains available.

Size limit (2026-10-07): an Emby Web profile with `Width LessThanEqual 320` added to its `h264` conditions was requested from Couch over HTTP for `Hevc10.mkv` (640×360). `TranscodingUrl` contains `MaxWidth=320`, the first segment is H.264 320×180 `yuv420p`; after `Videos/ActiveEncodings/Delete` the `transcode` directory is empty. Not checked in the browser: on this Mac, Emby Web does not set a `Width` condition because 4K decodes smoothly.

H.264 level (2026-10-07): an 8-second HEVC 3840×2160, Emby Web profile with `VideoLevel LessThanEqual 42` — `MaxWidth=1982`, `MaxHeight=1114`, H.264 segment 1980×1114 at level 4.2 (`libx264` chose it); with `62` — no size limit, 3840×2160, level 5.1. After `Videos/ActiveEncodings/Delete` the `transcode` directory is empty. Checked by requests to Couch, without a browser.

Macroblock rate (2026-10-07): HEVC 1920×1080, 60 fps, 6 s; with `VideoLevel LessThanEqual 41` — `MaxWidth=1360`, `MaxHeight=764`, H.264 segment 1358×764, 60 fps, level 4.0; with `42` — 1920×1080, 60 fps, level 4.2. The `transcode` directory is empty after stopping.

Not checked: long files and real CPU load (#29), sound by ear, other clients (#26, #27). Manual check with FFmpeg 9.0.2: HEVC with open GOPs in MPEG-TS, after a restart from the middle, started from the next source keyframe, and the segment plan diverged; in MKV and for H.264 in MPEG-TS there was no divergence. Fixed (#81): restart from the keyframe of the previous segment with `trim`/`atrim` trimming; the unit test now also checks open GOPs in MKV and MPEG-TS, and it failed on the old code (segment 3 started at 29.326 s instead of 26.726 s).

## Rotated video (2026-10-11, #22)

FFprobe 9.0.2 reports rotation from the display matrix (`side_data_list`, `rotation` counterclockwise); when copying into MPEG-TS the matrix is lost, and remux would show the frame sideways. `-web-dir ../emby-webui` setup, Chromium, synthetic 20 s `testsrc2` 1280×720 files rotated 90°: `MOV HEVC rotated.mov` (HEVC, AAC) and `MP4 H264 rotated.mp4` (H.264, AAC). The Emby Web profile accepts HEVC only in HLS, so the MOV used to be served via remux; now PlaybackInfo returns a `TranscodingUrl` with `VideoCodec=h264` and `VideoBitrate`, the player renders a 720×1280 frame, and after seeking to 14 s also 720×1280. The MP4 goes via Direct Stream (`stream.mp4`) and the browser applies the rotation itself — also 720×1280, with the frame oriented the same as after transcoding. After stopping, the `transcode` directory is empty, there are no FFmpeg processes, and no errors in the Couch log.

Unit tests: rotation −90 is read as 270; rotated video is excluded from remux; the H.264 level limits the size of the already rotated frame (`MaxWidth` < `MaxHeight`); the condition `VideoRotation Equals 0` (Emby Web on Tizen) accepts only unrotated video. Not checked: Tizen and other clients. Seek, stop and resume for both files were verified in Emby Web on 2026-11-01; the results are in the [matrix](COMPATIBILITY.md).

## Emby Web screen walkthrough (2026-10-07, #76)

`-web-dir ../emby-webui` setup, Chromium, Movies and TV Shows libraries from the matrix set. API responses with status 400 and above were intercepted in the page. Walked through: home ("Home" and "Favorites" tabs), the Movies and TV Shows libraries with all tabs, movie, series, season and episode pages, a "Matrix" search and all nine settings pages. Found and fixed: the "Favorites" tab received 400 on four `Users/{id}/Items` requests — with `CollectionTypes=music`/`audiobooks` and with `ChannelNumber` sorting for channels. After the fix there are no API errors except 404 for `/Artists` and `/Persons` on the same tab: Couch has no music or people, and these operations are not ranked (#34). The Couch log has no errors. Later they started responding with an empty list (#80): the "Favorites" tab makes two `/Artists` requests and one `/Persons` request, all 200, and it has no more API errors.

Continued: playback of MP4 (Direct Stream), AVI (transcoding) and an episode (remux) with seeking and exiting the player — no API errors (one `ThemeMedia` request was aborted by the client itself when leaving the page). The movie's "More" menu offers "Add to Playlist" for any video (the client decides by item type); the dialog received 400 on `CanEditItems=true` (#77). After the fix the playlist list is empty and error-free; creating a playlist was not checked — playlists are not implemented.

## Library sorting (2026-10-07, #79)

`-web-dir ../emby-webui` setup, Chromium. The Movies sort menu offers 19 options, TV Shows — 13. Before the change, 17 of 32 responded 400 (`UnsupportedSort`) to the same `Users/{id}/Items` request the library makes. After: all 32 — 200. Selecting "Size" in the menu sent `SortBy=Size,SortName&SortOrder=Descending` (200), the Movies grid — 25, 20, 14, 14, 13, 13, 9 MB; the sort was then set back to "Name".

## Library filters (2026-10-07, #78)

`-web-dir ../emby-webui` setup, Chromium, matrix set. Before the change, opening the "Filter" dialog in Movies produced 13 404 responses for value lists. After: no failing requests; the lists the dialog loads on expanding match the library — containers avi, mkv, mp4, ts, webm; video codecs h264, hevc, mpeg4, vp9; audio aac, ac3, mp3, opus; layout mono; audio languages eng, rus, und; subtitles srt (external file); genres empty. Selecting the mpeg4 video codec in the dialog sent `Items?…&VideoCodecs=mpeg4` (200), and the library showed one movie "AVI MPEG4 MP3"; "Remove filters" brought back all seven. The list operations pass the e2e contract and were removed from `knownFailures`. FFprobe reports the language of untagged tracks as `und`, and it is shown as a value.

Additionally: before showing a section, the client requests each list with `Limit=1&EnableTotalRecordCount=false`, and with a selected value — with `OuterIds`; lists are served page by page. In TV Shows the dialog requests only genres, studios, tags, ratings and years — the client does not show the media info section for series. The library "Settings" dialogs and the "More" menu of a series, season and episode work without API errors; "Shuffle" starts an episode, "Show missing episodes" opens a dialog, "Season" opens the season page.

## Library added by rescan (2026-10-07, #75)

`-web-dir ../emby-webui` setup, Chromium. Couch was started without the series (`Media scan complete videos=7 folders=0`), then `Matrix Show` was returned to the media directory and `SIGHUP` was sent (`videos=9 folders=2`). Before the fix, `GET /Items/{id}` compared the requested ID against the libraries of the catalog captured at route registration, and responded 404 for the newly appeared TV Shows library (reproduced by a server test). After the fix, in a freshly loaded Emby Web "TV Shows" opens (`Users/{id}/Items/{library id}` — 200) and shows the series.

## UserConfigurationUpdated across tabs (2026-10-07, #25)

`-web-dir ../emby-webui` setup, Chromium, two tabs of the same user. In the second tab, the subtitle mode was changed via the Emby Web API client (`updateUserConfiguration`, "Default" → "Always"); in the first tab, without a reload, `ApiClient.getCurrentUser()` returned "Always". This call returns the user from the client cache (before the event it returned a stale policy, see #72), so the event updated the cache. The mode was then reverted. A unit test checks the event content and that saving `UserSettings` does not send it.

## ServerShuttingDown on stop (2026-10-07, #25)

`-web-dir ../emby-webui` setup, Chromium. With a WebSocket open, SIGTERM was sent to the Couch process: a subscription to Emby Web API client messages received `ServerShuttingDown` before the connection dropped, and the process exited with code 0. No visible notification appeared in this browser: notification permission is denied, and the toast did not show even when the same message was passed to the client handler directly — this is client behavior in this environment, not a delivery issue. Unit tests check the message and the 1001 close frame for several connections.

## Transcoding load (2026-10-07, #22, #29)

Apple M1 (4 performance and 4 efficiency cores), FFmpeg 9.0.2. The FFmpeg command uses the same encoding parameters as Couch (`libx264` `veryfast`, CRF 23, forced keyframes, `segment` muxer), without rate limiting; sources are synthetic 60 s `testsrc2` (AVI — 120 s from `scripts/matrix-media.sh`). "Cores" is CPU time (user + sys) per second of video, i.e. how many cores are busy during real-time playback: Couch pauses FFmpeg once it is 10 segments ahead.

| Source → output | Speed | Cores | Peak RSS |
|---|---|---|---|
| HEVC 10-bit 1080p24 + AC3 5.1 → H.264 1080p (level 4.0) + AAC 2.0 | 7.4× | 0.92 | 555 MiB |
| HEVC 10-bit 2160p24 → H.264 1980×1114 (profile with level 4.2) | 3.9× | 1.94 | 916 MiB |
| HEVC 10-bit 2160p24 → H.264 2160p (level 5.1) | 1.9× | 3.59 | 1918 MiB |
| MPEG-4 Part 2 360p25 + MP3 → H.264 360p, MP3 copied | 55.6× | 0.09 | 81 MiB |

A synthetic image is easier to encode than a real movie, so the figures are a lower bound. A paused FFmpeg keeps its memory: one 4K session holds about 2 GiB. Memory can be reduced by the thread count at the cost of speed (4K → 4K, same setup): by default 2.1× and 1918 MiB; `libx264` with 4 threads — 1.5× and 1389 MiB; decoder with 2 and `libx264` with 4 threads — 1.4× and 1134 MiB; both with 2 threads — 1.4× and 996 MiB (`rc-lookahead=10` does not change memory: `veryfast` already sets that much). Defaults were not changed: the trade-off between memory and speed headroom depends on the budgets (#15). Hardware encoding is not used (#28); a comparison with Emby on the same setup was not performed (#29).

## LibraryChanged after rescan (2026-10-07, #39, #25)

`-web-dir ../emby-webui` setup, Chromium, a library of two movies. With the Movies library open, `Fresh.mp4` was added to the folder and `SIGHUP` was sent to the Couch process: the log shows `Media rescan complete videos=3`, and the list showed three items on its own, without navigation or reload. After moving the file out of the folder and `SIGHUP`, the list returned to two items on its own; the update arrives with a delay of a few seconds — the client defers redrawing. On home, the "Latest Movies" row showed `Fresh` on its own after the file was returned and `SIGHUP`. A unit test checks the message lists (added, updated, removed, parent folders, libraries) and that an unchanged catalog sends no event. Series and season screens and other clients were not checked.

## Undeliverable subtitles in the browser (2026-10-06, #24)

`-web-upstream` setup, Couch started with a `PATH` without `ffmpeg` (only `ffprobe`), `Subbed.mp4` with a `mov_text` track temporarily added to the media directory. On the item page the `rus MOV_TEXT` track is selected, playback: PlaybackInfo with `SubtitleStreamIndex=2` gives a direct stream without `ErrorCode`, the video plays (206, `Sessions/Playing` and progress reports), no `<track>` is added, no error dialog; the log shows `Subtitle not deliverable, playing without it`. The file was then removed.

## Subtitle auto-selection in the browser (2026-10-04, #24)

`-web-upstream` setup, preferred subtitle language `ru`, `Sample Movie.ru.srt` without flags temporarily next to the movie. "Always" mode: playback without selecting a track — Emby Web receives `DefaultSubtitleStreamIndex=2` and shows "Автовыбор субтитров" from ~1 s. "Default" mode: the same track is not selected (no default/forced flags), index −1, no text. Settings were reverted (`Default`, no language set), the file was removed. The mode rules follow the descriptions in Emby Web strings; reference responses for them were not captured.

## Sustained load (2026-10-04, #30)

`COUCH_SOAK=5m`, 8 workers, macOS arm64, synthetic e2e media set: 193,834 requests in 5 minutes (about 650 per second), no errors. RSS: 15.2 MiB at start, 25.1 MiB at the end (same peak); a 20-second run before that reached 23.3 MiB, i.e. growth slows down, but 5 minutes is not enough to rule out a slow leak. Open files: 13 before and 14 after; FFmpeg child processes appeared during subtitle requests and did not linger; the data directory contents did not change. Not covered: WebSocket reconnects, scanning during playback (there is no scanning at runtime, #39), the duration and concurrency from #15.

A repeat run with WebSocket reconnects (every fifth worker cycle, alternating clean close and TCP drop): `COUCH_SOAK=5m`, 8 workers — 195,965 requests and connections, no errors; RSS 15.1 → peak 24.4 → 22.7 MiB at the end, open files 13 before and 14 after, no child processes or new files. That the check catches a failure was confirmed by substituting the WebSocket path: every connection was marked as an error.

With HLS and rescans (2026-10-07): workers 0 and 1 play the first remux-to-HLS segment every fourth cycle, worker 2 a transcoding one, and they close the session with `ActiveEncodings/Delete`; throughout the load, `SIGHUP` is sent to the process every 75 s. `COUCH_SOAK=10m`, 8 workers, Apple M1, FFmpeg 9.0.2: 365,131 requests, no errors, 8 rescans. FFmpeg was running in 12 of 22 samples, with no more than three processes at once (two remux and one transcoding — the limits held); at the end there are no child processes, open files 13 → 14, the data directory contents are unchanged (no segments left). RSS 15.5 MiB at start, 24.4 after 30 s, then slow growth to 26.3 MiB by the end (about 0.2 MiB per minute, +0.1 MiB over the last 2 minutes). A leak is not established, but not ruled out either: a longer run with the duration from #15 is needed.

The same test for 30 minutes (2026-10-07): 1,071,888 requests, no errors, 7 rescans, FFmpeg was running in 13 of 21 samples, no child processes at the end, open files 13 → 14, no segments left. RSS 15.5 MiB at start, 24.9 after 1.5 minutes, slow growth to a peak of 26.7 MiB by minute 23, then a decline to 25.2 and 25.6 MiB at the end — below the peak. This is a plateau around 25–27 MiB, not unbounded growth: no signs of a memory leak over 30 minutes. The duration and concurrency for acceptance are still not agreed (#15).

A repeat run on Linux after the 2026-10-31 changes (images, folder listing cache, resource log; #95): `COUCH_SOAK=10m`, 8 workers, `golang:1.25-alpine3.24` container with FFmpeg 8.1, AMD Ryzen 7 255. 848,742 requests, no errors, 8 rescans. RSS 10.9 MiB at start, 16.6 after 30 s, then a plateau of 16.5–17.3 MiB until the end (peak 17.3, 16.6 at the end); open descriptors 10 → 11; no more than three FFmpeg processes at once, no child processes at the end. A run before the sampling fix on the same system showed RSS 0 and other processes' files: BusyBox `ps` does not support `-p`, and `lsof` lists the files of all processes. On Linux the sampling now reads `/proc`, and the test fails if RSS or descriptors could not be read.

## Response time on a large library (2026-10-05, #61)

`go test -run '^$' -bench 'BenchmarkSeriesPage|BenchmarkScreens' ./internal/server/` on a synthetic library: 1000 series × 2 seasons × 10 episodes (20,000 episodes), 300 started series; Apple M1, handler without network. Time per request:

| Request | Time |
|---|---|
| Page of 100 series with the "unplayed" filter | 355 ms before the folder index, 13 ms after |
| `Shows/NextUp`, 24 items | 2.3 ms |
| "Continue Watching" row (`Sections/resume/Items`) | 2.5 ms |
| `Items/Latest` for TV Shows | 16 ms |
| Search by name | 11 ms |
| Series item page | 0.5 ms |
| Series `Seasons` / `Episodes` | 3.5 / 3.8 ms |

Latency budgets are not agreed (#15); the figures are a baseline for #14.

## Backdrop, logo and Thumb in the browser (2026-10-31, #85, #86)

Setup: image from `Dockerfile` (Docker build, linux/amd64) with `-media-dir` and `-web-dir` (local copy of Emby Web 4.10.0.40), Chromium in the built-in browser. Synthetic set: an MP4 H.264 + AAC movie with `-poster`, `-fanart`, `-clearlogo`, `-landscape`; a two-episode series with `poster`, `backdrop`, `logo`, `landscape` in the series folder. Each image has its own color, so a screenshot shows which one is displayed.

- API: the movie and the series return `ImageTags` with `Primary`, `Logo`, `Thumb` and one `BackdropImageTags`; the season and episodes return empty `ImageTags` and the series' `Parent{Backdrop,Logo,Thumb}ItemId`.
- Home: "Latest Episodes" shows the series Thumb (series `Images/Thumb`, 200).
- Movie page: poster, logo instead of the title, dimmed backdrop; requests for `Primary`, `Logo`, `Thumb`, `Backdrop/0` — 200.
- Episode page: the episode image is the series Thumb, with the series logo and backdrop; requests for series images — 200.
- All `/emby` requests during the run — 2xx; no warnings in the Couch log. The console shows one 404 error without an API request (later established: the Emby Premiere registration check on `mb3admin.com`, see below).

Repeat run on a new image (same day, after #88):

- Series page: poster, logo, series Thumb on episodes; the series `Backdrop/0` is displayed (the `displayingBackdropImage` element, appears after a fade-in delay).
- Season page: no cover of its own, the series poster is shown (series `Images/Primary` by `SeriesPrimaryImageTag`, 200), with the series logo and backdrop, and the series Thumb on episodes.
- No warnings in the Couch log. The console again shows one 404 error. This is not Couch: after a full reload no page resource returned 4xx, and the client log names the source — the Emby Premiere registration check on `mb3admin.com` (`ConnectionManager response status: 404`, `getRegistrationInfo response: 404`).

Not checked: NAS.

Image lookup cost (same day, #87; AMD Ryzen 7 255, local SSD):

- The #61 benchmarks (catalog without a root, i.e. without file I/O) showed a regression from looking up the series by scanning the catalog in every DTO: `Shows/NextUp` 2.3 → 4.6 ms, the "Continue Watching" row 3.1 → 4.2 ms, `Episodes` 5.8 → 6.8 ms. After the folder index — 2.3, 3.0 and 5.7 ms, at the pre-images level.
- Looking up one image type when the file is missing: 80 µs (12 failed opens via `os.Root`), i.e. up to 420 µs per episode with four types and series images versus 80 µs before #85. With a folder listing revalidated by the folder's mtime — 4.3 µs per type.

## FFmpeg cost per HLS session (2026-10-31, #90, #14)

First figures from the `HLS job ended` log line. This is not the #14 baseline: the media set is not agreed (#16), and Emby was not measured.

Setup: image from `Dockerfile` (FFmpeg 8.1, x264 `veryfast`, CRF 23), container limited to 4 cores (`--cpus 4`), AMD Ryzen 7 255, local SSD. File: synthetic `testsrc2` 1920×1080, 24 fps, 120 s, H.264 + AC3 stereo, MKV, 106 MiB. Segments were requested in order by one client, back to back, without playback.

| Session | Segments | Time of all requests | FFmpeg CPU | FFmpeg peak RSS |
|---|---|---|---|---|
| Remux: video copied, AC3 → AAC | 15 | 1.5 s | 2.0 s | 47 MiB |
| Transcoding to H.264 1280×720, 4 Mbit/s, AC3 → AAC | 15 | 14.5 s | 57.4 s | 453 MiB |

Couch: RSS 9.3 MiB idle after the scan, peak 11.1 MiB after both sessions. Segments on disk after both sessions: 65 MiB (removed on idle or stop).

For #15: transcoding 1080p to 720p costs about 0.5 seconds of CPU per second of video (half a core in real time) and almost half a gigabyte of memory per job: 29 times more CPU than remux, and 10 times more memory than remux and 40 times more than Couch itself. In this run the log line still wrote a single `encode` field (remux had `encode=true` because of audio encoding); video and audio are now reported separately: `video=copy audio=aac` and `video=h264 audio=aac`. The `testsrc2` test pattern is easier to encode than real video, so CPU on real files will be higher.

## FFmpeg threads during transcoding (2026-10-31, #92)

Same file and image as in the section above; FFmpeg was run directly with Couch's encoding parameters (x264 `veryfast`, CRF 23, `-maxrate 4000000`, scaling to 1280×720, AC3 → AAC) and `-benchmark`. `-threads` before `-i` limits the decoder, after it the encoder. Host: 16 logical CPUs.

| Threads (decoder / encoder) | Container CPU limit | Time | CPU (user + sys) | Peak RSS | Output size |
|---|---|---|---|---|---|
| auto / auto (current) | 4 | 14.3 s | 57.3 s | 452 MiB | 23.6 MiB |
| 4 / auto | 4 | 14.3 s | 57.1 s | 384 MiB | 23.6 MiB |
| 4 / 4 | 4 | 12.1 s | 48.2 s | 222 MiB | 21.3 MiB |
| 2 / 4 | 4 | 12.0 s | 47.7 s | 204 MiB | 21.3 MiB |
| 4 / 2 | 4 | 11.5 s | 45.7 s | 202 MiB | 21.3 MiB |
| auto / auto (current) | none | 5.5 s | 58.6 s | 453 MiB | — |
| 4 / 4 | none | 10.0 s | 48.3 s | 221 MiB | — |

FFmpeg picks the thread count from the visible CPUs, not from the container quota. Under a quota, extra threads only add memory and contention; without a quota they make encoding twice as fast, and therefore also the wait for the first segment and for the segment after a seek. All variants encode much faster than real time. Fewer x264 threads give a 10% smaller file at the same CRF; quality (PSNR/VMAF) was not compared.

Decision (owner): a `-ffmpeg-threads` flag, automatic by default. Checked through Couch's own HLS job on the same kind of file (synthetic `testsrc2` 1920×1080, 24 fps, 120 s, H.264 + AC3, MKV), transcoding to 1280×720 at 4 Mbit/s, `--cpus 4`, segments requested back to back; two runs each, figures from the `HLS job ended` line:

| `-ffmpeg-threads` | Segments | Time of all requests | FFmpeg CPU | FFmpeg peak RSS |
|---|---|---|---|---|
| 0 (automatic) | 12 | 13.4–13.5 s | 52.9–53.3 s | 449 MiB |
| 4 | 12 | 11.3–11.4 s | 44.8 s | 214 MiB |

The file has x264's default keyframe interval, so it has 12 segments instead of the 15 above.

## External requests blocked by CSP (2026-11-01, #89)

Couch serves `/web/*` with `Content-Security-Policy: connect-src 'self'`. Release image, `-web-dir` with the local Emby Web 4.10.0.40, Chromium, R03 and R04 from the real set.

- On each load, the client's `validateDevice` request to `mb3admin.com` (ServerId, DeviceId and device name in the query) is blocked by the browser: "violates the following Content Security Policy directive". Emby Web logs `getRegistrationInfo failed: TypeError: Failed to fetch` and an unhandled rejection, shows no dialog, and keeps the "Get Emby Premiere" button, as with the earlier 404.
- Still working: sign-in, home with Continue Watching and Latest, the library, item pages, search suggestions, settings, Direct Stream of R04 (9.1 s after 10 s), and HLS remux of R03 with a seek (327 s → 336.5 s 10 s later).
- No other request was blocked during the walk, and no warnings in the Couch log.

Not checked: casting (the Chromecast sender loads its script, which `connect-src` does not cover; casting itself was not tried) and `-web-upstream`, which goes through the same header code.

## x264 options from Emby (2026-10-31, #99)

Transcoding now adds Emby's `-x264-params subme=0:me_range=4:rc_lookahead=10:partitions=none` to the same `veryfast` and CRF 23. Release image (FFmpeg 8.1), container limited to 4 CPUs, AMD Ryzen 7 255, local SSD. Source: R06 from the real set (MP4 HEVC 720p + AC3 5.1).

FFmpeg alone, the first 120 s of video only, quality against the source:

| x264 options | Wall time | CPU | Peak RSS | Output | SSIM (All) | PSNR Y |
|---|---|---|---|---|---|---|
| `veryfast` (before) | 12.5 s | 49.8 s | 390 MiB | 15.3 MiB | 0.9646 | 31.87 dB |
| `veryfast` + Emby's options | 8.1 s | 32.3 s | 310 MiB | 20.5 MiB | 0.9633 | 31.83 dB |

Couch's HLS job on the same file (video to H.264 at the source size, AC3 → AAC), 20 segments (about 120 s) requested back to back, two runs each; the job runs ahead of the requests as in playback, so its CPU covers more than 20 segments:

| x264 options | First segment | 20 segments | FFmpeg CPU | FFmpeg peak RSS |
|---|---|---|---|---|
| `veryfast` (before) | 1.11–1.17 s | 21.5 s | 85.7 s | 428 MiB |
| `veryfast` + Emby's options | 0.80–0.81 s | 14.2–14.3 s | 56.8–57.1 s | 352 MiB |

The options cut CPU by a third and memory by a fifth, and the picture stays within 0.002 SSIM and 0.05 dB PSNR. The cost is a file about a third larger at the same CRF, so more segment storage and bandwidth, as with Emby. Not yet measured: `scripts/baseline.sh` on the three profiles against Emby, and the 4K source (#97).

## FFprobe cost during scanning (2026-10-31, #93)

Same image and set: 4 videos (a 1080p MKV movie of 106 MiB, a movie and two 720p MP4 episodes of 20 s each). Log lines: `Media scan complete ... probes=4 probe_cpu=141ms probe_peak_rss_kib=48264`; after `SIGHUP` without changes — `Media rescan complete ... probes=0 probe_cpu=0s probe_peak_rss_kib=0`. One FFprobe run takes about 35 ms of CPU and up to 47 MiB of memory; runs are sequential, so the scan peak equals the peak of one run. For the #61 library (20,000 episodes) this is about 12 minutes of CPU on the first scan, if the files are similar to the synthetic ones; real files and a network volume were not measured.

Stop line (#94), same container after the scan and `docker stop`: `Couch stopped cpu=31ms peak_rss_kib=11832 children_cpu=174ms children_peak_rss_kib=48184`, exit code 0. `children_cpu` also includes the FFprobe protocol check at startup, so it is larger than the scan's `probe_cpu` (145 ms).

## Couch cost of Direct Stream (2026-10-31, #94, #14)

Same image and set, no CPU limit. A 106 MiB file (`stream.mkv?Static=true`) was downloaded in full over loopback, from the OS page cache; a new container for each run, figures from the `Couch stopped` line. The baseline run (start, scan, sign-in, stop with no downloads) — 106 ms CPU, peak 11.6 MiB; it is subtracted in the table.

| Load | Served | Couch CPU above baseline | Couch peak RSS |
|---|---|---|---|
| 1 worker, 5 downloads in a row | 531 MiB | 0.27 s | 11.3 MiB |
| 8 workers concurrently, 3 times | 2549 MiB | 1.52 s | 12.4 MiB |

Couch spends about 0.5–0.6 s of CPU per gibibyte of served file, and memory barely grows with the number of workers. For playback at 20 Mbit/s this is on the order of 1.5 ms of CPU per second per stream. Not checked: reading from disk or a network volume instead of the cache, TLS, slow clients.

## Saved catalog index (2026-11-01, #39, #31)

Couch built from the working tree, run in the `couch:dev` image (FFprobe 8.1) on the 18-file real set from [COMPATIBILITY](COMPATIBILITY.md):

- First start, no index: `Media scan complete videos=18 folders=2 skipped=0 probes=18 probe_cpu=771ms`, then `catalog.json` (26 KiB) in the data directory. The process was killed with `SIGKILL` 6 s later.
- Second start: `Media index loaded; rescanning in the background videos=18 folders=2`, `/healthz` answered 28 ms after `docker run` returned, and the background rescan finished with `probes=0`.
- Third start with an empty directory mounted as `/media`: the index loaded, the rescan logged `Media rescan failed; keeping the previous catalog error="media directory has no videos"`, and `catalog.json` still held 18 items.

`TestSavedCatalog` covers the round trip (items, series and seasons equal to a fresh scan), a rescan after loading that probes nothing unchanged, a first-seen date that survives a save and reload, and the rejected indexes: missing, truncated, empty, another version, another directory, a path outside the library, a duplicate path, no videos.

## 4K source in Emby Web after 3 s transcode segments (2026-11-01, #97)

Couch at a9eb3a9 in an image built from it, limited like the large profile of [BASELINE](BASELINE.md) (`--cpus 4 --memory 4g`), with only R09 (MKV HEVC Main 10 HDR10 3840×2076, 88 min) mounted, and Emby Web 4.10.0.40 via `-web-dir` in the built-in Chromium. The soak test of #30 was running on the host. The scenario is the one of the matrix in [COMPATIBILITY](COMPATIBILITY.md).

- **Start:** the player showed 3840×2076 frames at once, without the 10 s restarts seen before. Over 14 s the position went from 1.5 to 11.8 s: the encode runs at about 0.75× real time, and `readyState` alternated between 2 and 4, so the player stalls briefly every few seconds. Emby on the same profile delivers 18 s of this video in 23.9 s ([BASELINE](BASELINE.md), fourth run), also below real time.
- **Seek to the middle (2644 s):** 12 s later at 2645.9 s, then about 0.8× real time. The log shows one job ending for the restart.
- **Stop:** 2664.9 s saved with the player at 2665 s.
- **Resume:** no frames 10 s after "Resume"; at 12 s playing from 2665.5 s, then at real time. One job ended after a single segment during the start, which suggests one restart.
- Couch itself: `cpu=526ms peak_rss_kib=16448`; FFmpeg jobs: `children_cpu=7m48.834s children_peak_rss_kib=2685160` over the session.

So the 4K source now starts in Emby Web on 4 CPUs, but does not play smoothly, and a resume can take longer than the client's 10 s wait. Smooth 4K needs a smaller output size or hardware encoding (#97, #28).

## 8-hour soak (2026-11-01, #30)

`COUCH_SOAK=8h`, 8 workers, run from `e2e` in the `couch:dev` image, limited like the large profile of [BASELINE](BASELINE.md) (`--cpus 4 --memory 4g`), on the bench host (AMD Ryzen 7 255). The binary was built from 3908118, before the saved catalog index (#39) and the 3 s transcode segments (#99). As in the shorter runs, workers 0 and 1 remux and worker 2 transcodes, filling the FFmpeg limits, and each ends with `Videos/ActiveEncodings/Delete`. The test sends `SIGHUP` every eighth of the duration, so this run rescanned 7 times, once an hour.

- 42,412,664 requests, 0 failures; `--- PASS: TestSoak (28808.59s)`.
- Child processes: 0 to 3 during the load, 0 at the end. The data directory held the same files at the end as at the start, so no segments were left.
- Open files: 10 at the start, 20–28 under load, 11 at the end.
- RSS, sampled every 24 minutes: 9.7 MiB at the start, then 14.6–15.4 MiB for the whole run, peak 15.4 MiB (15,724 KiB) at hour 4.8, 14.9 MiB at the end. The peak of the last 4 hours is 280 KiB above that of the first 4 (15,444 KiB); the samples after it return to the same band, with no trend.

Against the stability criteria in [ROADMAP](ROADMAP.md) M0 (#15, approved 2026-11-01): the duration, profile, concurrency, errors, child processes and segments match. The rescan was hourly, not every 75 s, and "no growth above the peak of the first 4 hours" is missed by 280 KiB. Rerun with a 75 s rescan (`COUCH_SOAK_RESCAN=75s`, added after this run) and the current code.

## 2-hour and 6-hour soak with a 75 s rescan (2026-11-02, #30)

A rerun of the 8-hour soak above with the approved 75 s rescan and the current code, split at the owner's request into a 2-hour run followed, once it passed, by a 6-hour run. Both used the `couch` binary from 44c86c8 (with the saved catalog index, #39, and the 3 s transcode segments, #99) in a `couch:soak` image limited like the large profile (`--cpus 4 --memory 4g`), on the bench host (AMD Ryzen 7 255): `COUCH_SOAK=2h` and then `COUCH_SOAK=6h`, both with `COUCH_SOAK_RESCAN=75s` and 8 workers. Workers 0 and 1 remux and worker 2 transcodes, filling the FFmpeg limits.

A first 2-hour attempt failed only because Couch rotated its server log (`logs/couch-1.log`), which the test counted as a leftover file (#118). Its load was clean: 7,312,796 requests, 0 failures, 95 rescans. The test now leaves rotated logs out of that check.

| | 2 hours | 6 hours |
|---|---|---|
| Requests / failures | 7,404,574 / 0 | 22,036,315 / 0 |
| Rescans | 96 | 288 |
| Child processes under load / at the end | 0–3 / 0 | 0–2 / 0 |
| Data directory at the end | same files as at the start | same files as at the start |
| Open files: start, under load, end | 12, 20–30, 13 | 12, 19–26, 13 |
| RSS at start | 15.6 MiB (16,004 KiB) | 15.6 MiB (16,020 KiB) |
| RSS band after warm-up | 20.6–21.5 MiB (21,096–22,048 KiB) | 20.6–21.2 MiB (21,100–21,676 KiB) |
| RSS peak, first half / second half | 21,620 / 22,048 KiB | 21,504 / 21,676 KiB |
| RSS at the end | 21,548 KiB | 21,512 KiB |

The extra open file at the end is the test's HTTP client, not Couch: a 20-second run listed the descriptors at the start and the end, and the files (database, lock, log, media directory, cgroup file) were identical. Only the accepted TCP sockets differed: one idle client connection at the start, two at the end, which is the Go client's limit of 2 idle connections per host.

Against the stability criteria in [ROADMAP](ROADMAP.md) M0 (#15): the profile, concurrency, 75 s rescan, errors, child processes and segments match, and open files return to the start count apart from that client connection. The second half's RSS peak is 428 KiB above the first half's in the 2-hour run (one sample at hour 1.2, followed by the same band) and 172 KiB above it in the 6-hour run, with no trend: the last samples of each run sit inside the band. Two runs are not the approved single 8-hour run, so the "last 4 hours against the first 4" comparison is not made across them.

The owner then amended the criterion (2026-11-02): 4 hours or more, the second half's RSS peak at most 5% above the first half's, and up to 2 idle connections of the test's client among the open files. The 6-hour run meets it (+0.8%), and so does the 2-hour one apart from its length (+2.0%), as does the earlier 8-hour run apart from its hourly rescan (+1.8%). `TestSoak` now fails when the criterion is not met.

## Hardware encoding with `-hwaccel` (2026-11-01, #28)

`-hwaccel vaapi|qsv|nvenc|amf` replaces `libx264` with the GPU's H.264 encoder; decoding, scaling and subtitle burn-in stay on the CPU. Host FFmpeg 8.0.1 (not the image), AMD Ryzen 7 255 with Radeon 780M (radeonsi, VA-API render node `/dev/dri/renderD128`), 16 CPUs, no container limit.

- `TestHardwareTranscodeMatchesThePlan` with `COUCH_TEST_HWACCEL=vaapi`: Couch's HLS job transcodes an HEVC 10-bit source in MKV and an open-GOP one in MPEG-TS to 320 px. Every segment starts with an IDR frame on its planned keyframe, and segments rebuilt after a seek start where the continuous pass did. The same test with `libx264` (`TestTranscodeSegmentsMatchThePlan`) now also checks for the IDR frame.
- Startup on this machine: `vaapi` starts; `nvenc` stops with `Cannot load libcuda.so.1`, `qsv` with a VA-API initialisation error (no Intel GPU), `amf` with `Unrecognized option` (this FFmpeg has no `h264_amf`).
- VCN encodes frames from 128×128 to 4096×4096; smaller output fails the job.
- Emby Web in Chromium, R11 (HEVC Main 10 1920×816, AC3 5.1) from `-media-dir` with `-hwaccel vaapi`: "Play" transcodes to H.264 at 1920×816 and plays (14.3 s after 15 s); a seek to 10:00 plays on (611 s 12 s later); stopping ends the jobs. Log: `HLS job ended ... video=h264 audio=aac encoder=h264_vaapi segments=28 cpu=38.455s peak_rss_kib=336248`. Chromium dropped 43 of 812 frames, at the start and the seek; not compared with `libx264`.

FFmpeg alone, 60 s of video from 5:00, at the source size with the default 8 Mbit/s cap; the HLS segmenter and audio are left out:

| Source | Encoder | Wall time | CPU | Peak RSS |
|---|---|---|---|---|
| R11, HEVC Main 10 1920×816 | `libx264` | 5.5 s | 54.9 s | 693 MiB |
| R11 | `h264_vaapi` | 4.6 s | 29.4 s | 318 MiB |
| R09, HEVC Main 10 HDR10 3840×2076 | `libx264` | 26.4 s | 288.8 s | 2751 MiB |
| R09 | `h264_vaapi` | 16.1 s | 198.0 s | 1093 MiB |

The GPU halves CPU and memory on R11 and takes a third of the CPU off R09. Most of what remains is software HEVC 10-bit decoding, which hardware decoding would remove. Quality, R11 20 s: `libx264` SSIM 0.9716 at 5.4 MiB, `h264_vaapi` SSIM 0.9748 at 10.4 MiB. At `-global_quality 23` VA-API on radeonsi (QVBR) spends about twice the bits for similar quality, still within the cap. Not checked: `qsv`, `nvenc`, `amf` on their hardware; Intel VA-API.

The release image has no GPU drivers; built with `--build-arg VAAPI=1` it adds Mesa's VA-API drivers (`mesa-va-gallium`), and with `--device /dev/dri/renderD128 --group-add GID` (the host's `render` group) `-hwaccel vaapi` starts in the container. The default image stops at startup with `Failed to initialise VAAPI connection`. In this image every transcode row of the agreed set passes in Emby Web ([COMPATIBILITY](COMPATIBILITY.md), "Transcoding on the GPU"), and the resources and picture quality against `libx264` and Emby are in [BASELINE](BASELINE.md), "VA-API". In the container, Mesa logs `Failed to create //.cache for shader cache` as FFmpeg runs with no home directory; the encode does not need the cache.

## Metadata from `.nfo` files (2026-11-01, #101)

`-web-dir ../emby-webui` setup, Chromium, no provider keys. Synthetic 20 s H.264/AAC files: `Night Train (2015)/Night Train (2015).mkv` with a `.nfo` (title, original title, plot, tagline, year, premiere, rating 7.4, critic rating 81, `PG-13`, two genres, a studio, a tag, a director, a writer, two actors, an IMDb ID), `Plain.Movie.2012.mp4` without one, and `Harbor/Season 01/Harbor.S01E01.mkv` and `S01E02.mkv` with `Harbor/tvshow.nfo` and an episode `.nfo` for E01.

- Home: Latest shows "Night Train 2015"; episode cards show the series title "Harbor Lights" from `tvshow.nfo`.
- Movie page: rating, critic score, year, runtime, `PG-13`, genre, tagline, overview, "Director: Ida Berg" and the Cast & Crew row. A person card opens the person's page, which lists the movie and the series she appears in.
- Movies → Filter: the Genre, Parental rating, Studio, Tag and Year lists appear; choosing "Thriller" (`GenreIds`) leaves Night Train only.
- Series page: rating, year, studio, `TV-14`, genre, overview; Season 1 lists "1. Low Tide" (title, air date and plot from the episode `.nfo`) before "2. Harbor.S01E02". Before the fix in this change, the untitled episode came first: the row requests episodes without `SortBy`, and the name sort put "Harbor…" before "Low…"; episodes now sort by season and episode number first.
- No request answered 4xx or 5xx during the walk.
- Restart: the stored metadata was served without reading a `.nfo` again (no `Metadata files read` line). After `tvshow.nfo` was given a new title and Couch received `SIGHUP`, the log showed `Metadata files read nfo_read=1`, and the series and its episodes carried the new title.
- A start on a fresh data directory under `strace -f -e trace=connect` with no provider keys made no `connect` call, FFprobe included.

The `internal/metadata` tests also run the providers against a local TLS server that answers like both APIs: lookups by title and year and by `.nfo` IDs, TheTVDB before TMDb for series, a translated series title, episodes by their series' IDs, poster and backdrop downloads, no request for unchanged items, a changed `.nfo` read without a new lookup, a lookup again after `-metadata-refresh`, a rejected key stopping the run, and no request without keys.

## Metadata from TMDb and TheTVDB (2026-11-01, #101)

`-web-dir ../emby-webui` setup, Chromium, both API keys set in the environment. Synthetic 5 s files named like a real library: `The Matrix (1999)/The Matrix (1999).mkv`, `Spirited.Away.2001.1080p.BluRay.x264.mkv`, `Heat (1995)/Heat (1995).mkv` with a `.nfo` holding only a title, the TMDb ID 949 and the genre Crime, and `Breaking Bad/Season 01/Breaking.Bad.S01E01.mkv`, `S01E02.mkv` and `Dark/Season 01/Dark.S01E01.mkv`.

- First start: `Online metadata fetched items=8 not_found=0 failed=0` (three movies, two series, three episodes) within 8 s.
- Every item matched: TMDb 603, 129 and 949 (the last by the `.nfo` ID, keeping its genre "Crime" over TMDb's); TheTVDB 81189 and 334824 with their TMDb and IMDb IDs. Spirited Away carries its Japanese original title; the series carry TheTVDB's network, rating, genres and poster and TMDb's backdrop, rating and tagline; the episodes carry TheTVDB's titles, air dates and stills and TMDb's overviews.
- Emby Web: posters on the home screen; the Breaking Bad page shows poster, backdrop, rating, AMC, TV-MA, tagline, overview, and episode stills with titles and ratings. No request answered 4xx or 5xx.
- Restart with the keys still set, under `strace -f -e trace=connect`: no non-local connection; the stored metadata was served.
- `-metadata-language ru` on an emptied metadata table: Russian titles and overviews ("Матрица", "Во все тяжкие", "Тьма", episode "Пилотная серия") and Russian certifications ("16+", "18+"). Spirited Away first matched a 2001 TV special: in Russian, TMDb ranks the special, whose Russian title is its Japanese one, above the film for the query "Spirited Away". Fixed in this change by preferring a result whose title or original title equals the query, else the most popular; it then matched TMDb 129, "Унесённые призраками", 12+.
- Genres of series come from TheTVDB, which has no translations of them, so they stay in English under `ru`; movie genres come from TMDb in Russian.

## People from TMDb and TheTVDB (2026-11-03, #131)

`-web-dir ../emby-webui` setup, Chromium, TheTVDB key only: TMDb was unreachable from the host (its API name resolved to localhost), and with both keys every lookup failed. Library: `The Matrix (1999)/The Matrix (1999).mkv` and `Breaking Bad/Season 01/Breaking Bad.S01E01.mkv`.

- First start: `Online metadata fetched items=3 people=9 not_found=1 failed=0`: the series, season and episode, and the series' nine credited actors; the movie needs TMDb.
- All nine got a TheTVDB photo (22–32 KiB JPEG) and a birth date. Four have an English biography at TheTVDB, and Couch showed exactly those four; the other five have an empty one there.
- Emby Web: the Breaking Bad page's Cast & Crew row shows the photos; Aaron Paul's page shows the photo, "Born: August 27, 1979" and the biography. The console logged only the blocked `mb3admin.com` check (#89).
- Not checked live: TMDb people (biography, `h632` photo) and a `.nfo` `<actor><thumb>`. Both are covered by tests against recorded answers.

## Metadata lookup limits (2026-11-01, #109)

- **Scale:** a generated library of 10,000 movies against a local stand-in for TMDb (20 actors, 3 genres and 2 keywords each, request spacing off).
  - The first lookup took 5.9 s and published the library twice, instead of about 400 times before this change. One full publish costs 49 ms at this size.
  - Couch allocated about 0.8 GB over the run, and the heap kept 44 MiB for the 10,000 items.
  - A second lookup with nothing due took 0.47 s.
- **Retry:** on the 18-file real set under real names, both providers on, the first lookup started while TMDb was unreachable from the host. It logged `Online metadata lookup stopped; it is tried again later reason="requests keep failing"`. 5 min 20 s later, with no rescan, it retried and fetched 28 items, 1 not found.
- **Images**, same set:
  - TMDb posters now 780 px wide, 108 KiB on average; backdrops 1280 px, 116 KiB.
  - The originals before reached 3840×2160, and backdrops averaged 460 KiB.
  - TheTVDB images are unchanged: posters 680 px wide at 160 KiB, backdrops 1920 px at 459 KiB.
  - The folder went from 12 MB to 9.6 MB, since most of this set's images come from TheTVDB.
  - With every download then scaled to at most 1280 px (landscape) or 780 px (portrait) and saved as JPEG quality 85: TheTVDB backdrops 1280 px at 155 KiB on average, posters 81 KiB, and all 42 images 4.5 MB. Scaling and encoding took about 26 ms per image. The binary grew by 0.2 MB for `golang.org/x/image`. The series page in Emby Web shows the poster, backdrop and stills without visible artifacts.
- **Provider responses:** the largest seen was TheTVDB's extended record of a series with 30+ seasons, at 475 KB, against the 4 MiB cap. The longest season checked, 194 episodes, fits TheTVDB's 500 per page. TMDb sends no rate-limit headers.
- **Tests:** two 429 answers waited out; a failed metadata save stops the run without storing the lookup; an image that cannot be written leaves its item to be fetched again; a stopped lookup is retried by the timer; a rescan with the same items does not restart a running lookup. A mutation that restarts it fails the last test.


## Install on the target host (2026-11-01, #114)

Host: AMD Ryzen 7 255 with Radeon 780M, Ubuntu 26.04.1 LTS, kernel 7.0.0-42, Docker 29.9.2 with the iptables firewall backend and `docker-proxy`, Docker Compose v5.6.1. Image built from `deploy/compose.yaml`: Go 1.26.10 on `golang:1.26-alpine3.24`, Alpine 3.24.3, FFmpeg 8.1.3. Host FFmpeg for the native install: 8.0.1-3ubuntu2 (`fd` protocol present; `h264_vaapi` encodes on the 780M through `radeonsi` from `mesa-libgallium`). Media: three files of the agreed set (R05, R07, R10) and a copy of Emby Web, mounted read-only.

Container, with Compose as the README describes:

- **Fresh install:** `docker compose build`, `-init` through `docker compose run -T`, `up -d`: two libraries created from `/media`, 3 videos scanned, Emby Web served at `http://192.168.1.87:8097/web/`, the server name from `COUCH_ARGS`.
- **Upgrade:** the stack was installed from `99c9d16` (the release before #33, an older schema), a movie marked played, then upgraded to the current image with `docker compose up -d`. The old token stayed valid, the mark stayed, and the start loaded the catalog index (`Media index loaded`, the background rescan probed 0 files).
- **Forced stop:** `SIGKILL` sent to Couch's process from the host PID namespace: Docker restarted the container (`RestartCount` 1) with its state. `docker kill` itself counts as a manual stop, after which `restart: unless-stopped` does not restart it.
- **Restore:** a backup written with `-backup` in the running container and copied out with `docker compose cp`; the mark was then removed; after the documented restore the mark was back. Copied-out files belong to the host user with mode 0600, so the restore runs as root in the container and gives them back to `couch`.
- **VA-API:** with `compose.vaapi.yaml` the image is 540 MB and Couch starts with `-hwaccel vaapi`, which encodes a test pattern on `/dev/dri/renderD128` at startup.
- **Client address:** a sign-in to `192.168.1.87:8097` is logged from `192.168.1.87`, and `System/Endpoint` answers `lan`. Sign-ins to `127.0.0.1:8097` and from a container on another Docker network are logged from the gateway `172.19.0.1`, through `docker-proxy`.

Native service: `make release` builds a static 13.6 MB binary with its SHA-256. `systemd-analyze verify` accepts `deploy/couch.service` (it reports only that `/usr/local/bin/couch` is not yet installed), and `systemd-analyze security --offline` rates its exposure 1.8, "OK".

Not checked: running `scripts/install.sh` and the service on the host (it needs root), a sign-in from another LAN host, a host reboot, and Podman (not installed) (#117).

## Collections (2026-11-02, #40)

Emby Web 4.10.0.40 (`-web-dir`) in Chromium against Couch with the 18-file real set, signed in as the administrator:

- **Create:** a movie's "…" menu offers "Add to Collection". "New Collection" sent `POST Collections?Name=…&Ids=…` and showed the "Added to collection" toast.
- **Add:** a second movie was added from the same dialog, which listed the collection under "Recent" and "All Collections". Emby Web posts the IDs as JSON in a `text/plain` body with `reqformat=json`. The first build read only `application/json`, answered 204 and added nothing. Fixed, and the test now posts the body the way Emby Web does.
- **Show:**
  - The Movies library got a Collections tab listing the collection.
  - My Media got a "Collections" tile, and it opens as a list of collections. The sidebar lists it after a reload.
  - The collection page lists its movies under "Movies".
  - A member's page shows the collection under "Included In".
- **Play:** "Play" on the collection card sent `Items?ParentId=<collection>&ProjectToMedia=true&…`. That answered 400 until `ProjectToMedia` was supported; then it played the first movie.
- **Remove:** "Remove from Collection" on the "Included In" card sent `POST Collections/{id}/Items/Delete`, and the row disappeared.
- **Rename:** Edit Metadata on the collection saved a new title ("Item saved.").
- **Edit Images:** the image editor opened on a movie, with no error.
- **Delete:** confirmed with "Are you sure you wish to delete …?", which lists no file paths. It sent `POST Items/Delete`, and Emby Web went back to the previous page.

Other users, "Group into collections", "Mark Played" and favorites are covered by `TestCollections` but were not clicked through. `make e2e` passes the collection operations, `Items/{id}/DeleteInfo`, `Items/Delete`, `DELETE Items/{id}`, `Items/{id}/Images` and `RemoteImages/Providers` against a collection made at startup.

## Playlists (2026-11-02, #103)

Emby Web 4.10.0.40 (`-web-dir`) in Chromium against Couch with the 18-file real set, signed in as the administrator, who owns the playlist:

- **Create:** a movie's "…" menu offers "Add to Playlist". Before any playlist, the dialog listed only "New Playlist". "New Playlist" sent `POST Playlists?Name=…&Ids=…&userId=…` (200).
- **Duplicates:** adding the same movie again listed the playlist under "Recent" and "All Playlists", sent `GET Playlists/{id}/AddToPlaylistInfo`, and asked "This item is already in your playlist." with Add Again, Skip and Cancel. Add Again posted the IDs as JSON in a `text/plain` body (200).
- **Series:** "Add to Playlist" on a series showed "2 items added to …": its two episodes.
- **Show:** after a reload, My Media got a "Playlists" tile, which opens as a list of playlists, and the sidebar got a Playlists section. The playlist page showed "4 Tracks" with drag handles: R16 twice, then S1:E1 and S1:E2. A movie's page shows the playlist under "Included In".
- **Reorder:** dragging S1:E2 to the top sent `POST Playlists/{id}/Items/{entry}/Move/0` (204).
- **Remove:** "Remove from Playlist" on one R16 entry sent `POST Playlists/{id}/Items/Delete?EntryIds=…` (204). After a reload the page showed "3 Tracks": S1:E2, R16, S1:E1.
- **Play:** "Play" sent `Items?ParentId=<playlist>&SortBy=ListItemOrder&SortOrder=Ascending&…` and played S1:E2 first through HLS.
- **Rename:** Edit Metadata on the playlist saved a new title ("Item saved."), and the sidebar showed it.
- **Delete:** confirmed with "Are you sure you wish to delete …?". It sent `Items/{id}/DeleteInfo` (200) and `POST Items/Delete` (204), and the sidebar section disappeared. Emby Web then reloaded the deleted playlist and showed "The requested item was not found."

No server errors were logged. A playlist of a user who is not an administrator, its privacy from an administrator, and a hidden entry whose file is gone are covered by `TestPlaylists` but were not clicked through. `make e2e` passes the seven `Playlists` operations against a playlist made at startup.

## Music (2026-11-02, #104)

Emby Web 4.10.0.40 (`-web-dir`) in Chromium 152 against Couch with only `-media-root`, signed in as the administrator. The media root held a `music` folder of 9 synthetic tracks made with FFmpeg (MP3, FLAC, Ogg Vorbis, Opus, AAC and ALAC in M4A, WMA, WAV, AIFF; tagged, except a WAV under `Artist B/Untagged Album/01 - Plain Track.wav`), a red `cover.jpg` in `Artist A/Album One`, and a `films` folder with R07. Formats and delivery are in [COMPATIBILITY](COMPATIBILITY.md), "Music".

- **Library setup:** New Library offered Music among the content types; with the `music` folder picked in the browser it was created, and the scan logged `videos=0 tracks=9 folders=3 probes=9`.
- **Home:** My Media showed Music and "Latest Music" its albums (Album One with the red cover) and the loose AIFF track. After a 60 s WMA was added and `SIGHUP` sent, the open Songs tab listed it without a reload.
- **Tabs:** Suggestions, Albums, Album Artists (Artist A, B, C), Artists (also "Guest" from `Artist A; Guest`), Genres (Pop, Rock), Songs (tracks of Album One with the album cover) and Folders (albums as folders, the loose track as a file) loaded with no failed request. Every sort the Songs and Albums menus offer answers 200; Bitrate and Composer answered 400 until they were added.
- **Album page:** cover, artist link, year, track count, genre and the tracks by disc and track number; the artist link opened an artist page with songs, albums and albums as a contributing artist. That row listed Album One for its own album artist until contributing artists were made to exclude album artists, and a "composed by" row's `ComposerArtistIds` answered 400 until it was accepted.
- **Playback:** the album card's play button played Album One's MP3, FLAC and Ogg tracks in order, and Mixed's Opus, AAC, ALAC and WMA; the ALAC and WMA tracks were encoded (`Audio encode ended … cpu=102ms`). Clicking a song in the Songs tab ("play from here") failed with "The page range is invalid" until a `Limit` over 1000 was reduced instead of refused (Emby Web asks for 5003). On the 60 s WMA, a click in the middle of the progress bar went on from 32.5 s.
- **Playlists:** "Add to Playlist" → New Playlist on a song created "Road Trip" (`MediaType: Audio`); adding Album One from its card added its 3 tracks. The playlist showed under the Music library's Playlists tab and in My Media, and "Play" played it.
- **Continue Listening:** stopping the WMA at 32 s put it in a Continue Listening row with its progress, above Latest Music.
- **Favorites:** the heart on Artist A's page marked it favorite (404 before artists could be favorites), and the home Favorites tab listed it under Favorite Artists. It also showed under "Favorite Authors" until `Artists` honored `CollectionTypes=audiobooks`.
- **Instant Mix:** the item menu offers it; the error dialog said "The requested item was not found." until its routes answered "Instant Mix is not available in Couch yet." Since #119 (2026-11-02) Instant Mix on "First Song" sent `Items/{id}/InstantMix?Limit=5000` (200) and played a queue of First Song, then Third Song and Second Song, which share Artist A or the Rock genre; tracks of other artists and genres stayed out.
- **With a Movies library:** a Films library added on the `films` folder showed beside Music; Continue Watching stayed empty, Continue Listening kept the WMA, and Latest Movies showed R07.
- **Restart:** the saved index loaded with `tracks=10` and the background rescan probed nothing.

- **Embedded cover (2026-11-02, #120):** an MP3 whose only cover is a green JPEG in its ID3 tag, in `Artist D/Embedded Art`, showed that cover on its album card and album page. The restart rejected the version 3 index and scanned again (`media index is for another version`), and the cover was cached once in `covers/` (765 bytes).

No server errors were logged. A music library shared with a movies library's folder, the saved index with tags, the artist filters, an album added to a playlist in track order and marking an album played are covered by `TestMusicLibrary` and `TestMusic`; encoding once per track for concurrent requests and keeping 32 encoded tracks by `TestAudioEncodesOnceAndKeepsRecentTracks`; MP3 and FLAC covers, an album taking its first track's, the cache and a local cover winning by `TestEmbeddedCovers`; Instant Mix from every kind of seed and its limit by `TestMusic`, and its seven operations in `make e2e`, which now adds a Music library on its sample track. Real music collections, other clients and gapless playback were not checked.

## Home Screen settings (2026-11-03, #100)

Emby Web 4.10.0.40 (`-web-dir`) in Chromium against Couch with `-media-dir` on the real-file set (16 movies, one series), signed in as the `-init` user.

- **Editor list:** Settings → Home Screen listed My Media, Continue Watching and Latest Media, from `HomeSections?isEditMode=true`.
- **Add:** Add → Dynamic Media, type Movies, custom title "Films to watch", Playstate Unplayed → Save sent `POST Users/{id}/HomeSections` (204). The section came back with its type, title, item types and `IsPlayed: false`.
- **Move and remove:** dragging "Films to watch" to the top sent `HomeSections/Move` (204). Removing Continue Watching after the confirmation sent `HomeSections/Delete` (204).
- **Edit:** unticking TV Shows under Libraries on Latest Media and saving left one latest row.
- **Home:** rows in order: Films to watch (R01, R02, R03…), My Media, Latest Movies. No Continue Watching, no Latest Episodes. The row's "›" link opened its list page with all 16 movies.
- **Restart:** after the server was stopped and started again, the home screen kept that layout.
- **Refusal:** saving an On Now section answered 400 "Couch has no Live TV, so this section would always be empty."

No server errors were logged. The other section types, the display-mode choice, the play-state filter of Latest Media, edits of a missing section and the stored layout across a database upgrade are covered by `TestHomeSectionsCanBeEdited` and `TestHomeSections`.

## Home videos and photos (2026-11-03, #105)

Emby Web 4.10.0.40 (`-web-dir`) in Chromium against Couch with only `-media-root`, signed in as the `-init` administrator. The media root held a `photos` folder: `Trip 2024/Beach.jpg` (EXIF orientation 6, taken 2024-07-01 10:30 +02:00), `Trip 2024/Sunset.webp`, `Trip 2024/Wave.gif` (animated), `Trip 2024/Clip.mp4` (5 s H.264/AAC), `Trip 2024/Day 2/Hill.png` with a red `folder.jpg`, and `Loose.jpg` and `Old.bmp` at the top. Formats are in [COMPATIBILITY](COMPATIBILITY.md), "Photos".

- **Library setup:** New Library offered "Home videos & photos"; with the `photos` folder it was created, and the scan logged `videos=1 photos=6 folders=2 probes=1`: `folder.jpg` was not taken for a photo, and only the clip went through FFprobe. The "Enable photos" switch was off in the dialog until `DefaultLibraryOptions` set it on.
- **Home:** My Media showed Family, and "Latest from Family" the photos and the clip, Beach upright.
- **Tabs:** Videos listed Clip; Photos the six photos, every format drawn; Folders the Trip 2024 album with Beach as its image, Loose (2025, its mtime) and Old. Every request answered 200, 204 or 206.
- **Slideshow:** a photo on the Photos tab opened the slideshow at that photo, and the arrow key went on to the next. The Trip 2024 album card played its photos (Beach, Sunset, Wave) from `Items?ParentId=<album>&Filters=IsNotFolder&MediaTypes=Photo,Video`, as Emby Web's playback manager does for every photo album; the slideshow passed over the clip.
- **Video:** Play on the Videos tab played Clip directly (`stream.mp4`, 206) and reported its progress and stop.
- **Playlist:** the photo menu (titled "Beach 2024", the EXIF year) offered Add to Playlist; New Playlist "Best shots" was created (`POST Playlists`, 200, without a `MediaType`), showed in My Media → Playlists with `MediaType: Photo`, and its card played Beach as a slideshow. Before, Couch reported every non-audio playlist as `Video` and refused `MediaType=Photo`.
- **Restart:** the saved index (version 5) loaded with `photos=6`, and the background rescan probed nothing.
- **500 photos:** a library on one folder of 500 noisy 3000×2000 JPEGs (1.8 GiB) made with FFmpeg, on the host without a container. The scan read their headers within the same second, without FFprobe. Fetching every photo's Primary image as the Photos tab asks for it (`maxWidth=375`), 16 at a time with curl, answered 500 × 200 with the 1.8 GiB of originals in 0.55 s from the page cache. Couch peaked at 23 MiB RSS and 0.45 s of CPU, and at 28 MiB in the run that also scanned them, against Emby's idle 201–205 MiB in the M0 budgets ([ROADMAP](ROADMAP.md)). Emby itself was not measured on photos, and a browser gets each original, about 3.6 MiB here, where Emby would send a scaled image.

No server errors were logged; the console showed only the blocked Premiere check (#89). Nested albums, a file in both a TV shows and a home videos library, the EXIF parser, a video's own images, the saved index and the server's answers are covered by `TestHomeVideosLibrary`, `TestJpegExifRejectsBadSegments` (media) and `TestPhotos` (server). Real camera photos, large folders and other clients were not checked.

## HDR reference capture and tone mapping (2026-11-03, #125)

**Reference capture.** Authorized by the owner on 2026-11-03: a read-only capture as in #22. Sign-in as `oleg` with the device `couch-capture-hdr`, then `Sessions/Logout` (204) and token revocation confirmed by a 401. The reference now reports version **4.10.2.0**, not 4.10.0.40; Couch stays on 4.10.0.40 until #126 moves it. `oleg` is not an administrator, so `Encoding/PublicToneMapOptions` answered 403 and Emby's FFmpeg logs could not be read. Kept: the field values below, no raw data.

- Of 11,620 movies and episodes, 13 video streams are HDR10, 9 Dolby Vision, the rest SDR; none is HLG.
- HDR10 (HEVC Main 10, 1920×800): `VideoRange: "HDR 10"`, `ColorTransfer: smpte2084`, `ColorPrimaries: bt2020`, `ColorSpace: bt2020nc`, `ExtendedVideoType: Hdr10`, `ExtendedVideoSubType: Hdr10`, `ExtendedVideoSubTypeDescription: "HDR 10"`, `DisplayTitle: "1080p HDR 10 HEVC"`.
- Dolby Vision (HEVC Main 10, 3840×2160): `VideoRange: "DolbyVision"`, the same colour fields, `ExtendedVideoType: DolbyVision`, `ExtendedVideoSubType: DoviProfile81`, `ExtendedVideoSubTypeDescription: "Profile 8.1 (HDR10 compatible)"`, `DisplayTitle: "4K Dolby Vision HEVC"`.
- SDR streams report `VideoRange: "SDR"`, `ExtendedVideoType: None`, and whatever colour tags the file has (`bt709`, `bt470bg`, `smpte170m`, `reserved`), or none.
- **Transcode:** PlaybackInfo with Emby Web's Chromium profile and a 3 Mbit/s limit gave an H.264 `TranscodingUrl` (`TranscodeReasons=ContainerBitrateExceedsLimit`) for one item of each kind. One segment of each was fetched, its encoding stopped with `DELETE Videos/ActiveEncodings` (204), and FFprobe read `h264`, `yuv420p`, `smpte2084`/`bt2020`/`bt2020nc`: Emby passes the HDR tags through without tone mapping, as Couch did. Whether an administrator setting would make it tone-map was not visible to this account.

Couch now reports those values. HLG has no reference item, so `VideoRange: "HLG"` and `ExtendedVideoType: HyperLogGamma` are assumptions, as are descriptions of Dolby Vision profiles other than 8.1. The 1920×800 stream shows that Emby names the resolution by width, where Couch says 720p; not changed here.

**Tone mapping.** On the Radeon 780M, `tonemap_vaapi` fails (`VAAPI driver doesn't support HDR`) and OpenCL has no platform, so the GPU path is libplacebo on Vulkan; in the image it needs Mesa's `mesa-vulkan-ati`, now part of `--build-arg VAAPI=1`. `TestToneMapTranscode` transcodes a synthetic HLG file with each method this machine runs and checks BT.709 segments and the `tonemap=` log field; on the host (FFmpeg 8.0.1) both `zscale` and `libplacebo` ran. In Emby Web, R09 and a synthetic HLG file played through the VA-API image with SDR colours, R09 with seek, stop and resume ([COMPATIBILITY](COMPATIBILITY.md), "HDR tone mapping"). The cost of each path is in [BASELINE](BASELINE.md), "HDR tone mapping". Compared with the HDR picture as it is (washed out on a client that ignores the tags), libplacebo keeps saturated colours, slightly less contrast than `zscale` with `mobius` on the synthetic bars.

**Saved index.** It is now version 6, with the colour properties. Versions 1, 2, 4 and 5 are still read and served at once; their videos carry a mark, kept in the index until a rescan, that makes the background rescan probe them again instead of reusing their streams (`TestSavedCatalog`). Their first-seen dates are kept.

**Review fixes (same day).**
- **Software Vulkan:** in the image with only Mesa's lavapipe (`mesa-vulkan-swrast`) and no render node, FFmpeg's own Vulkan device would be `llvmpipe (software)`, but libplacebo answered `Found no suitable device, giving up`, so the startup check fails and `auto` turns tone mapping off.
- **Burned-in subtitles** are now overlaid after tone mapping. A 50 % grey HLG video made with libplacebo, with R07's DVD subtitles muxed in, ran through Couch's graph with both methods. Against the same subtitles overlaid on SDR grey, libplacebo's frames scored 44.1 dB PSNR in the new order and 39.5 dB in the old one. `zscale` scored 18.4 and 18.6 dB, dominated by how it maps the libplacebo-made grey.
- **HLG in `zscale`:** SDR test bars converted to HLG and PQ by libplacebo (BT.2408 reference white), then tone-mapped back by `zscale` + `mobius`, against the SDR original (PSNR Y): HLG 27.7 / 22.5 / 12.4 dB at `npl=100` / `203` / `1000`, PQ 27.1 / 22.4 / 12.3 dB. `npl=100` stays for both.
- **Dolby Vision** without a transfer tag, or profile 5, now counts as HDR: libplacebo tone-maps it, and `zscale` leaves it alone and the job logs `tonemap=off` (`TestEncodeToneMapsHDR`).
- **`-tonemap` selection** moved to `Manager.SetToneMap` and is covered by `TestSetToneMap` with a fake FFmpeg.

Not checked: Dolby Vision profile 5 on a real file (no sample), Intel or Nvidia GPUs, a burned-in subtitle with tone mapping in Emby Web, `-tonemap software` in Emby Web, and other clients.
