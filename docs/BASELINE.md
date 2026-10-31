# Resource baseline: Emby and Couch (#14)

Status: two runs, 2026-10-31, plus a third Couch run after the x264 change ("Couch after Emby's x264 options" below). The second run adds I/O, temporary files and seek time; a fourth, on the large profile, measures the server process and FFmpeg separately. The phases both runs share agree within about 3 %; more runs are needed for a real variance figure. Requirements: [PRD](PRD.md) G-2; budgets that build on this: #15.

## Method

`scripts/baseline.sh emby|couch small|average|large MEDIA_DIR OUT` starts one server fresh in a resource-limited container. It uses the same media and the same client profile for both servers, and appends one JSON line per phase.

- **Profiles:** small is 1 CPU and 1 GiB, average 2 CPU and 2 GiB, large 4 CPU and 4 GiB (`--cpus`, `--memory`, swap off).
- **Isolation:** both servers run on the internal Docker network `couch-bench`, which has no route out. The host reaches them directly, so the media's titles never leave the host. Emby keeps its defaults, except internet metadata providers, real-time monitoring and missing-episode import are off.
- **Client:** every PlaybackInfo sends Emby Web 4.10.0.40's Chromium profile ([fixture](fixtures/emby-4.10.0.40/deviceprofile-chromium.json)).
- **Media:** the real library set from [COMPATIBILITY](COMPATIBILITY.md) ("Real library files"), all 18 files.
  - R04 (MP4 H.264 + AAC, 503 MiB) for Direct Stream;
  - R03 (MKV H.264 1080p + AC3 5.1) for remux;
  - R06 (MP4 HEVC 720p + AC3/E-AC3 5.1) for transcoding;
  - R09 (MKV HEVC 10-bit HDR10 3840×2076) for 4K.
- **Measures, per phase, for the whole container (server plus FFmpeg):**
  - wall time;
  - CPU from `cpu.stat`;
  - peak anonymous memory from `memory.stat`, sampled every 0.2 s. `memory.current` would also count the page cache of the media read, which is reclaimable;
  - bytes read from and written to the disk, from `io.stat`;
  - peak size of the server's transcode folder (`/data/transcode` for Couch, `/config/transcoding-temp` for Emby), sampled every 0.2 s.

  Also the server process's own peak RSS (`VmHWM`).
- **Measures, per phase, for the server process alone:** its CPU from `/proc/PID/stat`, so the rest of the container's CPU is FFmpeg and FFprobe, and its peak anonymous memory (`RssAnon`) next to the peak sum over the `ffmpeg` and `ffprobe` processes running at once, both sampled every 0.2 s.
- **Cache:** before the scan and before each phase that reads media, the files are evicted from the page cache (`dd iflag=nocache`, which works as a normal user). Reads therefore come from the disk, as on a server that has not just read them.
- **Phases:**
  1. Startup and library scan: Couch scans before it listens; Emby runs its wizard and adds the library with a scan.
  2. 60 s idle.
  3. 100 requests to each of seven API endpoints.
  4. A full Direct Stream download.
  5. HLS: time to the first segment, then segments fetched one after another until 120 s of video (18 s for 4K), as fast as the server delivers. Not real-time pacing.
  6. Seek:
     - in the direct stream, five 1 MiB byte ranges at 10–90 % of the file, timed to the first byte;
     - in HLS, a new session fetches its first segment, then requests the segment at 50 % and later at 75 % of the playlist, as a player does after a seek, timed to the whole segment. The server restarts its encoding there.

Bench: AMD Ryzen 7 255 (16 threads), 22 GiB RAM, Linux 7.0, Docker 29.9.2, cgroup v2, local NVMe. Images:

- `emby/embyserver:4.10.0.40` with its bundled FFmpeg 5.1 (Emby build);
- Couch `main` at c138f56 in the release image, with Alpine FFmpeg 8.1.

## Results

### Idle, startup and scan

| | Couch | Emby |
|---|---|---|
| Startup + scan of 18 files, wall | 1.2 s (all profiles) | 5.1 s small, 3.4 s average and large |
| Startup + scan, CPU | 0.8 s | 2.3–2.4 s |
| Startup + scan, peak anonymous memory | 6–13 MiB | 85–118 MiB |
| Idle 60 s, CPU | 0 s | 0.13 s small, 0.35 s average and large |
| Idle, anonymous memory | 4 MiB | 71–72 MiB |
| Server process peak RSS at idle | 11 MiB | 202–206 MiB |
| Server process peak RSS at the end of the run | 14–15 MiB | 237–245 MiB |

### API latency, p50 / p95 in ms (100 requests each)

| Endpoint | Couch small | Couch average | Couch large | Emby small | Emby average | Emby large |
|---|---|---|---|---|---|---|
| `Users/{id}/Views` | 0.4 / 0.4 | 0.3 / 0.5 | 0.4 / 0.5 | 1.9 / 2.4 | 1.4 / 1.7 | 1.1 / 1.6 |
| Movies, 50 per page | 2.2 / 2.5 | 2.1 / 2.7 | 2.2 / 2.8 | 3.4 / 4.6 | 1.7 / 2.7 | 1.9 / 2.6 |
| Item | 0.5 / 0.6 | 0.5 / 0.6 | 0.5 / 0.6 | 3.6 / 4.3 | 2.1 / 2.8 | 2.2 / 2.6 |
| Latest | 2.2 / 2.7 | 2.2 / 2.6 | 2.3 / 2.8 | 4.0 / 4.9 | 2.1 / 2.7 | 2.1 / 3.0 |
| Resume | 0.4 / 0.4 | 0.3 / 0.4 | 0.3 / 0.4 | 1.4 / 1.5 | 0.7 / 0.8 | 0.7 / 0.9 |
| Search | 1.3 / 1.5 | 1.2 / 1.5 | 1.2 / 1.5 | 3.3 / 5.1 | 1.6 / 2.4 | 1.7 / 2.3 |
| PlaybackInfo (Chromium profile) | 0.6 / 0.7 | 0.6 / 0.7 | 0.6 / 0.7 | 4.2 / 5.1 | 2.3 / 3.2 | 2.3 / 3.2 |

On two or more CPUs Emby answers the movie list and Latest 0.1–0.4 ms faster (p50). Couch looks up local images for every item in a list, and this set has none to find.

### Playback

| Phase | Measure | Couch small | Couch average | Couch large | Emby small | Emby average | Emby large |
|---|---|---|---|---|---|---|---|
| Direct Stream, 503 MiB | CPU | 0.24 s | 0.26 s | 0.25 s | 0.22 s | 0.45 s | 0.58 s |
| | peak anon. memory | 7 MiB | 7 MiB | 7 MiB | 72 MiB | 89 MiB | 89 MiB |
| Remux H.264, AC3 → AAC, 120 s of video | first segment | 0.35 s | 0.25 s | 0.25 s | 0.34 s | 0.33 s | 0.33 s |
| | CPU | 2.1 s | 2.2 s | 2.1 s | 1.9 s | 2.1 s | 2.0 s |
| | peak anon. memory | 22 MiB | 22 MiB | 22 MiB | 86 MiB | 99 MiB | 99 MiB |
| Transcode HEVC 720p → H.264, 120 s of video | first segment | 4.5 s | 2.5 s | 1.4 s | 1.4 s | 0.8 s | 0.5 s |
| | wall | 54 s | 28 s | 14 s | 35 s | 17 s | 8.5 s |
| | CPU | 54 s | 55 s | 55 s | 35 s | 34 s | 33 s |
| | peak anon. memory | 383 MiB | 384 MiB | 383 MiB | 368 MiB | 368 MiB | 368 MiB |
| Transcode HEVC 10-bit 4K → H.264 | result | out of memory, no segment | out of memory, no segment | 20 s of video | out of memory, no segment | out of memory, no segment | 18 s of video |
| | first segment | — | — | 27.8 s | — | — | 4.4 s |
| | CPU | — | — | 158 s | — | — | 90 s |
| | peak anon. memory | 905 MiB, near the limit | 1899 MiB, near the limit | 3324 MiB | 870 MiB, near the limit | 1911 MiB, near the limit | 2603 MiB |

The HLS segments differ:

- Couch cuts at source keyframes, from 6 s up: 15 segments for 120 s in the remux, 13 in the transcode.
- Emby cuts 6 s segments for remux and 3 s segments with a keyframe every 3 s for transcoding.
- Couch's transcode is 18 MiB per 122 s, Emby's 25 MiB per 120 s. Emby also encodes the audio to MP3 at 192 kbit/s.

### Seek

| | Couch small | Couch average | Couch large | Emby small | Emby average | Emby large |
|---|---|---|---|---|---|---|
| Direct Stream, median time to first byte | 0.6 ms | 0.6 ms | 0.6 ms | 2.7 ms | 1.7 ms | 1.8 ms |
| Remux R03, to 50 % / to 75 % | 0.33 / 0.35 s | 0.21 / 0.21 s | 0.21 / 0.21 s | 0.23 / 0.20 s | 0.21 / 0.15 s | 0.21 / 0.14 s |
| Transcode R06, to 50 % / to 75 % | 6.0 / 6.6 s | 3.1 / 3.4 s | 1.6 / 1.8 s | 1.5 / 1.7 s | 0.76 / 0.80 s | 0.36 / 0.46 s |

A seek in a transcode waits for the new first segment, so it follows the time to the first segment above. Couch's segments are 6 s and longer, Emby's 3 s, and Couch's encoder is slower.

### Disk I/O and temporary files

Read from the disk per phase, with the media evicted from the page cache first (MiB; ranges are over the three profiles):

| Phase | Couch | Emby |
|---|---|---|
| Startup and scan of 18 files | 85 | 18 |
| 700 API requests (with 100 PlaybackInfo for R03) | 195–202 | 0 |
| Direct Stream of R04, 503 MiB | 503 | 503 |
| Remux of R03, 120 s of video | 117–118 | 115–120 |
| Transcode of R06 (a 322 MiB file), 120 s of video | 321 | 39 |
| Transcode seek in R06 | 39 | 10–14 |
| 4K transcode of R09 (a 10 GiB file) | 707–759 | 0.4–51 |

Written to the disk: at most 0.2 MiB per phase; only Emby wrote, during startup and idle. Transcoded segments stay in the page cache and are deleted before the kernel flushes them.

Peak size of the transcode folder (MiB):

| Phase | Couch | Emby |
|---|---|---|
| Remux of R03 | 46–66 | not captured (see below) |
| Transcode of R06 | 7.2–7.4 | 96–97 |
| Remux seek / transcode seek | 16–27 / 2.6–2.8 | 89–96 / 72–76 |
| 4K, large | 33 | 31 |

Couch deletes segments the player has passed. Emby keeps a session's segments until it ends.

Two gaps in the second run: Emby's memory sampler read 0 during startup, so startup memory above is from the first run; and Emby's transcode folder showed no files during the remux phase, though it did during the remux seek right after.

## Findings

- **Idle and API:** Couch idles at about 1/18 of Emby's anonymous memory (4 vs 71 MiB) and about 1/18 of its RSS (11 vs 202 MiB), with no idle CPU. Its API answers are 1.5–7 times faster on the small profile. On two or more CPUs, five endpoints stay 1.3–4.7 times faster, and the movie list and Latest are slightly slower than Emby's.
- **Direct Stream and remux:** the cost is about the same. Couch holds less memory (7 vs 72–89 MiB, 22 vs 86–99 MiB).
- **Transcoding: Couch costs more.**
  - 1.6 times Emby's CPU for the same 120 s of 720p HEVC.
  - Its first segment comes 3 times later.
  - At 4K with 4 CPUs, the first segment comes after 27.8 s instead of 4.4 s, beyond Emby Web's 10 s wait (#97).

  Emby's FFmpeg command shows why. Its preset and CRF match Couch's (`veryfast`, CRF 23), but it adds `-x264opts subme=0:me_range=4:rc_lookahead=10:partitions=none` and makes 3 s segments with a keyframe every 3 s. Those options cut motion search and shorten the lookahead, which also lowers memory at 4K. Picture quality was not compared.
- **4K needs a large container on both servers:** both run out of memory at 1 and 2 GiB, because both transcode at the source size.
- **Couch reads much more of the file before transcoding:**
  - all 321 MiB of a 322 MiB MP4 before 120 s of a 720p transcode, against Emby's 39 MiB;
  - about 750 MiB of a 10 GiB MKV before the first 4K segment.

  The cause is its keyframe listing, which plans segments on the source's keyframes. `PlaybackInfo` already starts it for an item that will need HLS, which is why 100 `PlaybackInfo` calls read about 200 MiB. On a local NVMe this costs little time. On a NAS volume it delays the first segment by the time to read that much.
- **Seeking:** within a direct stream, Couch answers a seek in 0.6 ms against Emby's 1.7–2.7 ms. Within a remux both take 0.14–0.35 s. Within a transcode Couch takes about 4 times longer, for the same reasons as the first segment.

## Couch after Emby's x264 options (#99)

Third run, Couch only, same script, bench and media: Couch `main` at f5f9eed, which adds Emby's `subme=0:me_range=4:rc_lookahead=10:partitions=none` to transcoding (5b2b7b8). Remux, Direct Stream and the API did not change. The Emby figures are from the runs above.

| Phase | Measure | Couch before | Couch now | Emby |
|---|---|---|---|---|
| Transcode HEVC 720p → H.264, 120 s of video | CPU | 54–55 s | 36–37 s | 33–35 s |
| | wall, small / average / large | 54 / 28 / 14 s | 37 / 19 / 9.8 s | 35 / 17 / 8.5 s |
| | first segment, small / average / large | 4.5 / 2.5 / 1.4 s | 3.4 / 2.0 / 1.4 s | 1.4 / 0.8 / 0.5 s |
| | peak anon. memory | 383 MiB | 303 MiB | 368 MiB |
| | output | 18 MiB per 122 s | 22 MiB per 122 s | 25 MiB per 120 s |
| Transcode seek in R06, to 50 %, small / average / large | | 6.0 / 3.1 / 1.6 s | 4.5 / 2.4 / 1.25 s | 1.5 / 0.76 / 0.36 s |
| 4K transcode, large | first segment | 27.8 s | 18.2 s | 4.4 s |
| | CPU | 158 s | 100 s | 90 s |
| | peak anon. memory | 3324 MiB | 2657 MiB | 2603 MiB |
| 4K transcode, small and average | result | out of memory | out of memory | out of memory |

- Transcoding CPU is now within 10 % of Emby's, and memory is below it. Picture quality on R06 stayed within 0.002 SSIM and 0.05 dB PSNR ([VALIDATION](VALIDATION.md), "x264 options from Emby").
- The first segment and a transcode seek are still 2.5–3.5 times slower than Emby's. Couch's segments are 6 s and longer against Emby's 3 s, and it reads the source for its keyframe plan first (#98); reads did not change (321 MiB for R06).
- 4K still starts after Emby Web's 10 s wait (#97).

## Server process and FFmpeg separately

Fourth run, 2026-11-01, large profile only, same script, bench and media: Couch at f5f9eed (the `couch:dev` image), and Emby. An unrelated 4-CPU soak test of Couch ran on the host throughout, so wall times and first segments of this run are not comparable and are left out. CPU seconds and memory are.

| Phase | Measure | Couch server | Couch FFmpeg | Emby server | Emby FFmpeg |
|---|---|---|---|---|---|
| Startup and scan | CPU | 0.11 s | 0.82 s | 2.54 s | 0.41 s |
| Idle 60 s | CPU | 0 s | 0 s | 0.35 s | 0.05 s |
| 700 API requests | CPU | 0.94 s | 0.32 s | 4.31 s | 0 s |
| Remux of R03, 120 s of video | CPU | 0.04 s | 2.26 s | 0.66 s | 1.69 s |
| | peak anon. memory | 7 MiB | 15 MiB | 85 MiB | not captured |
| Transcode of R06, 120 s of video | CPU | 0.05 s | 40.4 s | 0.82 s | 37.1 s |
| | peak anon. memory | 7 MiB | 296 MiB | 91 MiB | 277 MiB |
| Transcode seek in R06 | CPU | 0.01 s | 19.9 s | 0.21 s | 13.1 s |
| 4K transcode of R09 | CPU | 0.14 s | 106 s | 0.41 s | 94 s |
| | peak anon. memory | 7 MiB | 2650 MiB | 92 MiB | 2511 MiB |

- **The server process:** Couch's stays at 7 MiB of anonymous memory through every phase, Emby's at 63–92 MiB. During HLS Couch's own process uses 0.01–0.14 s of CPU, Emby's 0.2–0.8 s.
- **FFmpeg:** Couch's costs more than Emby's in every HLS phase: 9 % more CPU and 7 % more memory for the 720p transcode, 13 % and 6 % for 4K, 1.5 times the CPU for a transcode seek. Couch's container figures are lower or close only because its server process is smaller. So the transcode budgets (#99) are FFmpeg work: the command line and its keyframe plan, not the Go server.
- **FFprobe outside playback:** Couch's 0.32 s of CPU during the API phase is its keyframe listing, which `PlaybackInfo` starts (#98). Its scan also probes in FFprobe (0.82 s), while Emby's scan spends its CPU in the server process.
- **Gaps:** Emby's remux FFmpeg finished between samples, so its memory reads 0; Emby's startup memory reads 0 as in the second run. The split counts FFmpeg's memory as `RssAnon`, so it can differ from the container's `anon` figure by a few MiB.

## 3 s transcode segments (#97, #98, #99)

Fifth run, 2026-11-01, large profile only, same script, bench and media: Couch at the commit that adds 3 s transcode segments, in an image built from it. The soak test from the fourth run was still running on the host, so the comparison is with Couch and Emby from the fourth run, measured under the same load, not with the tables above.

A transcode now cuts 3 s segments from the video's first frame, as Emby does, with a forced keyframe at each. Its plan needs only the file's header and first packet, so `PlaybackInfo` no longer lists the source's keyframes for a transcode. A remux still does.

| Phase | Measure | Couch before | Couch now | Emby |
|---|---|---|---|---|
| Transcode of R06, 120 s of video | first segment | 1.80 s | 0.70 s | 0.47 s |
| | CPU | 40.5 s | 40.5 s | 38.0 s |
| | read from disk | 321 MiB | 39 MiB | 39 MiB |
| | peak anon. memory | 303 MiB | 304 MiB | 368 MiB |
| Transcode seek in R06, to 50 % / to 75 % | time | 1.44 / 1.38 s | 0.77 / 0.62 s | 0.56 / 0.47 s |
| | CPU | 19.9 s | 13.8 s | 13.3 s |
| 4K transcode of R09 | first segment | 20.8 s | 5.4 s | 4.5 s |
| | CPU | 106 s | 96 s | 94 s |
| | read from disk | 758 MiB | 53 MiB | 51 MiB |
| 700 API requests | read from disk | 126 MiB | 139 MiB | 0 MiB |

- **4K now starts within Emby Web's 10 s wait** on the large profile, 0.9 s behind Emby. This was not checked in the browser.
- **Reads before a transcode** equal Emby's. The API phase still reads 126–139 MiB: the keyframe listing for R03, which needs a remux, so this change does not affect it.
- **First segment and transcode seek** are 0.15–0.23 s behind Emby's. CPU for the same 120 s is 7 % above Emby's.
- **Picture quality:** FFmpeg alone, first 120 s of R06, same x264 options, against the source. A keyframe every 3 s instead of about every 6 s gives SSIM 0.9831 against 0.9826 and PSNR Y 41.84 dB against 41.74 dB, for an output 5 % larger (24.4 against 23.2 MiB) at the same CPU.

## Not measured yet

- Picture quality of the two encoders.
- More runs for variance.
- The split above on the small and average profiles, and on a host without other load.
- A real NAS, where the read volumes above turn into time, and Emby's internet metadata, which the offline network rules out.
