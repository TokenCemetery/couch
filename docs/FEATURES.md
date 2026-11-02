# Supported features (#35)

Version: Couch 0.1.0, `main` at 81a45d0 (2026-11-01). Reference: Emby Server and Emby Web 4.10.0.40. Client: Emby Web in Chromium; the other mandatory clients (Android/Android TV, iOS/Apple TV) are not checked (#26, #27), so nothing here applies to them.

The list follows what a viewer does, not API routes. Statuses:

- **Supported:** the scenario works in Emby Web and is backed by a browser check, a matrix row or an automated test.
- **Partial:** it works with a stated limit.
- **Unsupported:** not implemented; Emby Web hides it, shows an empty list or reports an error.

Evidence lives in [COMPATIBILITY](COMPATIBILITY.md) (formats and delivery), [VALIDATION](VALIDATION.md) (checks) and the linked issues. Update this list in the same change as the behavior.

## Connection and account

| Feature | Status | Notes |
|---|---|---|
| Connect by address; stable ServerId across restarts | Supported | M1 acceptance, [ROADMAP](ROADMAP.md) M1 |
| Sign in, sign out, session restored after restart, revoked token refused | Supported | M1 acceptance; sign-in is rate limited |
| User accounts and user management | Partial | `-init` creates the administrator; the Users pages create, edit, disable, hide and delete users, set passwords and copy data (#111), and limit a user to some libraries (#116) |
| Emby Connect, Emby Premiere | Unsupported | The Premiere check to `mb3admin.com` is blocked by the UI's CSP; the client shows "Get Emby Premiere" (#89) |

## Browsing

| Feature | Status | Notes |
|---|---|---|
| Home screen: My Media, Continue Watching (with Next Up), Continue Listening, Latest | Supported | Row titles localized for ru, de, fr, es, it; English otherwise (#18); Continue Listening only with a music library (#104) |
| Movie and TV Shows libraries, item pages, seasons, episodes | Supported | Series need `Show/Season NN/Show.SNNENN.ext` naming; a movies library holds the other files |
| Search, recent searches | Supported | By title, original title or file name |
| Customizing home sections in Settings → Home Screen | Unsupported | Adding, removing or reordering a section shows "Couch's home screen sections are fixed for now" in Emby Web's error dialog (#100) |
| Sorting, paging, filters on technical data (container, codecs, audio layout, languages) | Supported | #78, #79 |
| Filters and sorts on metadata: genres, studios, tags, parental ratings, years, people; genre, studio and person pages | Supported | Values come from `.nfo` files and online providers; people cannot be marked favorite (#101, #80) |
| Next Up, Suggestions tab of TV Shows | Supported | Merge order with in-progress items is an assumption, not captured from Emby (#40) |
| Local images: Primary, Backdrop, Logo, Thumb; series images on seasons and episodes | Supported | #85, #86, #88 |
| Titles from file names, duration and technical data from FFprobe | Supported | Used when an item has no metadata |
| Metadata from `.nfo` files: title, overview, year, genres, studios, tags, ratings, people, provider IDs | Supported | Re-read when the file changes; checked in Emby Web (#101) |
| Metadata and poster/backdrop from TMDb and TheTVDB | Supported | On only with `COUCH_TMDB_API_KEY` / `COUCH_TVDB_API_KEY`; checked against the live services in English and Russian (#101) |
| Collections: create, add and remove items, rename, delete, play; Collections library and tabs, "Included In" row | Supported | Administrators edit them; other users see those with an item they can see. No collection images, no `.nfo` `<set>`, missing items always empty (#40) |
| Playlists: create, add (with or without duplicates), reorder, remove, rename, delete, play; Playlists library and menu section | Supported | Private to their user; movies, episodes and tracks, an album adds its tracks (#103, #104); no playlist images |
| Music libraries: albums, album artists, artists, songs, genres, folders; album and artist pages; Latest Music; favorite artists | Supported | Album, artists, track and disc numbers, year and genres from the file tags, else from `Artist/Album/NN Title` folders; local `cover.jpg` or `folder.jpg` album art, else the cover embedded in the files (#120). No composers, lyrics or online music metadata (#104) |
| Instant Mix | Unsupported | Emby Web's error dialog says "Instant Mix is not available in Couch yet." (#104) |
| Missing and upcoming episodes, recommendations | Unsupported | Empty lists (#34) |
| Photos, books, games, Live TV, DVR, channels | Unsupported | No such libraries; Live TV lists are empty (#34) |

## Watching

| Feature | Status | Notes |
|---|---|---|
| Direct Stream with seek (HTTP Range) | Supported | MP4, MKV, WebM with codecs the browser plays, including AV1; [COMPATIBILITY](COMPATIBILITY.md) |
| Remux to HLS (container or audio the browser cannot play) | Supported | Video copied, audio such as AC3 encoded to AAC; at most 2 sessions |
| Transcode to H.264 over HLS | Supported | MPEG-2, MPEG-4 ASP and HEVC sources verified; at most 1 session; threads follow a CPU quota by default, `-ffmpeg-threads` overrides (#92) |
| 4K HEVC transcode | Partial | Encoded at the source size; on 4 CPUs it starts, but encodes at about 0.75× real time, so playback stalls and a resume can exceed Emby Web's 10 s wait (#97) |
| HDR sources | Partial | Transcoded without tone mapping and reported as SDR (#22) |
| Interlaced sources | Partial | Transcoded without deinterlacing |
| Rotated video | Partial | Rotation applied; seek checked once on a transcode, resume not checked (#22) |
| Quality switch in the player | Supported | Keeps the position |
| Saved position, resume, played and favorite marks, remove from Continue Watching | Supported | Survive restart and forced stop |
| Auto-advance to the next episode, play a whole series | Supported | |
| Music playback: tracks, albums, playlists, with seek and saved position | Supported | MP3, FLAC, AAC in M4A, Ogg Vorbis, Opus and WAV play as they are; WMA, ALAC and AIFF are encoded to AAC in M4A (192 kbit/s, at most stereo) once per track, at most 2 at a time (#104) |
| Hardware-accelerated transcoding | Supported on the target | `-hwaccel vaapi` on the target AMD GPU (radeonsi), with the image built with `VAAPI=1`: every transcode file of the agreed set passes; `qsv`, `nvenc`, `amf` unverified on hardware; decoding stays on the CPU (#28) |
| Casting, play on another device, remote control | Unsupported | Casting not checked; session control routes not implemented (#34) |
| Offline sync and downloads to the client | Unsupported | #34 |

## Tracks and subtitles

| Feature | Status | Notes |
|---|---|---|
| Audio track selection | Supported | A non-first track goes through HLS remux |
| External SRT, WebVTT, ASS/SSA; embedded SubRip, `mov_text`, WebVTT, ASS/SSA | Supported | WebVTT external or inside HLS; subtitle files must be UTF-8 |
| Default subtitle by the user's subtitle mode and language | Supported | Rules follow Emby Web's mode descriptions |
| Image subtitles (PGS, DVD, DVB) | Supported | Burned into an H.264 transcode; turning them off returns to Direct Stream or remux |
| Subtitle files in legacy encodings | Unsupported | By decision: UTF-8 only (#24) |
| Subtitle search and download, subtitle editing and deletion | Unsupported | Hidden: Emby Web offers them only when the user policy enables subtitle downloading or management, and Couch's does not (#34) |

## Library and server

| Feature | Status | Notes |
|---|---|---|
| Libraries managed in Emby Web: create, rename, delete, add and remove folders below `-media-root` | Supported | Movies, TV shows and music (#104); library options are accepted but not stored; `-media-dir` creates the first two libraries once (#112) |
| Rescan from the library page, the "Scan media library" task, `SIGHUP` or `-rescan-interval`; open screens refresh (`LibraryChanged`) | Supported | IDs of unchanged files kept; a folder that cannot be read or comes back empty keeps its videos, and the others are updated (#39, #112) |
| Catalog kept across restarts | Supported | Saved to `catalog.json`; a start serves it at once and rescans in the background, probing only changed files; date added is kept (#39) |
| Library unreachable at startup | Partial | With a saved index, an empty volume keeps the saved catalog; on a first start without one, an unreadable folder stays empty and is reported until a rescan reaches it (#31, #39, #112) |
| Backup and restore of user state | Partial | By copying the data directory; the Emby backup API is not implemented (#32) |
| Server administration in the UI (dashboard, libraries, users, tasks, activity, logs, devices) | Supported | Users (#111) and libraries (#112) are managed; the dashboard shows sessions and activity, tasks run on request, logs are kept in `-data/logs`, devices can be signed out; only the server name is a setting (#113); Refresh Metadata, Identify and Remove Identification work, and the Metadata Manager shows items without saving them (#115) |
| Container image (Linux amd64, FFmpeg 8.1) | Supported | `Dockerfile` and `deploy/compose.yaml`, checked in CI; install, upgrade, forced stop, restore and VA-API verified on the target host (#114); reboot and a sign-in from another LAN host not checked (#117) |
| Native install as a systemd service | Partial | `make release`, `scripts/install.sh`, `deploy/couch.service`, [INSTALL](INSTALL.md); not yet run on the target host (#117) |
