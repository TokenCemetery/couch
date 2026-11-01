# Design: HLS delivery — remux and transcoding (#21, #22)

Status: stages 1–2 (job manager and remux) implemented 2026-10-06 (`src/internal/hls`, `src/internal/server/hls.go`); stage 3 (transcoding to H.264) implemented 2026-10-07; browser check and CPU measurement are in [VALIDATION](VALIDATION.md). Decisions on the "Questions" section (2026-10-06): limit of 2 remux sessions (1 transcode for stage 3); segments in `<data>/transcode`, up to 2 GiB per session; segment length 6 seconds; start with remux; media for checks are synthetic, made with FFmpeg.

How the implementation differs from the design below:
- Segments are cut by the `segment` muxer from a list of times (`-segment_times`), not by `hls`; FFmpeg reports each closed segment through `-segment_list` into a pipe, from which the server knows readiness and immediately pauses the job (SIGSTOP) once it is 10 segments ahead. Polling the directory could not keep up: on a fast disk FFmpeg managed to write the whole file.
- Restart from segment `n`: `-seek_timestamp 1 -ss` slightly before the keyframe in the file's absolute time, `-copyts -copypriorss 0 -avoid_negative_ts disabled`. FFmpeg compares the `-copypriorss 0` threshold with absolute timestamps; a relative `-ss` with a negative `start_time` (FFprobe 6.1 and 7.1 give −0.006 s for MKV with AC3) dropped the keyframe itself (#64). Verified: segments match a continuous pass by video timestamps (MKV and MPEG-TS with nonzero `start_time`). The first segment may start one AAC frame later if the source audio starts before the video.
- Keyframes for the plan (#98; 2026-11-01): Couch reads them in Go from the container index: in Matroska and WebM the `Cues` of the video track (found before the first `Cluster` or through the `SeekHead`, `CueTime` × `TimestampScale`), in MP4 and MOV the sync samples (`stss`) with their decode times (`stts`), composition offsets (`ctts`) and the edit list (`elst`: empty edits and one edit), which give the `pts_time` FFprobe reports. It reads the header elements or boxes and the index only: 29 KB of R03.mkv (1.2 GiB) and 250 KB of R04.mp4, where the FFprobe listing below read 228 and 593 MiB. The index is used if one FFprobe run agrees with it — the first video packet is its first keyframe and a seek to the end lands on its last — and no two of its keyframes are more than two segments apart, since a sparse index would lengthen the segments. On R03–R15 and the series episodes the index held exactly the keyframes of a full packet scan, so the plan is the one all keyframes give; it differs from the seek-based plan only where the seeks skipped keyframes. Without a usable index (AVI, MPEG-TS, an MKV without `Cues` such as R10, a fragmented MP4, an MP4 with `rap ` sample groups) FFprobe seeks every 2 seconds (`-read_intervals t%+#1`) and takes the keyframe the seek lands on; this reads a few packets per seek rather than the whole file. Any subset of keyframes yields a plan FFmpeg cuts by. The step is shorter than the segment length because a seek lands on a frame no later than the target, and with a 6-second step segments stretch to 10–13 seconds. If a seek did not land on a keyframe (MPEG-TS), FFprobe reads all packets.
- Only a request for the same segment as the session's newest request may restart the job: a request overtaken by a request for another segment (for example, one the player cancelled while seeking) gets 503 instead of sending the job back. Concurrent requests for the same segment (retry, `HEAD` and `GET`) do not cancel each other (#74).
- Transcoding cuts 3 s segments, as Emby does, from the video's first packet: the `segment` muxer counts time from the job's first packet, so a grid from the file's start time missed every cut by one keyframe. The encoder places forced keyframes at those points (`-force_key_frames`). FFprobe reads only the header and the first video packet, not the keyframes (#98, #99; 2026-11-01). Until then transcoding used the remux's source keyframe plan, 6 s and longer; a fixed-step plan was first rejected because of the first-packet offset and because an MPEG-TS restart landed on a non-keyframe, which the restart below now handles. The encoder keeps the source time base (`-enc_time_base:v demux`, FFmpeg 7+); otherwise timestamps are rounded to the frame rate and the keyframe shifts by a frame. Restart uses a relative `-ss` without `-copypriorss`: when decoding, an absolute `-ss` counts `start_time` twice. Later (#81): a direct `-ss` to a segment in MPEG-TS could land past its keyframe, and HEVC with open GOPs started from the next one. Now an encoding restart decodes from the previous segment's keyframe and trims with `trim`/`atrim` filters by absolute time (`-copyts`); with copied audio, which cannot be filtered, the direct `-ss` remains.
- The bitrate cap is `VideoBitrate` in the URL (the parameter's presence means video is encoded); size is limited by `MaxWidth`/`MaxHeight` from the `Width`/`Height` conditions of the `h264` profile. `VideoLevel` reduces them to the level's largest frame (MaxFS) and caps `VideoBitrate` at its MaxBR for High (×1.25); macroblock rate (MaxMBPS) is handled by shrinking the frame at the source frame rate; `libx264` picks the level itself.
- A request without `PlaySessionId` or codecs (as third-party clients send) gets a session by token and stream parameters; video is copied, audio is copied for AAC/MP3.

## Current state

- Couch serves only the source file (Direct Stream, `src/internal/server/stream.go`). If the DeviceProfile does not allow it, PlaybackInfo answers `ErrorCode=NoCompatibleStream`, and Emby Web shows "no compatible streams".
- At runtime FFmpeg runs only to extract embedded subtitles (`src/internal/media/extract.go`). That code already has a concurrent job limit, waiting on the request context, a timeout, an output limit and output through a pipe without temporary files (#23).
- In e2e the behavioral check `hls-master-playlist` fails (404); HLS operations are marked `hlsDelivery` in `e2e/known_failures.go`.

## What the client expects

Verified on Emby Web 4.10.0.40 in Chromium (`-web-upstream` setup, `modules/browserdeviceprofile.js`). Video in the DeviceProfile:

| Container | Context | Protocol | Video | Audio |
|---|---|---|---|---|
| ts | Streaming | hls | hevc, h264 | mp3, aac |
| webm | Streaming | http | vpx | vorbis |
| mkv | Static | http | h264, hevc, av1, vp8, vp9 | mp3, aac, opus, flac, vorbis |
| mp4 | Static | http | h264 | mp3, aac, opus, flac, vorbis |

`Static` is the download context. For viewing, HLS (`ts`) or progressive WebM with VP8 remains.

`modules/common/playback/playbackmanager.js`:

- Takes `TranscodingUrl` through `apiClient.getUrl`. With `TranscodingSubProtocol=hls` it sets the type `application/x-mpegURL` and keeps the start position.
- Treats a URL with `.m3u8` as seekable: seeking happens inside the playlist, without a new PlaybackInfo.
- For progressive transcoding, seeking is a new PlaybackInfo with `StartTimeTicks`, and the position is counted from `transcodingOffsetTicks`.

Conclusion: a progressive stream through a pipe is simpler to implement but contradicts the declared profile. HLS with MPEG-TS segments is proposed.

## Proposed solution

### 1. Decision in PlaybackInfo

The order is:

1. **Direct Stream**, as now.
2. **Remux.** The video codec and its profile/level are allowed by the HLS profile. Video is copied; audio is copied if it is `aac` or `mp3`, otherwise encoded to AAC respecting `MaxAudioChannels`. Rotated video is not copied: MPEG-TS does not store rotation, while FFmpeg rotates the frames when encoding (2026-10-11, #22).
3. **Video transcoding to H.264** (`libx264`, preset `veryfast`, or the `-hwaccel` GPU encoder) with bitrate and resolution limited by the profile and `MaxStreamingBitrate`.
4. Otherwise, `NoCompatibleStream`, as now.

Response for options 2 and 3: `SupportsTranscoding=true`, `TranscodingSubProtocol=hls`, `TranscodingContainer=ts` and `TranscodingUrl=/videos/{id}/master.m3u8?...` with `MediaSourceId`, `PlaySessionId`, `api_key` and the selected codecs. As with `DirectStreamUrl`, the link is relative, the token is inside, and Couch does not write the URL to the log.

### 2. Playlists

- `master.m3u8`: one variant linking to `main.m3u8` with `BANDWIDTH` from the bitrate estimate; with `ManifestSubtitles=vtt`, also a subtitle group (section 4).
- `main.m3u8`: a VOD playlist for the whole movie (`#EXT-X-PLAYLIST-TYPE:VOD`, `#EXT-X-ENDLIST`). It is served immediately, so the client sees the full duration and can seek anywhere.
- **Segment boundaries.**
  - When transcoding, they are set by forced keyframes every N seconds (`-force_key_frames`), and the playlist matches FFmpeg's output.
  - With remux, video is copied, and cuts are possible only at source keyframes. A playlist computed from a fixed length would diverge from the real segments: a known source of stutter when seeking. The proposal is to take a keyframe list on the first remux request (`ffprobe -skip_frame nokey`, video packets only), build segments from it (no shorter than N seconds) and keep the list in memory until restart. The list is taken by an FFprobe job with the same limits.

### 3. FFmpeg jobs

- **One job per PlaySessionId.** It writes segments `seg-%05d.ts` to the session's temporary directory, starting from the requested number (`-ss` at the segment start, `-start_number`).
- **Request for segment `n`:**
  - the segment is ready: it is served;
  - the job is about to produce it (no more than K segments ahead): the request waits for the file with a timeout;
  - otherwise (seek): the job is restarted from `n`.
- **Limits:**
  - no more than M concurrent transcoding jobs and R remux jobs;
  - when exhausted, PlaybackInfo returns a clear error rather than a URL that will not work;
  - output is limited by the session directory size;
  - keyframes are read for no more than two files at once; the rest wait in a queue;
  - FFmpeg runs with a clean environment, input through `fd:`, as with FFprobe.
- **Release:**
  - `Sessions/Playing/Stopped`, logout or 60 seconds without segment requests: the process ends and the directory is deleted;
  - stopping Couch ends FFmpeg jobs and keyframe reads;
  - at startup Couch deletes leftover directories (like temporary state files, #54).
- **FFmpeg failure.** The segment request gets 500, and the player shows a playback error. The server keeps running; the item ID and exit code are written to the log.

### 4. Subtitles

For a client that accepts WebVTT inside HLS (`{"Format":"vtt","Method":"Hls"}`, like Emby Web), text subtitles under HLS are delivered in the stream, as Emby does: PlaybackInfo marks them `DeliveryMethod=Hls` and adds `SubtitleStreamIndexes` and `ManifestSubtitles=vtt` to `TranscodingUrl`; `master.m3u8` lists them in index order (Emby Web matches tracks by position), and `subtitles.m3u8` serves one WebVTT segment for the whole duration. Its `X-TIMESTAMP-MAP` gives the MPEG-TS timestamp of the file start: `start_time` plus 1.4 s of FFmpeg muxer delay (`-copyts`). The first video segment may start one AAC frame later due to encoder delay, so the test checks the second segment. For other clients and under Direct Stream, delivery stays external (#24). Image subtitles are burned into a transcode: the overlay runs before trimming and scaling, and the session key includes the track, so selecting or turning it off starts another session.

## Stages

| # | Stage | Verification |
|---|---|---|
| 1 | Session directory, job manager (limits, idle, stop, cleanup at startup) without HLS routes | unit tests with a fake process: limit, cancellation, failure, directory deletion |
| 2 | Remux: decision in PlaybackInfo, keyframes, `master.m3u8`/`main.m3u8`, segments, seeking by restart | e2e `hls-master-playlist` passes; browser: MKV with AC3 plays, seeks, stops, directory deleted |
| 3 | Transcoding to H.264 with profile limits | browser: HEVC 10-bit or MPEG-4 Part 2 from the #16 set; CPU measurement (#29) |
| 4 | Client × format × delivery method matrix ([COMPATIBILITY](COMPATIBILITY.md): Emby Web in Chromium, 2026-10-07) | #20 |

Each stage is a separate commit that works on its own.

## Questions

1. Concurrent job limits: how many transcodes (NAS CPU) and remuxes? Proposal: 1 transcode and 2 remuxes.
2. Where to keep temporary segments: in `<data>/transcode` (the same volume as state) or in the system temporary directory (may be tmpfs in RAM)? And what size limit per session?
3. Segment length: 6 seconds (Jellyfin's default; Emby's value not checked) or 3 for faster seeking at the cost of more requests?
4. Start with remux (stage 2) without video transcoding? For a typical library with H.264 in MKV and AC3/DTS audio this is enough, and the CPU load is low.
5. Media set #16 is not agreed on yet. Stages 2–3 need at least: MKV H.264 + AC3, MKV HEVC 10-bit + DTS, AVI MPEG-4 Part 2. Can they be generated synthetically with FFmpeg, as in e2e?
