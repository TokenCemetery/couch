# Couch

<img src="docs/logo.svg" alt="Couch logo" width="132">

A Go backend compatible with Emby Web: a local user account, sessions, settings, movies and a basic TV series hierarchy, direct HTTP video delivery and saved playback position. The media library is read-only.

The reference is Emby Web **4.10.0.40**. The original API modules and the browser scenario "search → item page → play → seek → stop → restart → resume" have been verified; the limits of that verification are described in [VALIDATION.md](docs/VALIDATION.md), and the verified combinations of formats and delivery methods in [COMPATIBILITY.md](docs/COMPATIBILITY.md). This is a prototype, not a replacement for a production Emby. Resource use is compared with Emby in [BASELINE.md](docs/BASELINE.md): Couch uses less at idle, in the API, Direct Stream and remux; transcoding uses about the same CPU but starts and seeks slower.

## Running

Requires Go 1.25+ and Linux/macOS.

```sh
go -C src build -o ../bin/couch ./cmd/couch
```

Create a local user. The password is passed via stdin, at least 12 bytes. Example for the current zsh shell; the input is hidden and does not end up in the command history:

```zsh
read -rs 'couch_password?New Couch user password: '
printf '\n'
printf '%s\n' "$couch_password" | ./bin/couch -init -username viewer
unset couch_password
```

Start the server with a full local copy of Emby Web:

```sh
./bin/couch -web-dir ../emby-webui
```

Address: <http://127.0.0.1:8097/>. The user `viewer` and the password set above belong to Couch; Emby accounts are not imported.

`-web-dir` points to the folder containing `index.html`, not its parent. Couch reads the UI unmodified; it adds `data-appversion="4.10.0.40"` to the served source `<html>`. This is a loader setting, not automatic version detection for an arbitrary UI. The provided API modules match the 4.10.0.40 reference. The provided copy was missing four files the client loads at startup: `modules/apphost.js`, `modules/input/keyboard.js`, `modules/virtual-scroller/virtual-scroller.js` and `.css`; on 2026-10-06 they were copied from the reference Emby 4.10.0.40, and Emby Web starts with `-web-dir` (#19). Of the remaining 307 files the client loads on the home screen, in libraries, search, series and movie pages, all user settings and the playback screen, 306 are byte-identical to the reference; `settings/appgeneral.html` differs only in the encoding of the "Norwegian Bokmål" label (checked 2026-10-04). Only static files inside the chosen directory are served, with no listing, no hidden files and no escape via symlink. Do not point it at a folder with private data: allowed HTML/JS/JSON and other resources are available without signing in. UI files are not included in the Couch binary or repository.

The alternative is `-web-upstream http://192.168.1.11:8096`: `/web/*` fetches the static files of the source Emby via GET/HEAD. Tokens, cookies, the request body and arbitrary query parameters are not passed upstream; redirects are not followed. At most 16 requests go to Emby at once; the rest wait up to 10 seconds for a free slot and then get 503 with `Retry-After`; a file larger than 32 MiB is not served. There is no cache. This mode requires a reachable Emby. `-web-dir` does not contact it for resources. In both modes the UI is served with `Content-Security-Policy: connect-src 'self'`, so the browser lets the client's fetch, XHR and WebSocket reach only Couch. This blocks the Emby Premiere registration check that Emby Web makes on every load, which would send the Couch ServerId, DeviceId, device name and app version to `https://mb3admin.com/admin/service/registration/validateDevice`; the client logs `getRegistrationInfo failed`, shows the "Get Emby Premiere" button and keeps working (#89).

The `-web-dir` and `-web-upstream` options are mutually exclusive. Without either, only the API and the information at `/` are available. In all modes, authentication, the catalog and video are served by Couch.

Other options: `-listen` (default `127.0.0.1:8097`), `-data` (`./data`), `-name` (`Couch`), `-ffmpeg-threads` (`0`). `-ffmpeg-threads N` caps the decoder and `libx264` encoder threads of each transcode at N; remux is not affected. By default FFmpeg sizes them from the CPUs it sees, which ignores a container CPU quota; under a quota a cap that matches it halves transcoding memory (#92, [VALIDATION](docs/VALIDATION.md)). The default listener is local; TLS and running behind a reverse proxy are not configured yet. On a graceful shutdown (SIGINT, SIGTERM) Couch writes a `Couch stopped` line: its own CPU time and peak resident memory (`cpu`, `peak_rss_kib`) and, separately, the total time and the highest peak of finished FFmpeg and FFprobe child processes (`children_cpu`, `children_peak_rss_kib`) over the whole run.

### Container

The [Dockerfile](Dockerfile) builds an image: a static `couch` on Alpine 3.24 with FFmpeg/FFprobe 8.1 (`fd` protocol, `libx264`, `aac`). The process runs as user `couch` (UID 10001), state is kept in the `/data` volume, and it listens on `:8097` inside the container. Example for Podman; Docker accepts the same commands:

```sh
podman build -t couch .
read -rs 'couch_password?New Couch user password: '
printf '\n'
printf '%s\n' "$couch_password" | podman run --rm -i -v couch-data:/data couch -init -username viewer
unset couch_password
podman run -d --name couch -p 127.0.0.1:8097:8097 -v couch-data:/data -v /absolute/path/to/media:/media:ro couch -listen :8097 -media-dir /media -web-upstream http://192.168.1.11:8096
```

Arguments after the image name replace `-listen :8097`, so `-listen` is given explicitly; `-data /data` is set in the image. For `-web-dir`, the Emby Web directory is mounted separately (for example, `-v /absolute/path/to/emby-webui:/web:ro` and `-web-dir /web`). Rescan: `podman kill -s HUP couch`. Sign-in rate limiting counts the client address as the container sees it; with port forwarding this may be a shared gateway address.

`scripts/check-container.sh IMAGE` checks the image: `-init`, scanning an MKV H.264 + AC3 and remux to HLS (FFprobe reads `h264` + `aac`), and shutdown with exit code 0. Docker is used by default, Podman via `CONTAINER=podman`. Requires `curl` and `jq`.

## Movie and TV series catalog

To scan, specify a single test folder and install FFprobe with support for the `fd` protocol:

```sh
./bin/couch -media-dir /absolute/path/to/test-movies -web-dir ../emby-webui
```

Scanning runs before the HTTP server starts. A rescan without restarting is triggered by the `SIGHUP` signal (`kill -HUP <pid>`) and, if `-rescan-interval` is set (for example, `30m`, at least `1m`), periodically; by default there is no periodic scan. On a rescan, files with the same path, size and mtime do not go through FFprobe again; added, changed and deleted files become visible after the new catalog is published, and IDs of unchanged paths are kept. One scan runs at a time: a `SIGHUP` during a scan starts another one after it. A traversal error, exceeded limits, or a catalog that suddenly has no videos left (this is what an unmounted NAS volume looks like) are not published: the previous catalog stays, and `Media rescan failed; keeping the previous catalog` is written to the log. After publishing a changed catalog, Couch sends `LibraryChanged` over WebSocket to all connections: added, changed (size, mtime, duration) and deleted IDs, their parent folders and the affected libraries; open Emby Web screens (the library, "Recently Added" on the home screen) refresh themselves. The field names are taken from the client handler; a reference Emby message has not been captured. Without `-media-dir` the library stays empty; FFprobe is not required. Source files are not modified. Files are grouped into `Movies`, except for explicitly recognized episodes.

The structure `Show/Season 01/Show.S01E02.mp4` inside `-media-dir` creates a `TV Shows` library, a series, a season and an episode. The season number in the folder and the name must match; `Season 00` is supported. Other names remain movies. Movie IDs do not change; series/season IDs are stable while the path is unchanged. Item pages, search, Seasons/Episodes and direct episode delivery are supported; "Play" on a series plays the episodes in order in the client queue. `Shows/NextUp` returns, for each started series, the episode after the furthest one watched; series are ordered by last watched, and `Season 00` specials are not counted. The Continue Watching row on the home screen (`Sections/resume/Items`, the section declares `IncludeNextUpInResume`) and `Items/Resume` add these episodes to the started items (as in the reference Emby, `Items/Resume` adds them even without `IncludeNextUp`; only `IncludeNextUp=false` excludes them — this is an assumption, not captured); when sorting by date played, the episode gets the last-played date of its series. This order is an assumption: the Emby response has not been captured. Alternative naming schemes and online metadata are not implemented. Home screen row titles follow `X-Emby-Language` for Russian, German, French, Spanish and Italian (Emby Web client wording); for other languages they are English. Translations live in `internal/server/locales/<language>.yaml` (key: text, keys are Emby Web strings) and are embedded in the binary; `en.yaml` contains all keys and serves as the fallback language. A new language is a new file; a test checks that it has no keys missing from `en.yaml` and no empty values. Item titles are not translated: there is no source of localized titles, so the original title is shown.

- Extensions: mp4, m4v, mkv, webm, avi, mov, mpg, mpeg, ts, m2ts, wmv, ogv. A detected video track is required; broken files are skipped and counted in the log without paths.
- The title comes from the file name; FFprobe provides the duration, codecs, frame dimensions, and audio and subtitle tracks. NFO and external providers are not available yet.
- The ID depends on the absolute library path and the relative file path: it persists across restarts and changes on move/rename. `DateCreated` is the time Couch first saw the path: for files found at startup, the file modification time; for files found by a rescan, the time of that rescan. Latest and sorting by DateCreated use it. The date is not saved to disk: after a restart it again equals the file modification time.
- The catalog is kept in memory, separately from `state.json`. There is no persistent index: every start scans the entire library with FFprobe. A file change that keeps the size and mtime is not detected by a rescan.
- The `Media scan complete` and `Media rescan complete` lines report how many times FFprobe ran in this scan (`probes`; unchanged files are not probed on a rescan), its total CPU time (`probe_cpu`) and the highest peak memory of a single run (`probe_peak_rss_kib`).
- Limits: 10,000 video files, 100,000 traversal entries, depth 64, scanning up to 10 minutes; one FFprobe at a time, up to 15 seconds and 1 MiB of JSON per file. Exceeding a global limit stops startup (on a rescan, keeps the previous catalog) instead of publishing an incomplete catalog. The timeout does not guarantee interruption of hung OS file I/O.
- Symbolic links and special files are not traversed. Files are opened inside `os.Root`; FFprobe receives a descriptor, not a path. Only the `fd` protocol is allowed, with no network requests and no opening of neighboring files. This is an access restriction, not a full sandbox for FFprobe.

Supported: search by name, ParentId, Recursive, IDs/exclusions, media types, `CollectionTypes` (`movies` — movies and the Movies library, `tvshows` — series, seasons, episodes and the TV Shows library; other types return an empty page), `CanEditItems` (`true` — an empty page: the library is read-only, there are no playlists or collections), filters on FFprobe data — `Containers`, `VideoCodecs`, `AudioCodecs`, `AudioLayouts`, `SubtitleCodecs`, `AudioLanguages`, `SubtitleLanguages`, `ExtendedVideoTypes` (a series and season match by their episodes) — and basic filters. The library "Filter" dialog gets values from `/Containers`, `/VideoCodecs`, `/AudioCodecs`, `/AudioLayouts`, `/SubtitleCodecs` and `/StreamLanguages` (by `StreamType`) within `ParentId` and `IncludeItemTypes`, paginated (`StartIndex`, `Limit` up to 1000, as for Items: the client checks each list with a `Limit=1` request); Couch has no genres, studios, tags, parental ratings or years, so their lists are empty and filtering by them finds nothing (#78). `/Artists` and `/Persons`, requested by the home screen "Favorites" tab, are empty too: there are no music libraries or people data (#80). Sorting is a comma-separated list of fields from Name/SortName, DateCreated, DatePlayed, DateLastSearched, Runtime, ParentIndexNumber, IndexNumber, SeriesSortName, IsFolder (folders first), Filename, TotalBitrate, VideoCodec, Container, Size, Resolution, Framerate, PlayCount, SeriesDatePlayed (last watched episode of a series or season), DateLastContentAdded (newest episode) and Random (shuffle; subsequent keys do not change the order). ChannelNumber, OfficialRating, ProductionYear, PremiereDate, CommunityRating, CriticRating, Director and LastContentPremiereDate are accepted and do not change the order: Couch has no channels or such metadata, so the next key decides (#79). Fields are compared in order; `SortOrder` is shared. `IsSpecialEpisode` selects `Season 00` episodes. `LiveTv/Programs` and `Movies/Recommendations` are always empty: there is no Live TV and no recommendations. A page contains up to 100 items by default, Latest up to 20; `Limit` from 0 to 1000. Unknown filters/sorts return 400. Position, play count, and "played" and "favorite" marks are saved in the user state. Marking a series or season as played sets or clears the mark on all its episodes; a series or season counts as played when all episodes are played, and reports `UnplayedItemCount`. "Remove from Continue Watching" (`HideFromResume`) resets the saved position and marks the item hidden so it does not come back as the next episode; the mark is cleared on the next playback or with `Hide=false`, and the position is not restored. A home screen row returns at most 1000 items: a larger `Limit` (the item menu requests 5000) is reduced, not rejected.

## Artwork

Primary images are read from local `.jpg`, `.jpeg`, `.png` files (lowercase extensions). For videos the lookup order is: `<name without extension>-poster`, `<name without extension>`, then `poster` and `folder` in the same folder. For series/seasons: `poster` and `folder` in their folder.

The backdrop (item page background) is looked up the same way, by its own names: for videos `<name without extension>-fanart`, `<name without extension>-backdrop`, then `backdrop` and `fanart` in the same folder; for series/seasons, `backdrop` and `fanart` in their folder. One image per item (`Images/Backdrop/0`), in `BackdropImageTags`. The logo (`Logo`: `<name>-clearlogo`, `<name>-logo`, then `clearlogo`, `logo`) and the landscape image (`Thumb`: `<name>-landscape`, then `landscape`, `thumb`) are looked up the same way; they go into `ImageTags`. A season or episode without its own backdrop, logo or Thumb gets the series image, as in Emby: `ParentBackdropItemId` and `ParentBackdropImageTags`, `ParentLogoItemId` and `ParentLogoImageTag`, `ParentThumbItemId` and `ParentThumbImageTag`. A season and an episode always get the series image in `SeriesPrimaryImageTag`, as a season does in the reference: Emby Web shows it on an item card without its own image, in the "Now Playing" bar and in the system media controls.

Limits: file up to 8 MiB, header up to 1 MiB, side up to 16,384 pixels, area up to 32 million pixels. Access is restricted to the media library root; the server does not decode pixels. The original is served with no resize, quality conversions or overlays. The `.jpg`/`.jpeg`/`.png` names in an item's folder are read as one list and cached until the folder mtime changes (a list taken within two seconds of the mtime is not reused); a new image added without changing the folder mtime is not visible until its next change or a restart.

Emby Web gets a signed ImageTag and passes it in `tag` without an API token. The link authorizes only that specific image revision and is valid until logout/session expiry; a graceful restart keeps it. Do not publish such links: they are limited access keys. A regular API token is also accepted. Authorization is checked before a 304 response; the cache is private, with mandatory revalidation. The revision and weak ETag depend on the path, size and mtime, not the content: a replacement that keeps the size/mtime is not detected.

Changing the password: `POST /Users/{id}/Password` with `CurrentPw` and `NewPw` (12–1024 bytes) checks the current password under the same rate limit as sign-in; the current session remains, the others are revoked. Emby Web 4.10 hides the current password field for servers from 4.8.0.38 and sends it empty. Per decision #46, Couch still requires the current password: an empty one is rejected with 400, a wrong one with 401 (a text message); the profile form shows the client's generic error "Invalid username or password", and the session is kept.

The user avatar is uploaded and deleted on the Emby Web profile page. JPEG and PNG are accepted with the same limits as artwork; the file is stored as uploaded, in the `-data` directory next to `state.json` (`user-image-<revision>`). The revision is a hash of the content. `PrimaryImageTag` is signed the same way as an artwork ImageTag and is not valid for item images.

## Playback

Couch serves the source file via Direct Stream: GET/HEAD, Range, 206/416 and Resume are supported. If the client does not accept the file as is, but its HLS profile (`TranscodingProfiles`: `Protocol=hls`, `Container=ts`) accepts the video codec, Couch does a remux to HLS: video is copied without re-encoding, audio is copied if the profile accepts its codec, otherwise it is encoded to AAC (channels up to `MaxAudioChannels`, stereo without it). PlaybackInfo returns `TranscodingUrl` (`/videos/{id}/master.m3u8?...`, the token in the link, as for the stream), `TranscodingSubProtocol=hls`, `TranscodingContainer=ts`. The `main.m3u8` playlist is a VOD for the whole movie; MPEG-TS segments are cut at the source keyframes, no shorter than 6 seconds; FFprobe finds the keyframes on the first request by seeking every 2 seconds, reading a few packets per seek; MPEG-TS (where a seek does not land on a keyframe) is read in full (the list is cached in memory for up to 64 files; PlaybackInfo starts the read in advance; at most two files are read at once, the rest wait in a queue). Seeking beyond what is ready restarts FFmpeg from the needed segment, with timestamps matching a continuous pass. Limits: two remux sessions and one transcode session at a time (the next one gets `RateLimitExceeded`, an HLS request gets 503), segments are stored in `<data>/transcode` (cleared at startup and shutdown), a session holds up to 2 GiB; FFmpeg is paused (SIGSTOP) once it is 10 segments ahead and resumes when the client catches up; old segments are deleted. A session ends on `Sessions/Playing/Stopped`, `Videos/ActiveEncodings/Delete` (`POST`, or `DELETE Videos/ActiveEncodings`; without `PlaySessionId`, all of the caller's sessions), logout, or after a minute without requests. Without FFmpeg in PATH, remux and transcode are disabled. If the HLS profile does not accept the video itself (codec, profile, level, bitrate; for example, HEVC 10-bit or MPEG-4 Part 2 for a browser) but accepts `h264`, the video is encoded to 8-bit H.264 (`libx264`, `veryfast`, CRF 23) at the same size (an odd dimension is rounded down to even); `TranscodingUrl` contains `VideoCodec=h264` and `VideoBitrate`, the `-maxrate` ceiling: the source bitrate; with a client limit, no higher than the limit minus 192 kbit/s for audio (no less than half the limit and 100 kbit/s); without data, 8 Mbit/s. `LessThanEqual` conditions on `Width`, `Height` and `VideoBitrate` from the `h264` codec profile (without `ApplyConditions`) constrain encoding: the picture is downscaled preserving the aspect ratio (`MaxWidth`/`MaxHeight` in the URL), and the bitrate is capped at the lower of the ceilings. Emby Web sets `Width` to 1920 if the browser does not decode 4K smoothly. Audio is handled as in remux. Segments are cut by the same source keyframe plan, and the encoder places forced keyframes at those points, so seeking by restart produces the same segments. The `VideoLevel` condition of the same profile downscales the picture to the largest frame for that level (MaxFS per H.264 Table A-1: for example, 4K at level 4.2 is about 1980×1114); the bitrate ceiling does not exceed the level's MaxBR for the High profile (×1.25); the level in the stream is chosen by `libx264`. The level's macroblock rate (MaxMBPS) is also respected by downscaling the frame at the source frame rate: 1080p60 at level 4.1 is about 1358×764, and the frame rate is preserved. Restarting encoding from the middle decodes from the keyframe of the previous segment and trims to the start of the needed one with the `trim`/`atrim` filters, so even HEVC with open GOPs in MPEG-TS produces the same segments as a continuous pass (#81); with copied audio (which cannot be filtered) a direct `-ss` to the segment remains, and for such HEVC in MPEG-TS a restart may start later than planned. Encoding limitations: hardware acceleration is not used (#28). Otherwise the response is `NoCompatibleStream`. With FFmpeg, the user policy allows video transcoding and remux (`EnableVideoPlaybackTranscoding`, `EnablePlaybackRemuxing`), and PlaybackInfo for a direct stream reports `SupportsTranscoding` if HLS can also serve it: this makes Emby Web show the "Quality" item in the player, and the chosen bitrate arrives in a new PlaybackInfo with `MaxStreamingBitrate` (#72). Such a PlaybackInfo names the current playback in `CurrentPlaySessionId`: Couch ends its HLS session (the client deletes it only after the response, and switching from one transcoded quality to another would otherwise hit the limit) and adds the new `PlaySessionId` to the same playback. After the switch, Emby Web sends progress and stop under the new ID if the stream changed, and under the old one if not; both are accepted, and the position is saved (#73). Compatibility still depends on the container, codecs and client. Seek previews are not generated: `ThumbnailSet` returns an empty set. The "Download" command (`Items/{id}/Download`) serves the source file as an attachment; the file name is the item title with the source extension. Each FFmpeg run, when it finishes, writes an `HLS job ended` line to the log: the item ID, what was done with video (`video`: `copy` or `h264`) and audio (`audio`: `copy`, `aac` or `none`), the number of completed segments, FFmpeg CPU time (`cpu`, user + system) and its peak resident memory (`peak_rss_kib`), separately from Couch's own usage.

PlaybackInfo reads parameters from the query and JSON, including JSON sent as `text/plain`. It takes into account `EnableDirectStream`, bitrate/channel limits, the source and the audio track (Emby Web in Chromium accepts a non-first track only via remux to HLS: its profile sets an `IsSecondaryAudio` condition); it rejects conflicting values, another user's UserId and invalid indexes. `StartTimeTicks` is validated, but the file is not trimmed: with direct delivery the client seeks via Range.

The profile is checked against DirectPlayProfiles, ContainerProfiles and CodecProfiles/ApplyConditions. Supported comparisons are Equals/NotEquals/EqualsAny/LessThanEqual/GreaterThanEqual and the main video/audio parameters: resolution, codec level/profile/tag, bitrate, frame rate, channels and sample rate. Missing required metadata is not considered compatible; optional conditions without metadata are skipped. A condition is required by default. A missing DeviceProfile keeps discovery mode; an explicitly empty one does not allow playback.

On incompatibility the response contains `ErrorCode=NoCompatibleStream` with no stream URL. A refusal (`NoCompatibleStream` or `RateLimitExceeded`) is logged as a `Playback refused` line with the item ID, error code, container, video and audio codec, and flags for the presence of a DeviceProfile (`profile`) and FFmpeg (`hls`), without paths or tokens. External subtitles next to the video (`Movie.srt`, `Movie.ru.srt`, `Movie.en.forced.srt`, `Movie.ru.sdh.srt`; also `.vtt`) are added during scanning as tracks after the embedded ones: the language is the first tag of 2–3 Latin letters, and `forced`, `sdh`/`hi`/`cc` and `default` set flags; letter case in the name does not matter; if a name matches two videos, the file goes to the video with the longer name, and videos with the same name (`Movie.mp4` and `Movie.mkv`) both get it. SRT with CR, CRLF or LF line endings is supported. PlaybackInfo returns them with `DeliveryMethod=External` and a WebVTT link (`/Videos/{id}/{source}/Subtitles/{index}/Stream.vtt`, the token in the link, as for the stream; the same file is served at `/Items/{id}/{source}/Subtitles/{index}/Stream.vtt`); SRT is converted to WebVTT on the fly. Only UTF-8 (with or without BOM) is supported: a file in another encoding (for example, Windows-1251) is not served, and `Subtitle unavailable` is written to the log. Up to 32 files per video, up to 10 MiB per file. Embedded text subtitles (SubRip, mov_text, WebVTT, ASS/SSA) are served at the same link: on request, FFmpeg extracts the track to WebVTT through a pipe, without temporary files. If the video goes through HLS and the client accepts WebVTT inside HLS (subtitle profile `{"Format":"vtt","Method":"Hls"}`, as in Emby Web), the tracks are served, as in Emby, with `DeliveryMethod=Hls` and no link: `TranscodingUrl` gets `SubtitleStreamIndexes` and `ManifestSubtitles=vtt`, `master.m3u8` lists the tracks in index order (`EXT-X-MEDIA:TYPE=SUBTITLES`), and `subtitles.m3u8` is a single WebVTT segment for the whole duration (Emby cuts 6-second pieces) with `X-TIMESTAMP-MAP`, which ties cue times to the video segment timestamps. The player turns such subtitles on, switches and turns them off without a new PlaybackInfo. At most two extractions run at once; the rest wait for a free slot until the client cancels the request; a job is limited to 2 minutes and 10 MiB of output and ends when the connection drops; an FFmpeg failure gives 404 and a `Subtitle unavailable` warning. Without FFmpeg in PATH, and for image-based subtitles (PGS, DVD, DVB), the selected track is skipped: the video plays as a direct stream without subtitles (`DefaultSubtitleStreamIndex=-1`), and `Subtitle not deliverable, playing without it` is written to the log; burn-in is not implemented. Extracted subtitles are not cached: the file is read in full on each playback with them. If the client has not chosen a track itself, Couch chooses one based on the user's subtitle mode (mode descriptions taken from Emby Web): "Default" — tracks with the forced or default flag (forced first), "Smart" — a track in the preferred language if the audio language differs, "Only forced", "Hearing impaired" — tracks in the preferred language, SDH first, "Always" — tracks in the preferred language, "None". Languages are compared using the `Localization/Cultures` list (`ru` = `rus`), and preference order is respected; only tracks Couch can serve are chosen. The rules follow the descriptions, not captured reference responses. The selected audio track is reflected in DefaultAudioStreamIndex. If the client profile accepts it in the source file (the track is first or the profile does not set an `IsSecondaryAudio` condition), the file is served as is and the client switches the track; otherwise the file is repackaged to HLS with only the selected track. Rotated video (rotation in container metadata) is not served via remux: MPEG-TS does not store rotation, so the video is encoded to H.264 already rotated; the `VideoRotation` profile condition is checked. HDR and the full set of Emby constraints are not covered yet.

The 30-second write timeout is renewed on every block: it limits a stalled write, not the duration of the whole transfer. The file is not loaded into memory in full. Cache validators come from the current size/mtime; the ETag is weak, not a content hash, so `If-Range` with an ETag results in a full response. Do not modify a file while it is being watched; catalog metadata is updated on restart or rescan.

When stopped at ≥90%, the video is marked played and the position is reset. A missing PositionTicks keeps the position; a negative one is rejected. For reports with a PlaySessionId, the last 64 playback starts per client session are kept: a repeated start does not increase the counter; progress/stop are accepted only for the latest unfinished playback. Within it, reports are applied in the order received, including seeking backward. Without a PlaySessionId, the previous received-order semantics remain, without deduplication. The window survives a restart; a repeat of a start already evicted from the window counts as new. A discarded report still gets 204, but a `Playback report ignored` warning is written to the log with the event type, reason and item ID; a stop without a position is noted with the message `Playback stopped without a position`. Image-based subtitles and full media compatibility are not implemented yet.

## Implemented contract

The API accepts routes with and without the `/emby/` prefix, case-insensitively. Query parameter values are kept unchanged.

| Area | Routes |
|---|---|
| Connection | `GET /System/Info/Public`, `GET /Users/Public`, branding, ping |
| Sign-in | `POST /Users/AuthenticateByName`: JSON or form-urlencoded |
| Session | `GET /System/Info`, `GET /Users/{id}`, `GET /Users/Me`, `GET /Sessions`, `POST /Sessions/Logout` |
| Client capabilities | `POST /Sessions/Capabilities/Full` |
| Settings | `GET/POST /usersettings/{id}`, `POST /usersettings/{id}/Partial`, user Configuration |
| Catalog | Views, Items, ItemTypes for search, Items/{id}, Latest, root, Shows/{id}/Seasons and Episodes, Shows/NextUp; filters on saved Played/Favorite; search history (`POST /Users/{id}/SearchedItems`, `POST /Users/{id}/RecentlySearched/Delete`, `WasSearched`, `SortBy=DateLastSearched`); Resume pagination |
| Artwork | GET/HEAD Items/{id}/Images/Primary[/0], signed ImageTags or an API token |
| Home screen | HomeSections, Sections/{id}/Items, Resume |
| User settings | `GET /Localization/Cultures` — the language list of the reference Emby 4.10.0.40 (196 entries, captured read-only on 2026-10-04) for choosing subtitle and audio languages |
| Playback | PlaybackInfo, Videos/{id}/stream.*, Videos/{id}/master.m3u8, main.m3u8 and subtitles.m3u8, Videos/ActiveEncodings, Sessions/Playing, /Progress, /Stopped |
| Watch state | POST/DELETE PlayedItems and FavoriteItems |
| WebSocket | `/embywebsocket`: token sign-in, KeepAlive, ping/pong, UserDataChanged, UserConfigurationUpdated, LibraryChanged after a rescan, ServerShuttingDown on shutdown |
| Empty results for now | Upcoming, Missing (Couch does not know about missing episodes: no online metadata), Live TV recordings; Items with `MinPremiereDate` or `IsUnaired=true` (no premiere dates) |

Tokens are supported in headers, in the `X-Emby-Token`/`api_key` query, and in `Authorization: Emby …` and `MediaBrowser …`. Conflicting tokens are rejected. JSON sent by the client as `text/plain` with `reqformat=json` is supported. Unknown routes return 404. Errors, as in the reference Emby, are returned as `text/plain` with a readable message that Emby Web shows in a dialog.

The API field `Version=4.10.0.40` selects the matching branch of the client protocol. The Couch version is `0.1.0`, visible in `/healthz`, `X-Couch-Version` and the authenticated `/System/Info`. A matching protocol number does not mean full compatibility with Emby.

## OpenAPI contract

[api/openapi.yaml](api/openapi.yaml) is the Couch contract. It was derived once from the OpenAPI document that Emby Server 4.10.0.40 itself publishes (`GET /emby/openapi.json`; the source version and SHA-256 are recorded in `info.x-couch-derived-from`), and has been maintained by hand since. It is a derivative work of the Emby specification. The contract contains all 526 Emby operations and the Couch-only `GET /healthz`.

Differences from the source:

- only JSON is kept, the `application/xml` variants are removed;
- the reference server address is removed from `servers`;
- corrections confirmed by fixtures, the client or acceptance testing are marked `x-couch-source`. Currently these are the public pre-sign-in operations (Emby marks them as protected) and the JSON body of `POST /UserSettings/{id}/Partial`.

Emby schemas have no `required`, so only types and enums are checked, not field presence. Every success without a body is documented as `200`. The table above describes what is actually implemented; what is not implemented yet can be seen in the list of expected e2e failures.

## Storage and limitations

- A single local user with no administrative privileges via the API. Re-initialization is rejected.
- Password: PBKDF2-HMAC-SHA256, 600,000 iterations, a random 16-byte salt. Only token hashes are kept in the state.
- Up to 64 sessions, 30-day lifetime; logout persists the revocation to disk. Sign-in is limited to 10 attempts per minute per address (IPv6: per /64 network), 100 attempts per minute per server and two concurrent password computations. `X-Forwarded-For` headers are ignored: behind a reverse proxy all clients share one address.
- State: `data/state.json`, schema v1, permissions 0600, atomic replace and fsync. Concurrent startup on the same directory is blocked. A corrupted file is not reset automatically.
- Request body size is up to 64 KiB; state up to 4 MiB. The JSON store is intended for M1, not for a large media catalog.
- Sessions, settings and watch state survive a graceful restart and a `SIGKILL` in the middle of a write (checked by `make e2e`); temporary files of interrupted writes are deleted on the next start. Recovery after a power loss, NAS file systems and migration from Emby have not been verified.
- Backup: stop Couch, copy the `state.json` and `user-image-*` files from the `-data` directory (in a container, from the `couch-data` volume), start Couch. `state.lock` and the `transcode` subdirectory are not needed. The copy contains password and token hashes: keep it as restricted as the directory itself. Restore: stop Couch, replace the directory files with the copy, start it. The state as of the copy comes back: the password, sessions (ones logged out later become valid again until they expire), marks and positions. The `make e2e` check restores a copy after `SIGKILL`.
- Data upgrades: schema v1 has only been extended since the first release (172bee0); a first-release file opens with the current one (`TestOpenFirstReleaseState`). Startup rejects a file with a newer schema without overwriting it. An older release drops fields it does not know on the first write, so a rollback requires a copy made before the upgrade.
- WebSocket sends UserDataChanged after "played", "favorite" and progress are saved, to all active connections of that user. After `Users/{id}/Configuration` is saved (subtitle mode, languages, etc.), the user's connections receive UserConfigurationUpdated with the user as each of them sees it: Emby Web replaces the cached user with it, and the setting takes effect in other tabs and on other devices without a reload. `UserSettings` are not part of the user and do not send the event. On a graceful shutdown (SIGTERM, Ctrl+C) each connection receives `ServerShuttingDown` and a 1001 close frame, all in parallel; a client that has stopped reading delays shutdown by no more than a couple of seconds. Each connection may have 32 pending events; on overflow it is closed without delaying the API or other clients. There is no log/redelivery: after reconnecting, state must be re-read via the API.
- Logout closes all WebSocket connections of its session without affecting others. Token validity is checked before an event, on incoming frames and every 20 seconds on ping; stopping Couch closes open connections.
- Other server events, remote control, administration, hardware-accelerated transcoding and a custom frontend are not implemented.

## Verification

```sh
go -C src test -race ./...
go -C src vet ./...
go -C src build -o ../bin/couch ./cmd/couch
node scripts/check-emby-client.mjs
COUCH_CHECK_MEDIA=1 node scripts/check-emby-client.mjs
COUCH_WEB_DIR=../emby-webui COUCH_CHECK_MEDIA=1 node scripts/check-emby-client.mjs
```

The scenario requires Node.js with `fetch` and a global `WebSocket`. It creates a temporary user and directory, starts/restarts Couch on a free loopback port and checks the exchange of the original API modules. With `COUCH_WEB_DIR` the modules are read from the local UI; without it, access to Emby is required. Production Emby data is not modified; test processes are stopped and temporary state is deleted.

The `COUCH_CHECK_MEDIA=1` variant requires FFmpeg and FFprobe: it generates a synthetic video and checks the catalog/metadata, skipping a broken file, a stable ID, PlaybackInfo negotiation by the original API client, stream bytes, HEAD/Range/416/401, position persistence after a restart and playback completion. The source is checked by SHA-256. Without the variable, the empty-library scenario runs.

Another instance/binary can be specified via `EMBY_REFERENCE` and `COUCH_BINARY`. The check expects web version 4.10.0.40; a version change requires revisiting the contract. The report prints module SHA-256 hashes and routes without tokens. Application events and browser storage are replaced with minimal adapters. In the media scenario, the original ApiClient opens a WebSocket, receives UserDataChanged and reconnects after a restart; closing on logout is checked. The DOM, player and connection manager are not checked.

The same commands are in the `Makefile`: `make` (lint, test, build), `make lint` (golangci-lint v2 per `.golangci.yml`, including go vet and the gofmt/goimports check; installed separately), `make vet`, `make build-linux` (Linux amd64, `CGO_ENABLED=0`), `make check` (build and the Node scenario; environment variables are passed as above), `make e2e`, `make e2e-report`, `make clean`. `make lint` also checks the `e2e/` module with the same `.golangci.yml`.

CI ([ci.yml](.github/workflows/ci.yml)) runs on push to `main` and on every pull request. Stages run sequentially, each one only after the previous one succeeds:

1. Build: golangci-lint v2.14 for both modules, `make build build-linux`.
2. Tests: `make test` and `make e2e` in `golang:1.25-alpine3.24` with FFmpeg 8.1, as in the image, as a non-root user. The HLS tests also pass with FFmpeg 6.1 and 7.1 (Alpine 3.21, Debian 13).
3. Container: image build and `scripts/check-container.sh` on linux/amd64 and linux/arm64.

Build and tests run on Go 1.25, the minimum version from `src/go.mod`; the image is built with the Go from the `Dockerfile`. The Node scenario (`make check`) does not run in CI.

### Contract E2E

```sh
make e2e
make e2e-report
```

`make e2e` builds Couch and generates a synthetic media library with FFmpeg: a movie with two audio tracks, an embedded and an external subtitle and a poster; a series with `Season 00`; audio and photos. It then starts instances on loopback and checks every operation in `api/openapi.yaml`:

- without a token, 401 is expected if the operation is protected;
- with a token, the documented success is expected: for operations with a JSON body, 200, `application/json` and schema conformance; for the rest, any 2xx.

GET and HEAD run in parallel on a shared instance; each mutation gets its own. Behavior checks in `e2e/behavior_test.go` cover scenarios for which the response shape is not enough: HLS, WebVTT, Next Up in Resume, persistence of a confirmed position after `SIGKILL` in the middle of playback reports, a 500 response without publishing the change when the data directory is not writable (do not run as root). Requires FFmpeg with libx264 and FFprobe. A run takes about 20 seconds.

The long-running load test runs separately: `cd e2e && COUCH_SOAK=10m go test -run TestSoak -timeout 30m ./...` (`COUCH_SOAK_WORKERS`, default 8). Each worker loops requesting the catalog, an item page, PlaybackInfo, the entire movie stream and a progress report; every fifth iteration it reconnects to WebSocket (alternately closing the connection with a close frame and dropping it), and every tenth iteration it requests embedded subtitles (FFmpeg). Workers 0 and 1 play the first remux-to-HLS segment every fourth iteration, worker 2 the first transcode segment, and they end the session via `ActiveEncodings/Delete`: together they occupy all FFmpeg slots. Throughout the load, the process is periodically sent `SIGHUP`, a rescan during playback. The test records RSS, the number of open files (`lsof`) and child processes (`pgrep`), and fails on request errors, leftover child processes, growth of open files by more than 8, and changes to the set of files in the data directory. Resource budgets have not been agreed yet (#15), so RSS is only printed. Without `COUCH_SOAK` the test is skipped.

`e2e/known_failures.go` is a map of expected failures: `operationId` or check key → issue and reason. It works like `xfail(strict=True)`:

- a failing test in the map is marked as skipped with the reason;
- a passing test in the map fails the run: the entry must be removed;
- a failing test not in the map fails the run as a regression.

TDD cycle: remove the entry → see the failure → implement → the test turns green. `make e2e-report` builds an Allure report (Allure CLI 3) in `e2e/allure-report`. The test title is the operation `summary`; labels: epic (Contract or Behavior), feature (Emby service), story (`operationId`); requests and responses are attached to steps; expected failures have a link to the issue.

In a restricted environment, Go may need an accessible `GOCACHE`, and network tests may need permission for loopback connections. Results of the current run: [VALIDATION.md](docs/VALIDATION.md).

## Documents

- [Requirements](docs/PRD.md)
- [Roadmap](docs/ROADMAP.md)
- [Emby/Jellyfin discovery](docs/DISCOVERY.md)
- [First stage plan](docs/IMPLEMENTATION.md)

The project license is GNU GPL v2 ([LICENSE](LICENSE)). Jellyfin code and Emby resources are not included in the server implementation. `api/openapi.yaml` is derived from the Emby Server 4.10.0.40 OpenAPI document (see "OpenAPI contract").
