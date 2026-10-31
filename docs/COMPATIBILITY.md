# Compatibility matrix (#20)

Status: draft, updated 2026-10-31. Row and status rules: [ROADMAP](ROADMAP.md#compatibility-matrix-rules). Compatibility is claimed only for rows with the "verified" status. The media set is agreed (#16): the synthetic files below are the baseline, and the real library files in "Real library files" cover the formats the synthetic set does not.

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

## Real library files (#16), Emby Web 4.10.0.40, Chromium, Linux

The files come from the owner's library and stay private: they are named here only by ID and technical profile. The set was chosen from an FFprobe pass over 11 819 library videos. For each format the synthetic set does not cover, it takes the shortest file over one minute, plus two consecutive episodes. The copies are byte-identical to the originals.

Bench: Couch `main` at c138f56 in the release image (FFmpeg 8.1), Linux amd64, AMD Ryzen 7 255 (16 threads), no CPU limit. Emby Web via `-web-dir` in the built-in Chromium. The device profile is the one Emby Web sent.

The row scenario is scripted in the page: "Play" on the item page, frames after 10 s; seek to the middle by setting the video position, frames advancing 12 s later; exit the player and read the saved position from the server; "Resume" and check the position 10 s later. The saved position is cleared before each file.

| ID | Container, video / audio, subtitles | Delivery | Start | Seek (to the middle) | Stop | Resume | Status |
|---|---|---|---|---|---|---|---|
| R01 | AVI, MPEG-4 ASP 400p / AC3 2.0, keyframes without PTS | transcode to H.264, AC3 → AAC; missing PTS generated for the plan and the job | yes | 538.7 s of 527 | 546.0 s | 555.3 s | verified 2026-10-31 at f542e55 (failed before: `main.m3u8` 500, #96) |
| R02 | AVI, MPEG-4 ASP 400p / MP3 2.0 | transcode to H.264, MP3 copied | yes | 227.9 s of 216 | 227.9 s | 237.2 s | verified 2026-10-31 |
| R03 | MKV, H.264 High 1080p / AC3 5.1 ×2, E-AC3 5.1 ×2, SubRip ×2 | remux, AC3 → AAC; SubRip inside HLS | yes | 665.6 s of 654 | 665.6 s | 674.8 s | verified 2026-10-31 |
| R04 | MP4, H.264 High 576p / AAC 2.0, AC3 2.0, `mov_text` ×3 | Direct Stream; `mov_text` as external WebVTT | yes | 831.8 s of 820 | 831.6 s | 840.9 s | verified 2026-10-31 |
| R05 | MKV, MPEG-2 576i (interlaced) / AC3 2.0, DVD subtitles ×12 | transcode to H.264 without deinterlacing; DVD subtitles not delivered | yes | 43.9 s of 32 | 43.9 s | 53.2 s | verified 2026-10-31 |
| R06 | MP4, HEVC Main 720p / AC3 5.1, E-AC3 5.1, `mov_text` | transcode to H.264; `mov_text` inside HLS | yes | 575.7 s of 565 | 575.5 s | 584.0 s | verified 2026-10-31 |
| R07 | MP4, H.264 High 576p / AC3 1.0, AC3 2.0, DVD subtitles | remux; DVD subtitles not delivered | yes | 177.8 s of 166 | 177.6 s | 186.9 s | verified 2026-10-31 |
| R08 | MP4, AV1 Main 10-bit 720p / AAC 2.0 | Direct Stream | yes | 1189.9 s of 1178 | 1189.9 s | 1198.4 s | verified 2026-10-31 |
| R09 | MKV, HEVC Main 10 HDR10 3840×2076 / AC3 5.1 ×2, SubRip ×3 | transcode to H.264 at source size; reported as SDR, no tone mapping | no: the first segment takes over 10 s and Emby Web restarts every 10 s | — | — | — | failed (#97, #22) |
| R10 | MKV, MPEG-2 576p / AC3 5.1, DTS 5.1 | transcode to H.264, AC3 → AAC | yes | 195.5 s of 184 | 195.3 s | 204.5 s | verified 2026-10-31 |
| R11 | MKV, HEVC Main 10 1920×816 / AC3 5.1 ×2, PGS | transcode to H.264; PGS not delivered | yes | 2660.7 s of 2652 | 2660.7 s | 2666.0 s | verified 2026-10-31 |
| R12 | MKV, H.264 High 576p / AC3 5.1, AC3 2.0, AC3 5.1, ASS, SubRip | remux; ASS and SubRip inside HLS | yes | 1506.5 s of 1495 | 1506.3 s | 1515.7 s | verified 2026-10-31 |
| R13 | MKV, H.264 High 10 720p / AC3 5.1, AC3 2.0 | remux, 10-bit H.264 copied (Emby Web's profile accepts `high 10`); decoded by Chromium, no dropped frames | yes | 690.3 s of 679 | 690.3 s | 699.3 s | verified 2026-10-31 |
| R14 | MKV, H.264 High 720p / AC3 2.0 ×2, FLAC 2.0, SubRip | remux of the default AC3 track; SubRip inside HLS | yes | 1511.5 s of 1500 | 1511.3 s | 1520.7 s | verified 2026-10-31 |
| R15 | MP4, HEVC Main 720p / four audio tracks (AC3 2.0 ×3, AC3 5.1), `mov_text` ×3 | transcode to H.264; `mov_text` inside HLS | yes | 595.9 s of 585 | 595.8 s | 603.6 s | verified 2026-10-31 |
| R16 | AVI, MPEG-2 576i / MP2 2.0 | transcode to H.264, MP2 → AAC | yes | 172.9 s of 161 | 172.8 s | 182.1 s | verified 2026-10-31 |
| S01E01 | MKV, H.264 High 576p / AC3 5.1, AC3 2.0, AC3 5.1, ASS, SubRip | remux; subtitles inside HLS | yes | 1531.5 s of 1520 | 1531.3 s | 1540.6 s | verified 2026-10-31 |
| S01E02 | MKV, H.264 High 576p / AC3 5.1, AC3 2.0, AC3 5.1, SubRip | remux; subtitles inside HLS | yes | 1543.5 s of 1532 | 1543.3 s | 1552.7 s | verified 2026-10-31 |

The seek column shows the position 12 s after seeking, so a value about 10 s past the target means playback continued from it. Not checked here: the picture after a seek frame by frame (the synthetic set covers that), switching tracks, and auto-advance between the episodes. Image subtitles (DVD, PGS) are not delivered; burn-in is not implemented. Interlaced MPEG-2 is transcoded without deinterlacing.

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
- A browser whose profile limits H.264 to 1920 width: the limit was verified only with requests to Couch (VALIDATION, "Transcoding to H.264").
- HEVC with open GOPs in MPEG-TS with copied audio: a restart from the middle may begin later than planned (#81); with encoded audio this is fixed and covered by a unit test, not checked in the browser.
