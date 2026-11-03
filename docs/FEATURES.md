# Supported features (#35)

Reference: Emby Server and Emby Web 4.10.0.40. Client: Emby Web in Chromium only; Android/Android TV and iOS/Apple TV are unchecked (#26, #27). Update this list in the same change as the behaviour.

- **Supported:** works in Emby Web, backed by a browser check, matrix row or automated test ([VALIDATION](VALIDATION.md), [COMPATIBILITY](COMPATIBILITY.md)).
- **Partial:** works with a stated limit.
- **Unsupported:** not implemented; Emby Web hides it, shows an empty list or reports an error.

## Connection and account

| Feature | Status | Notes |
|---|---|---|
| Connect by address; stable ServerId | Supported | |
| Sign in, sign out, session survives restart, revoked token refused | Supported | Sign-in is rate limited |
| User accounts and management | Partial | `-init` creates the administrator; Users pages create, edit, disable, hide, delete, set passwords, copy data (#111), restrict libraries (#116) |
| Emby Connect, Emby Premiere | Unsupported | Premiere check blocked by CSP; "Get Emby Premiere" stays (#89) |

## Browsing

| Feature | Status | Notes |
|---|---|---|
| Home: My Media, Continue Watching (with Next Up), Continue Listening, Latest | Supported | Titles localized for ru, de, fr, es, it (#18) |
| Home Screen settings: add, edit, remove, reorder sections | Supported | Stored per user; Live TV sections refused; an administrator cannot edit another user's (#100) |
| Movie and TV Shows libraries, item pages, seasons, episodes | Supported | Series need `Show/Season NN/Show.SNNENN.ext` |
| Search, recent searches | Supported | Title, original title or file name |
| Sorting, paging, A–Z bar, technical filters (container, codecs, layout, languages) | Supported | #78, #79, #124 |
| Metadata filters and pages: genres, studios, tags, ratings, years, people | Supported | From `.nfo` and providers; people cannot be favorites; credits list only visible items (#80, #101, #123, #131) |
| Next Up, TV Suggestions tab | Supported | Merge order with in-progress items assumed (#40) |
| Local images: Primary, Backdrop, Logo, Thumb; series images on seasons and episodes | Supported | #85, #86, #88 |
| Titles from file names; duration and technical data from FFprobe | Supported | Used when no metadata exists |
| `.nfo` metadata | Supported | Re-read when the file changes (#101) |
| TMDb and TheTVDB metadata and images | Supported | Only with `COUCH_TMDB_API_KEY` / `COUCH_TVDB_API_KEY`; seasons (#132) and TMDb people checked against recorded answers only |
| Collections | Supported | Created, edited, played; auto-created from `.nfo` `<set>` or TMDb collections (#133); collage or uploaded image (#134); administrators edit |
| Playlists | Supported | Private per user; videos, tracks, photos; reorder, duplicates; collage or uploaded image (#103, #134) |
| Music libraries: albums, artists, songs, genres, folders, favorite artists | Supported | Tags, else `Artist/Album/NN Title`; folder or embedded art (#120); TheAudioDB with `COUCH_AUDIODB_API_KEY` (#136); no composers (#104) |
| Home videos & photos: tabs, albums, slideshow | Supported | jpg, png, gif, webp, bmp sent as originals; EXIF date and orientation; no HEIC, RAW or thumbnails; "Enable photos" not stored (#105) |
| Lyrics from `.lrc`, `.txt` and tags | Partial | Served; Emby Web's view needs Premiere (#135, #89) |
| Instant Mix | Supported | Seed tracks, then shared artist or genre; at most 200 (#119) |
| Missing and upcoming episodes, recommendations | Unsupported | Empty lists (#34) |
| Books, games, Live TV, DVR, channels | Unsupported | #34 |

## Watching

| Feature | Status | Notes |
|---|---|---|
| Direct Stream with seek | Supported | MP4, MKV, WebM with browser codecs, including AV1 |
| Remux to HLS | Supported | Video copied, audio such as AC3 to AAC; at most 2 sessions |
| Transcode to H.264 over HLS | Supported | MPEG-2, MPEG-4 ASP, HEVC verified; 1 session; threads follow the CPU quota, `-ffmpeg-threads` overrides (#92) |
| GPU transcoding | Supported on the target | `-hwaccel vaapi` on AMD with the `VAAPI=1` image; `qsv`, `nvenc`, `amf` unverified; decoding on CPU (#28) |
| 4K HEVC transcode | Partial | Real time with VA-API; `libx264` on 4 CPUs runs at 0.75× and stalls (#97) |
| HDR sources | Partial | Reported as Emby does (HLG values assumed). Tone-mapped to SDR with libplacebo where Vulkan works, or `zscale` with `-tonemap software` (0.3× on 4K); otherwise HDR tags kept (#125) |
| Interlaced sources | Partial | No deinterlacing |
| Rotated video | Partial | Rotation applied; resume after a transcode seek not checked (#22) |
| Quality switch | Supported | Position kept |
| Position, resume, played, favorite, remove from Continue Watching | Supported | Survive restart and forced stop |
| Auto-advance, play a whole series | Supported | |
| Music playback with seek and saved position | Supported | MP3, FLAC, AAC, Vorbis, Opus, WAV as is; WMA, ALAC, AIFF encoded once to AAC, 2 at a time (#104) |
| Casting, remote control | Unsupported | #34 |
| Offline sync and downloads | Unsupported | #34 |

## Tracks and subtitles

| Feature | Status | Notes |
|---|---|---|
| Audio track selection | Supported | A non-first track goes through remux |
| External SRT, WebVTT, ASS/SSA; embedded SubRip, `mov_text`, WebVTT, ASS/SSA | Supported | WebVTT external or inside HLS; files must be UTF-8 |
| Default subtitle by user mode and language | Supported | |
| Image subtitles (PGS, DVD, DVB) | Supported | Burned into an H.264 transcode |
| Subtitle files in legacy encodings | Unsupported | UTF-8 only by decision (#24) |
| Subtitle search, download, editing | Unsupported | Hidden by the user policy (#34) |

## Library and server

| Feature | Status | Notes |
|---|---|---|
| Libraries managed in Emby Web below `-media-root` | Supported | Movies, TV, music, home videos & photos; options other than real-time monitoring not stored; `-media-dir` creates the first two (#112) |
| Rescan: library page, scheduled task, `SIGHUP`, `-rescan-interval`; open screens refresh | Supported | Unchanged IDs kept; a folder that fails or comes back empty keeps its items (#39, #112) |
| Real-time monitoring | Partial | Linux inotify, rescan 10 s after changes stop; not macOS, not changes made by other hosts on network mounts (#130) |
| Catalog kept across restarts | Supported | In `couch.db`; served at once, rescanned in the background; date added kept (#39, #128) |
| Library unreachable at startup | Partial | Saved catalog kept; on a first start the folder stays empty until a rescan reaches it (#31, #39) |
| Backup and restore | Partial | `-backup` or copying the data directory; no Emby backup API (#32) |
| Administration: dashboard, users, libraries, tasks, activity, logs, devices, metadata refresh and identify | Supported | Only the server name is a setting (#113, #115) |
| Database settings page | Supported | Vacuum on start, optimize on stop (#129) |
| Container image (Linux amd64, FFmpeg 8.1) | Supported | Verified on the target host; reboot and LAN sign-in unchecked (#114, #117) |
| Native systemd install | Partial | [INSTALL](INSTALL.md); not yet run on the target host (#117) |
