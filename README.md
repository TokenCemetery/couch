# Couch

<img src="docs/logo.svg" alt="Couch logo" width="132">

[![CI](https://github.com/TokenCemetery/couch/actions/workflows/ci.yml/badge.svg)](https://github.com/TokenCemetery/couch/actions/workflows/ci.yml)
[![License: GPL v2](https://img.shields.io/badge/license-GPL--2.0-blue.svg)](LICENSE)

A lightweight media server in Go that speaks the Emby API, so the unmodified Emby Web client works against it.

Couch serves movies, TV series, music, home videos and photos from read-only folders: browsing, search, playback with saved position, remux and transcoding to HLS, subtitles, metadata and user management. It idles at about 11 MiB RSS, against about 200 MiB for Emby on the same hardware ([measurements](docs/BASELINE.md)).

> **Status:** prototype. The reference is Emby Server and Emby Web **4.10.0.40**. Only Emby Web in Chromium is verified; Android and iOS clients are not yet ([what works](docs/FEATURES.md)).

## Features

- **Libraries:** movies, TV shows, music, home videos & photos, managed from Emby Web; rescans keep IDs and watch state; real-time monitoring on Linux.
- **Playback:** Direct Stream with seeking; remux to HLS when only the container or audio is unsupported; transcoding to H.264 on the CPU or GPU (VA-API verified); HDR tone mapping.
- **Subtitles:** external SRT/WebVTT, embedded text subtitles as WebVTT, image subtitles burned in.
- **Metadata:** `.nfo` files, plus TMDb, TheTVDB and TheAudioDB when you provide API keys; local artwork.
- **Users:** accounts, per-user watch state, library access, collections and playlists.
- **Operations:** single static binary, SQLite state, online backup, crash-safe writes, bounded resource use.

Not supported: Live TV/DVR, DLNA, casting and remote control, offline sync, plugins.

## Quick start (Docker Compose)

Couch does not ship Emby Web. You need a copy of Emby Web 4.10.0.40 (the folder that contains `index.html`), or a running Emby to fetch it from.

```sh
git clone https://github.com/TokenCemetery/couch.git
cd couch/deploy
cp .env.example .env    # set COUCH_MEDIA and COUCH_WEB
docker compose build
```

Create the administrator. The password is read from stdin and must be at least 12 bytes:

```sh
read -rsp 'Password: ' pw; printf '\n'
printf '%s\n' "$pw" | docker compose run --rm -T couch -init -username admin
unset pw
docker compose up -d
```

Open `http://HOST:8097/` and sign in. The media folder becomes the Movies and TV Shows libraries; add more libraries in **Manage Emby Server → Library**.

To run Couch as a systemd service instead, see [docs/INSTALL.md](docs/INSTALL.md).

## Build from source

Requires Go 1.26+ on Linux or macOS. FFprobe is needed to scan libraries, and FFmpeg for remux, transcoding and embedded subtitles.

```sh
make build
read -rsp 'Password: ' pw; printf '\n'
printf '%s\n' "$pw" | ./bin/couch -init -username admin
unset pw
./bin/couch -media-dir /srv/media -web-dir /opt/emby-webui
```

Couch listens on `127.0.0.1:8097` by default. Use `-listen :8097` to serve the LAN.

## Configuration

| Flag | Default | Description |
|---|---|---|
| `-listen` | `127.0.0.1:8097` | HTTP listen address |
| `-data` | `./data` | State directory (database, logs, caches) |
| `-name` | `Couch` | Server name; can be changed later in Emby Web |
| `-web-dir` | | Local Emby Web folder containing `index.html` |
| `-web-upstream` | | Emby origin to fetch `/web/` files from instead, e.g. `http://emby:8096` |
| `-media-dir` | | Folder whose Movies and TV Shows libraries are created on first start |
| `-media-root` | `-media-dir` | Folder below which libraries may be added in Emby Web; repeatable |
| `-rescan-interval` | `0` (off) | Periodic rescan, e.g. `30m` |
| `-metadata-language` | `en` | `en`, `ru`, `de`, `fr`, `es` or `it` |
| `-metadata-refresh` | `0` (never) | Fetch online metadata again once it is this old, e.g. `720h` |
| `-ffmpeg-threads` | `0` | Thread cap per transcode; `0` follows the container's CPU quota |
| `-hwaccel` | | GPU encoder: `vaapi`, `qsv`, `nvenc` or `amf` |
| `-hwaccel-device` | `/dev/dri/renderD128` | Render node for `vaapi` and `qsv` |
| `-tonemap` | `auto` | HDR to SDR: `auto` (libplacebo on Vulkan), `software` (zscale), `off` |
| `-init -username NAME` | | Create the administrator (password on stdin) and exit |
| `-backup DIR` | | Write a consistent copy of the state and exit; safe while running |

Online metadata is enabled per provider by setting its key in the environment: `COUCH_TMDB_API_KEY`, `COUCH_TVDB_API_KEY`, `COUCH_AUDIODB_API_KEY`. Without a key, Couch sends that provider nothing.

`SIGHUP` triggers a rescan; `SIGTERM` shuts down gracefully.

### Hardware transcoding

`-hwaccel vaapi` is verified on an AMD Radeon 780M; `qsv`, `nvenc` and `amf` have not been tested on real hardware. Decoding, scaling and subtitle burn-in stay on the CPU. Couch encodes a test pattern at startup and refuses to start if the encoder fails.

With Compose, uncomment the VA-API lines in `.env`. They build the image with Mesa's VA-API and Vulkan drivers (`VAAPI=1`) and pass the render node in.

## Library layout

Couch never writes to the media folders.

**Movies:** any video file. `Movie (2010)/Movie (2010).mkv` works best for metadata lookups.

**TV shows:** `Show/Season 01/Show.S01E02.mkv`; the season number in the folder and the file name must match. `Season 00` holds specials. Files that do not match stay movies.

**Music:** tags are read first; otherwise `Artist/Album/03 - Title.mp3`. Album art comes from `cover.jpg` or `folder.jpg`, else from the cover embedded in the files. Lyrics come from `.lrc` or `.txt` files named like the track.

**Home videos & photos:** every folder is an album. Supported: jpg, png, gif, webp, bmp; JPEG EXIF date and orientation are read.

**Artwork** (`.jpg`, `.jpeg`, `.png` next to the item):

| Image | Per item | Per folder |
|---|---|---|
| Poster | `<name>-poster`, `<name>` | `poster`, `folder`, `cover` |
| Backdrop | `<name>-fanart`, `<name>-backdrop` | `backdrop`, `fanart` |
| Logo | `<name>-clearlogo`, `<name>-logo` | `clearlogo`, `logo` |
| Thumb | `<name>-landscape` | `landscape`, `thumb` |

Seasons and episodes fall back to the series' images.

**Metadata files:** `<name>.nfo` or `movie.nfo`, `tvshow.nfo`, `season.nfo` (Kodi/Emby format). A `.nfo` value wins over online providers.

**Subtitles:** `Movie.srt`, `Movie.ru.srt`, `Movie.en.forced.srt`, `Movie.en.sdh.vtt`. They must be UTF-8.

**Formats:** videos mp4, m4v, mkv, webm, avi, mov, mpg, mpeg, ts, m2ts, wmv, ogv; audio mp3, flac, m4a, aac, ogg, oga, opus, wav, wma, aiff, aif.

**Limits:** 10,000 videos, 10,000 tracks and 50,000 photos across all libraries. Remux sessions are capped at 2 and transcodes at 1; further requests get `503 Retry-After`.

## Operations

- **Logs:** standard error, also `logs/couch.log` in the data directory (rotated at 10 MiB) and the dashboard's logs page. Logs contain no tokens, passwords or request URLs.
- **Backup:** `couch -data DIR -backup NEW_DIR` (in Compose: `docker compose exec couch couch -data /data -backup /data/backup`). The copy includes password and token hashes, so keep it private. To restore, stop Couch, replace `couch.db` and `user-image-*` with the backup's files, then start Couch.
- **Upgrade:** back up, then rebuild and restart. The database is upgraded on first start; a database from a newer version is refused and left unchanged.
- **Security:** plain HTTP for the LAN, with no TLS. `X-Forwarded-For` is ignored, so behind a reverse proxy all clients share one sign-in rate limit. Files under `-web-dir` are served without sign-in. Image URLs carry signed, per-session tags; treat them as access keys. Emby Web's Emby Premiere registration check is blocked by a CSP header.

## Development

```sh
make            # lint, test, build
make test       # go test -race
make e2e        # contract and behaviour tests against api/openapi.yaml (needs FFmpeg with libx264)
make e2e-report # Allure report in e2e/allure-report
make check      # Emby Web's own API modules against Couch (Node.js; COUCH_WEB_DIR=../emby-webui, else fetched from EMBY_REFERENCE)
make release    # static Linux amd64 binary with its SHA-256
```

`make lint` needs [golangci-lint](https://golangci-lint.run/) v2. For a load test, run `cd e2e && COUCH_SOAK=10m go test -run TestSoak -timeout 30m ./...`.

`e2e/known_failures.go` lists the API operations that are expected to fail, each with its issue. A listed operation that starts passing fails the run, so remove its entry when you implement it.

| Path | Contents |
|---|---|
| `src/cmd/couch` | Entry point and flags |
| `src/internal/server` | HTTP API, WebSocket, Emby DTOs |
| `src/internal/media` | Scanning, FFprobe, catalog |
| `src/internal/hls` | Remux and transcoding jobs |
| `src/internal/library` | Library management, rescans, monitoring |
| `src/internal/metadata` | `.nfo` and online providers |
| `src/internal/state` | SQLite state |
| `api/openapi.yaml` | API contract, derived from Emby 4.10.0.40's OpenAPI document |
| `e2e/` | Contract, behaviour and soak tests |
| `deploy/` | Compose files and systemd unit |

Contributions are tracked in [GitHub issues](https://github.com/TokenCemetery/couch/issues); see [AGENTS.md](AGENTS.md) for the conventions.

## Documentation

- [Supported features](docs/FEATURES.md)
- [Compatibility matrix](docs/COMPATIBILITY.md)
- [Validation](docs/VALIDATION.md)
- [Resource baseline](docs/BASELINE.md)
- [Install as a systemd service](docs/INSTALL.md)
- [Requirements](docs/PRD.md) and [roadmap](docs/ROADMAP.md)

## License

GNU GPL v2, see [LICENSE](LICENSE). Couch includes no Jellyfin code and no Emby resources. `api/openapi.yaml` is derived from the OpenAPI document published by Emby Server 4.10.0.40.
