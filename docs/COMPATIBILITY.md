# Compatibility matrix (#20)

Status: draft, updated 2026-10-07. Row and status rules: [ROADMAP](ROADMAP.md#compatibility-matrix-rules). Compatibility is claimed only for rows with the "verified" status. The media set is not agreed (#16): the rows below were verified on synthetic files and do not replace verification on a real library.

## Test bench

- Reference: Emby Server and Emby Web 4.10.0.40.
- Client: unmodified Emby Web 4.10.0.40 via `-web-dir`, Chromium 152 (built-in browser, macOS), hls.js, Russian UI.
- Server: Couch `main` 2026-10-07 with FFmpeg 9.0.2; segments in `<data>/transcode`.
- Media: `scripts/matrix-media.sh DIR` — 120 s of `testsrc2` 640×360, 25 fps, keyframes every 2 s; each frame shows its timestamp.

## Row scenario

1. **Start:** "Play" on the movie page. The delivery method is determined from the requests: `stream.<container>` — Direct Stream; `master.m3u8` without `VideoBitrate` — remux to HLS; with `VideoBitrate` — video transcode to H.264. Frames are rendered (`getVideoPlaybackQuality`).
2. **Seek** to 70 s: the timestamp in the frame matches `currentTime` to within one frame.
3. **Stop:** exit the player. `Sessions/Playing/Stopped` — 204, position saved; for HLS the segment directory is empty and no FFmpeg processes remain.
4. **Resume:** "Resume" starts from the saved position, the timestamp in the frame matches `currentTime`.

## Emby Web 4.10.0.40, Chromium 152, macOS

| File | Video / audio | Delivery | Start | Seek | Stop | Resume | Status |
|---|---|---|---|---|---|---|---|
| `MP4 H264 AAC.mp4` | H.264 / AAC | Direct Stream | yes | 1:12.920 at 72.95 s | 204; position not read | 1:21.720 at 81.73 s | verified 2026-10-07 |
| `MKV H264 two AAC.mkv` | H.264 / 2× AAC | Direct Stream (`stream.mkv`) | yes | 1:14.920 at 74.92 s | 74.73 s | 1:15.840 at 75.87 s | verified 2026-10-07 |
| `WebM VP9 Opus.webm` | VP9 / Opus | Direct Stream | yes | 1:14.880 at 74.88 s | 75.25 s | 1:15.840 at 75.86 s | verified 2026-10-07 |
| `MKV H264 AC3.mkv` + `.srt` | H.264 / AC3 | remux to HLS, AC3 → AAC | yes | 1:21.160 at 81.11 s | 81.08 s, cleaned up | 1:25.640 at 85.59 s | verified 2026-10-07 |
| `TS H264 AAC.ts` | H.264 / AAC | remux to HLS, AAC copied | yes | 1:14.880 at 74.93 s | 74.71 s | 1:15.600 at 75.65 s | verified 2026-10-07 |
| `AVI MPEG4 MP3.avi` | MPEG-4 Part 2 / MP3 | transcode to H.264, MP3 copied | yes | 1:16.000 at 75.92 s | 75.77 s, cleaned up | 1:16.520 at 76.45 s | verified 2026-10-07 |
| `MKV HEVC10 AC3.mkv` | HEVC 10-bit / AC3 | transcode to H.264, AC3 → AAC | yes | 1:15.960 at 75.91 s | 75.78 s, cleaned up | 1:16.480 at 76.45 s | verified 2026-10-07 |
| `MOV HEVC rotated.mov` (90° rotation, 20 s) | HEVC / AAC | transcode to H.264 with rotation | yes, 720×1280 | at 14 s, 720×1280 | cleaned up | not checked | partial 2026-10-11 (#22) |
| `MP4 H264 rotated.mp4` (90° rotation, 20 s) | H.264 / AAC | Direct Stream, browser applies rotation | yes, 720×1280 | not checked | not checked | not checked | partial 2026-10-11 (#22) |

The Couch log for the run has no errors or warnings.

### Series

| Scenario | Delivery | Result | Status |
|---|---|---|---|
| `Matrix Show` S01E01 → S01E02 (H.264 / AC3, 30 s each): play E01 from the page, seek to 24 s, auto-advance | remux to HLS for both episodes | E01 played to the end (`Stopped` at 29.9 s), marked as played, position reset; the client requested PlaybackInfo for E02 and started it (timestamp 0:03.320 at 3.37 s); after stopping E02, 12.99 s was saved; no segments or FFmpeg left | verified 2026-10-07 |

## Tracks and subtitles (Emby Web, Chromium)

Quality switching was verified 2026-10-07 on the set above, the other rows earlier on other synthetic files; details in [VALIDATION](VALIDATION.md).

| Scenario | Delivery | Status |
|---|---|---|
| Quality switch in the player (420 kbps on `MKV H264 two AAC.mkv`) | new PlaybackInfo with `MaxStreamingBitrate`, transcode (`VideoBitrate=228000`, `MaxWidth=854`); timestamp in the frame 0:31.630 at 31.8 s | verified 2026-10-07: after two switches in a row (420 and 320 kbps, both transcoded) reports under the new IDs were accepted, 68.51 s saved at 68.71 s (#73) |
| Selecting the second audio track in the player | new PlaybackInfo, remux to HLS with the selected track | verified 2026-10-06 ("Audio track switching in the browser"); 2026-10-07 on `MKV H264 two AAC.mkv`: after switching to track 2 reports under the new ID were accepted, 28.76 s saved at 28.99 s (#73) |
| External SRT selected on the item page | external WebVTT with Direct Stream; with remux, WebVTT inside HLS (`subtitles.m3u8`) | verified 2026-10-04 and 2026-10-06; inside HLS on 2026-10-11 on `MKV H264 AC3.mkv`: cues match the frame, turning off and on in the player without a new PlaybackInfo (#21) |
| Embedded SubRip in MKV | extraction to WebVTT | verified 2026-10-04 and 2026-10-06 |
| Automatic subtitle selection by user mode | `DefaultSubtitleStreamIndex` | verified 2026-10-04 |
| A track Couch cannot serve (`mov_text` without FFmpeg) | playback without it, no error | verified 2026-10-06 ("Undeliverable subtitles in the browser") |
| Image-based subtitles (PGS, VobSub) | burn-in not implemented | not investigated |

## Not investigated

- Android, Android TV (#26), iOS, Apple TV (#27).
- Other browsers and Emby Web on TVs (Tizen, webOS).
- Real library files (#16), long movies and CPU load during transcoding (#29).
- A browser whose profile limits H.264 to 1920 width: the limit was verified only with requests to Couch (VALIDATION, "Transcoding to H.264").
- HEVC with open GOPs in MPEG-TS with copied audio: a restart from the middle may begin later than planned (#81); with encoded audio this is fixed and covered by a unit test, not checked in the browser.
