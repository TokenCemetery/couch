# M2 audit — 2026-09-20–21

Scope: M2 criteria from the [roadmap](../ROADMAP.md), FR-03–FR-09 from the
[PRD](../PRD.md). Baseline: `172bee0` and the uncommitted WebSocket stage.

## Strengths

- The catalog limits traversal and FFprobe; files are opened via `os.Root`.
- Streaming uses `ServeContent` and does not load the whole file; there are tests for Range,
  HEAD, 416, file changes and a slow client.
- State is written atomically. WebSocket sends UserDataChanged after
  saving; queues are bounded, logout and shutdown close connections.
- The original API client verifies login, PlaybackInfo, progress and restart.
  This is not a check of the DOM or of video decoding by the browser.

## Defects in the initial snapshot

Below are the original findings; fixes and remaining gaps are listed separately.

| Priority | Location | Evidence and minimal fix |
|---|---|---|
| P1 | `internal/server/catalog.go`, `listItems` | IsPlayed/IsFavorite=true always exclude the item, false ignores the state. Filter by the saved UserData, add tests after the flags change. |
| P1 | `internal/server/dto.go`, `movieDTO`; server routes | ImageTags is always empty; images are not served. Needs local posters, authorization, size limits and correct cache validators. |
| P1 | `internal/media/catalog.go`; `internal/server/catalog.go` | Every video file is a Movie. No Series/Season/Episode and no season/episode routes. This is an explicit M2/FR-03–04 gap, not an M3 feature. |
| P2 | `internal/server/playback.go`, `reportPlaystate` | A report without PositionTicks resets the position to zero. Keep the previous position when the field is missing; validate negative values the same way for JSON/query. |
| P2 | `internal/server/playback.go`, `reportPlaystate` | A repeated start increments PlayCount; a late progress after stop restores the position. PlaySessionId is not tracked; FR-08 is not yet fully met. |
| P2 | `internal/server/playback.go`, Resume route | Limit/StartIndex are ignored, TotalRecordCount is truncated to 24. Needs filters and pagination before building DTOs. |
| P2 | `internal/server/home.go`, `sectionItems` | Latest Movies advertises IsPlayed=false but includes played movies. Use the saved flag. |

## Verification gaps

- No current browser acceptance of "search → item page → play → seek → stop →
  restart → resume" on a long video. On 2026-09-21 the built-in browser showed
  login, the home page and a poster; after the search fix, the recheck was
  stopped by the tool's automatic access-permission limit.
- The series scenario has not yet been accepted by the original UI.
- The provided UI copy lacks `modules/apphost.js`: loading
  stops. Browser checks were run via `-web-upstream`;
  a standalone check of the API modules does not confirm the UI is complete.
- The full media set is not agreed. A synthetic MP4 does not confirm all codecs.
- No comparative resource measurements against Emby; that is M0/M4, not a proven M2 improvement.

## Fixed during the audit

- Played/Favorite/IsUnplayed respect the saved state; Latest Movies
  excludes played items. Resume applies filters, sorting, Limit/StartIndex
  and returns the total count before the page is truncated.
- A missing position is kept, a negative one is rejected. The 90% check
  no longer multiplies the int64 runtime with an overflow risk.
- The last 64 PlaySessionIds per client session are saved atomically with UserData.
  A repeated start does not increment the counter; progress/stop are accepted only for
  the last unfinished playback. Without an ID, arrival order applies;
  a start evicted from the window counts as new. Seeking backward is allowed.
- SetItem now returns ErrStateLimit when the limit is exhausted, instead of success
  without a change. UserDataChanged is still published after the write.
- Added Series/Season/Episode, a separate TV Shows, Seasons/Episodes API,
  item pages/search and Direct Play of episodes. Only
  `Show/Season 01/Show.S01E02.mp4` is recognized; ambiguous names remain movies.
  IDs and the hierarchy are stable after a rescan.
- Added a test for cancelling a real HTTP stream: the handler finishes and
  closes the file; deleting the test file afterwards yields 404. The test passed
  three times. A 64 MiB sparse file is not loaded fully into RAM.

### Images and search

The original `getImageUrl` does not add the API token. The user approved
signed ImageTags: access to a specific revision of the Primary poster is implemented
until logout/session expiry, and persists across restarts. Local JPEG/PNG files
are limited by size and header and are opened inside the media library root.
The original is served without resize; the revision is by path/size/mtime, not a content hash.
Tests cover isolation, forgery, revocation, restart and HTTP caching; the original
API client builds a working URL, and the browser shows the poster.

The UI's global search revealed a missing ItemTypes and the rejection of the
GroupProgramsBySeries/IncludeSearchTypes parameters. A route and validation were added;
types are counted over the filtered set before pagination. The full getSearchResults
is verified by the original API client; the browser recheck is still open.

## Recommendations

1. Complete browser acceptance of search, series and long video after
   the tool's access is restored; only then close M2.
2. Verify a standalone launch with the full set of Emby Web resources.
3. Agree on the media set and separately run comparative resource measurements.

DRY: no confirmed duplication requiring refactoring for M2 was found.
YAGNI: transcoding, metadata providers and a persistent index are not added
in this stage; they have separate requirements and later stages.

Verdict: the backend is recorded as a verified checkpoint; M2 stays open
until full client acceptance. Results: [VALIDATION](VALIDATION.md).

## Closure (2026-10-03)

Recommendation 1 is done: browser acceptance of search, series and long video
passed after fixing `IsSpecialSeason` and the series episode query
parameters. M2 is accepted on a synthetic H.264/AAC MP4. Recommendations 2–3 and search
history (`SearchedItems`/`WasSearched`) remain open. Details:
[VALIDATION](VALIDATION.md#m2-browser-acceptance-2026-10-03).
