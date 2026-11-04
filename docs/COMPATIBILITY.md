# Compatibility matrix (#20)

Row rules: [ROADMAP](ROADMAP.md#compatibility-matrix-rules). Compatibility is claimed only for "verified" rows. Client everywhere: unmodified Emby Web 4.10.0.40 via `-web-dir` in Chromium 152.

## Row scenario

1. **Start:** "Play" on the item page; frames render. Delivery is read from the requests: `stream.<container>` is Direct Stream; `master.m3u8` without `VideoBitrate` is remux to HLS; with it, transcode to H.264.
2. **Seek** (to 70 s, or the middle of a real file): the frame timestamp matches `currentTime` to within a frame, or playback advances from the target.
3. **Stop:** `Sessions/Playing/Stopped` 204, position saved; for HLS no segments or FFmpeg processes remain.
4. **Resume** from the saved position.

## Synthetic set

`scripts/matrix-media.sh DIR`: 120 s `testsrc2` 640×360, 25 fps, keyframes every 2 s, timestamp drawn in each frame. Rotated files: 20 s 1280×720 with a 90° display matrix.

|File|Video / audio|Delivery|Status|
|---|---|---|---|
|`MP4 H264 AAC.mp4`|H.264 / AAC|Direct Stream|verified 2026-10-07|
|`MKV H264 two AAC.mkv`|H.264 / 2× AAC|Direct Stream|verified 2026-10-07|
|`WebM VP9 Opus.webm`|VP9 / Opus|Direct Stream|verified 2026-10-07|
|`MKV H264 AC3.mkv` + `.srt`|H.264 / AC3|remux, AC3 → AAC|verified 2026-10-07|
|`TS H264 AAC.ts`|H.264 / AAC|remux, AAC copied|verified 2026-10-07|
|`AVI MPEG4 MP3.avi`|MPEG-4 Part 2 / MP3|transcode, MP3 copied|verified 2026-10-07|
|`MKV HEVC10 AC3.mkv`|HEVC 10-bit / AC3|transcode, AC3 → AAC|verified 2026-10-07|
|`MOV HEVC rotated.mov`|HEVC / AAC, 90°|transcode with rotation, 720×1280|verified 2026-11-01 (#22)|
|`MP4 H264 rotated.mp4`|H.264 / AAC, 90°|Direct Stream, browser rotates|verified 2026-11-01 (#22)|
|`Matrix Show` S01E01 → E02|H.264 / AC3|remux, auto-advance to E02|verified 2026-10-07|

## Real library files (#16)

From the owner's library (private; named by ID only): for each format the synthetic set misses, the shortest file over a minute from an FFprobe pass over 11,819 videos, plus two consecutive episodes. Couch release image, no CPU limit, subtitles off.

|ID|Container, video / audio, subtitles|Delivery|Status|
|---|---|---|---|
|R01|AVI, MPEG-4 ASP 400p / AC3 2.0, keyframes without PTS|transcode, AC3 → AAC|verified 2026-10-31 (#96)|
|R02|AVI, MPEG-4 ASP 400p / MP3 2.0|transcode, MP3 copied|verified 2026-10-31|
|R03|MKV, H.264 High 1080p / AC3 5.1 ×2, E-AC3 5.1 ×2, SubRip ×2|remux; SubRip inside HLS|verified 2026-10-31|
|R04|MP4, H.264 High 576p / AAC 2.0, AC3 2.0, `mov_text` ×3|Direct Stream; `mov_text` as WebVTT|verified 2026-10-31|
|R05|MKV, MPEG-2 576i / AC3 2.0, DVD subtitles ×12|transcode, no deinterlacing|verified 2026-10-31|
|R06|MP4, HEVC Main 720p / AC3 5.1, E-AC3 5.1, `mov_text`|transcode; `mov_text` inside HLS|verified 2026-10-31|
|R07|MP4, H.264 High 576p / AC3 1.0, AC3 2.0, DVD subtitles|remux; transcode with the DVD subtitle burned in when it is selected|verified 2026-10-31; burn-in 2026-11-01|
|R08|MP4, AV1 Main 10-bit 720p / AAC 2.0|Direct Stream|verified 2026-10-31|
|R09|MKV, HEVC Main 10 HDR10 3840×2076 / AC3 5.1 ×2, SubRip ×3|transcode at source size|`libx264` partial: 0.75× real time on 4 CPUs, stalls (#97); VA-API verified, below|
|R10|MKV, MPEG-2 576p / AC3 5.1, DTS 5.1|transcode, AC3 → AAC|verified 2026-10-31|
|R11|MKV, HEVC Main 10 1920×816 / AC3 5.1 ×2, PGS|transcode|verified 2026-10-31|
|R12|MKV, H.264 High 576p / AC3 ×3, ASS, SubRip|remux; subtitles inside HLS|verified 2026-10-31|
|R13|MKV, H.264 High 10 720p / AC3 5.1, AC3 2.0|remux, 10-bit H.264 copied|verified 2026-10-31|
|R14|MKV, H.264 High 720p / AC3 2.0 ×2, FLAC 2.0, SubRip|remux of the default AC3 track|verified 2026-10-31|
|R15|MP4, HEVC Main 720p / AC3 ×4, `mov_text` ×3|transcode; `mov_text` inside HLS|verified 2026-10-31|
|R16|AVI, MPEG-2 576i / MP2 2.0|transcode, MP2 → AAC|verified 2026-10-31|
|S01E01, S01E02|MKV, H.264 High 576p / AC3 ×3, ASS, SubRip|remux; subtitles inside HLS|verified 2026-10-31|

Not checked on these files: frame-accurate seek (the synthetic set covers it), track switching, auto-advance.

### GPU transcoding (VA-API, #28)

Every transcode row above (R01, R02, R05, R06, R07 with DVD burn-in, R09, R10, R11, R15, R16) with `-hwaccel vaapi` on the Radeon 780M: verified 2026-11-01. First frame in 0.8–1.3 s (2.3 s for R09), R09 in real time with no dropped frames, every job logged `encoder=h264_vaapi`, none left running.

### HDR tone mapping (#125)

VA-API image with Mesa's Vulkan driver, default `-tonemap auto` (libplacebo):

|Source|Result|Status|
|---|---|---|
|R09, HDR10 3840×2076|H.264 BT.709 with SDR colours; plays in real time; seek, stop, resume|verified 2026-11-03|
|Synthetic HLG 1920×1080|SDR colours; start and stop only|verified 2026-11-03|

`-tonemap software` is covered by `TestToneMapTranscode` only; at 0.3× real time on 4K it would stall.

## Music (#104)

Synthetic tagged tracks. Emby Web requests `Audio/{id}/universal` with `TranscodingProtocol=hls`.

|Codec|Delivery|Status|
|---|---|---|
|MP3, FLAC, Vorbis, Opus, AAC (M4A), PCM WAV|the file (206)|verified 2026-11-02|
|ALAC, WMA v2, AIFF|encoded once to AAC in M4A; seekable|verified 2026-11-02|

Not checked: real music files, gapless playback, multichannel sources.

## Photos (#105)

Synthetic pictures; Couch sends the original file for every size Emby Web asks for.

|Format|Status|
|---|---|
|JPEG with EXIF orientation and date, JPEG without EXIF, PNG, WebP, animated GIF, BMP|verified 2026-11-03: card and slideshow, upright|
|H.264/AAC MP4 in the same library|verified 2026-11-03: Direct Stream|

Not checked: real camera photos, HEIC, RAW, large panoramas.

## Tracks and subtitles

|Scenario|Delivery|Status|
|---|---|---|
|Quality switch in the player|new PlaybackInfo, transcode at the new bitrate; position kept|verified 2026-10-07 (#73)|
|Second audio track|new PlaybackInfo, remux with the selected track copied|verified 2026-10-07 (#73)|
|External SRT|WebVTT beside Direct Stream; inside HLS with remux, toggled without a new PlaybackInfo|verified 2026-10-11 (#21)|
|Embedded SubRip in MKV|extracted to WebVTT|verified 2026-10-06|
|Automatic selection by user subtitle mode|`DefaultSubtitleStreamIndex`|verified 2026-10-04|
|Track Couch cannot serve (`mov_text` without FFmpeg)|plays without it, no error|verified 2026-10-06|
|Image subtitles (PGS, DVD)|H.264 transcode with the track burned in|verified 2026-11-01 on R11 and R07 (#24)|

## Database settings (#129)

Compared with a local `emby/embyserver:4.10.0.40` on 2026-11-03.

|Setting|Emby|Couch|
|---|---|---|
|Page and save|Manage Emby Server → Database; `POST /System/Configuration/Partial`|same; 204|
|`VacuumDatabaseOnStartup`|off; vacuums each database at next start, then reads `false`|same for `couch.db`, before HTTP starts; logs `Database vacuumed`|
|`OptimizeDatabaseOnShutdown`|on; `pragma optimize` on stop|same; logs `Database optimized`|
|`DatabaseAnalysisLimit`|400|400, applied as `PRAGMA analysis_limit`; changes refused|
|`DatabaseCacheSizeMB`|128|not reported (SQLite's 2 MB default, below the page minimum); values refused|
|Partial without a body|500|400|

## Not investigated

- Android, Android TV (#26), iOS, Apple TV (#27), other browsers, Emby Web on TVs (Tizen, webOS).
- A browser whose profile caps H.264 width: checked only by requests to Couch.
- HEVC with open GOPs in MPEG-TS with copied audio: a mid-file restart may begin late (#81).
