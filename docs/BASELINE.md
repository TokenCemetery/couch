# Resource baseline: Emby and Couch (#14)

Status: two runs, 2026-10-31, plus a third Couch run after the x264 change ("Couch after Emby's x264 options" below). The second run adds I/O, temporary files and seek time; a fourth, on the large profile, measures the server process and FFmpeg separately; a fifth tries the 3 s transcode segments; the sixth repeats both servers on all profiles with no other load on the host, and the budgets compare with its Emby figures; the seventh repeats Couch after the thread and keyframe changes; the eighth, twice on every profile, after the remux plan's FFprobe runs were cut from two to one, and the budgets' Couch figures are from it. A ninth run ("VA-API") encodes on the target GPU. Phases shared across runs agree within about 5 %. Requirements: [PRD](PRD.md) G-2; budgets that build on this: #15.

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

## Sixth run: all profiles, no other load (2026-11-01)

Couch at b5c619e (with the saved catalog index and the 3 s transcode segments) and Emby 4.10.0.40, one after the other on each profile, same script, bench and media, with nothing else running on the host. These are the figures the budgets in [ROADMAP](ROADMAP.md) M0 now use. Small / average / large where they differ.

| Phase | Measure | Couch | Emby |
|---|---|---|---|
| Startup and scan | wall / CPU / reads | 1.2 s / 0.9 s / 85 MiB | 5.1 / 3.5 / 3.4 s / 2.3–2.5 s / 25 MiB |
| Idle 60 s | anon. memory / server RSS / CPU | 4 MiB / 11–12 MiB / 0 s | 67–70 MiB / 201–205 MiB / 0.13–0.36 s |
| 700 API requests | reads | 185–192 MiB | 0 MiB |
| Direct Stream of R04 | CPU / memory / seek to first byte | 0.32–0.33 s / 7 MiB / 0.6–0.7 ms | 0.53–0.74 s / 74–88 MiB / 1.9–2.7 ms |
| Remux of R03, 120 s | CPU / memory / first segment | 2.1–2.2 s / 22 MiB / 0.34 / 0.24 / 0.24 s | 1.9–2.0 s / 74–88 MiB / 0.34 / 0.33 / 0.32 s |
| | seek to 50 % / 75 % | 0.31 / 0.33, 0.21 / 0.21, 0.21 / 0.21 s | 0.22 / 0.19, 0.20 / 0.20, 0.20 / 0.14 s |
| Transcode of R06, 120 s | CPU | 36.1 / 36.8 / 36.9 s | 34.8 / 33.7 / 33.8 s |
| | first segment | 1.59 / 0.94 / 0.56 s | 1.39 / 0.79 / 0.47 s |
| | seek to 50 % / 75 % | 2.02 / 2.02, 1.08 / 1.09, 0.66 / 0.53 s | 1.75 / 1.67, 0.88 / 0.89, 0.40 / 0.52 s |
| | memory: container / server / FFmpeg | 303 / 7 / 296 MiB | 368 / 91 / 277 MiB |
| | reads / temp folder peak | 39.0–39.2 MiB / 3.9–4.9 MiB | 38.9–39.0 MiB / 94–100 MiB |
| | output | 24 MiB in 40 segments | 25 MiB in 40 segments |
| 4K transcode of R09 | small and average | out of memory, no segment | out of memory, no segment |
| | large: first segment / CPU / memory / reads | 4.99 s / 90.6 s / 2584 MiB / 52.7 MiB | 4.36 s / 90.7 s / 2602 MiB / 50.8 MiB |

API p95 in ms, Couch against Emby, small / average / large: Views 0.4–0.5 against 1.7–2.2; Movies 2.7 / 2.5 / 2.7 against 3.7 / 2.6 / 2.6; Item 0.6–0.7 against 2.5–4.0; Latest 2.7 / 2.7 / 2.6 against 4.8 / 2.5 / 3.2; Resume 0.4 against 0.8–1.5; Search 1.4–1.5 against 2.6–4.6; PlaybackInfo 0.7–0.8 against 3.1–4.8.

- **Transcoding is now close to Emby's.** The first segment and a seek come 0.09–0.27 s later, and CPU is 4–9 % higher. Memory, the temp folder and reads are equal or lower. The server/FFmpeg split shows the gap is FFmpeg's: Couch's own process stays at 7 MiB.
- **4K** starts on the large profile 0.6 s after Emby's, with the same CPU and less memory. Both servers run out of memory on 1 and 2 GiB.
- **Variance:** Emby's figures agree with the first two runs within about 5 %, Couch's remux, Direct Stream and API with the third run within about 5 %.
- **Still not met:** remux CPU and seek by up to 0.1 s, and the API phase's reads (the remux keyframe listing, #98). Startup reads are met since, see below.

## Scan reads (2026-11-01)

The startup scan read 85 MiB of the 18 files, against Emby's 25 MiB. Measured per file in a container, with the page cache evicted before each run, FFprobe reads 17.5 MiB in all by path, but 7–18 MiB for each MP4 through a descriptor (`-fd 3 -i fd:`), as Couch runs it. MKV and AVI files read the same either way. FFmpeg's `fd` protocol reads in unbounded blocks by default, so each seek in an MP4 header pulls in megabytes; with `-blocksize 65536`, R04 reads 1.8 MiB instead of 17.8 MiB, as by path.

- Couch's scan probe now passes `-blocksize 65536`. Its output is byte-identical on all 18 files.
- Startup on the small profile, two runs: reads 85 → 18.6 MiB, wall time 1.24 s and CPU 0.8 s unchanged.
- Not a cause: `-probesize`. At 1 MB instead of 5 MB the output is the same, but reads only drop to 80 MiB, so it was left at 5 MB.
- The keyframe listing of a remux (#98) gains nothing from the block size: R03 reads about 205 MiB either way, as it seeks every 2 s through the file.

## List requests (2026-11-01)

The movie list and Latest were the only API requests where Couch's p95 was above Emby's in the sixth run: Movies on large (2.7 against 2.6 ms) and Latest on average (2.7 against 2.5 ms). A CPU profile of the movie list with the 18 real files: JSON encoding of the map-based items takes about 62 % of the handler, a third of that sorting map keys; image lookup about 20 %, mostly a `stat` of the item's folder for each of the four image types.

- Couch now checks each item's folder once for all image types (`Catalog.ImageInfos`); the rules for when a listing is reused are unchanged.
- Handler time with the real files: Movies 1.41 → 1.17 ms, Latest 1.45 → 1.22 ms (Go benchmark, 2000 requests each).
- `scripts/baseline.sh`, Couch only, all profiles, p95 small / average / large, with Emby's from the sixth run: Movies 2.7 / 2.5 / 2.6 ms against 3.7 / 2.6 / 2.6; Latest 2.6 / 2.5 / 2.6 ms against 4.8 / 2.5 / 3.2. The other five endpoints did not change. Two cells now equal Emby's at the 0.1 ms resolution, so the margin there is none.

## Where the remaining FFmpeg CPU goes (2026-11-01)

After the changes above, Couch's remux and transcode still cost more CPU than Emby's: remux 2.1–2.2 s against 1.9–2.0 s, transcode 36–37 s against 34–35 s. The server/FFmpeg split puts the difference in FFmpeg. Measured with each server's own FFmpeg binary, 4 CPUs, 120 s of the same file, `-benchmark`:

| Work | Couch's FFmpeg 8.1.3 (Alpine) | Emby's FFmpeg 5.1 (`5.1-emby_2023_06_25_p6`) |
|---|---|---|
| R03 audio, AC3 5.1 → AAC stereo 128 kbit/s | 1.52 s user + 0.12 s system | 1.51 s + 0.016 s |
| R03 audio, AC3 5.1 → MP3 stereo 192 kbit/s | 1.62 s + 0.12 s | 1.29 s + 0.007 s |
| R06 video, x264 `veryfast` CRF 23 with Emby's options | 31.4 s + 0.85 s | 30.2 s + 0.81 s |

- **Remux:** Emby encodes the audio of an HLS remux to stereo MP3 at 192 kbit/s, as Emby Web's profile lists `mp3` before `aac`; Couch encodes AAC. Emby's MP3 encoder is about 20 % faster than Alpine's, and Alpine's build spends 7–17 times more system time on the same audio. With its own build, MP3 would not make Couch cheaper (1.73 against 1.64 s), so Couch keeps AAC.
- **Remux job length:** not a cause. In the remux phase Couch's job finished 15 segments (about 120 s, as the harness fetched) for 2.14 s of CPU; Emby's encoded 116.5 s. Neither ran far ahead.
- **Transcode:** the same x264 settings cost 4 % more CPU with Couch's build, most of the 4–9 % gap; the rest is the audio, as above.

So the remaining CPU difference comes from the FFmpeg build, not from what Couch asks it to do. Closing it means another FFmpeg build in the image, for example a glibc-based one, which is a packaging choice (#33).

A trial of glibc builds found none cheaper. Median of 3 at `--cpus 4`, x264 on R06 and AAC on R03 as in the table, with a run-to-run spread of up to 3.5 %: Alpine 3.24 with FFmpeg 8.1.3 took 31.9 s and 1.65 s of CPU; Debian trixie with FFmpeg 7.1.5 differed by −0.3 % and +4.3 %, Ubuntu 26.04 with FFmpeg 8.0.1 by −0.8 % and +0.3 %. The glibc images are also about 3.8 times larger (200 against 56 MB of content), so the image stays on Alpine.

## Encoder threads under a CPU quota (2026-11-01, #92, #99)

The first transcoded segment still came 0.1–0.3 s after Emby's, while FFmpeg encodes those 3 s of R06 in 0.96 s on one CPU. Emby's extra options (`-f mp4`, `-g 72 -keyint_min 72 -sc_threshold 0`) and Couch's own (`scale` filter, the long `-force_key_frames` list, reading through a descriptor) change nothing. What delays segment 0 is x264's frame threads: FFmpeg sizes them from the host's 16 CPUs, not the container's quota, and the segment closes only when the encoder emits the keyframe at 3 s, after as many frames as it has threads. Segment 0 of Couch's job alone, median of 3:

| Encoder threads | 1 CPU | 4 CPUs |
|---|---|---|
| automatic (now) | 1.57 s | 0.41 s |
| 1 | 0.98 s | 0.46 s |
| 2 | 1.10 s | 0.31 s |
| 4 | 1.21 s | 0.33 s |
| 8 | 1.28 s | 0.33 s |

`scripts/baseline.sh` with `COUCH_ARGS="-ffmpeg-threads N"`, N the container's CPUs (1 / 2 / 4), Couch at c4f1c78; automatic threads and Emby from the sixth run. Small / average / large:

| Phase | Measure | Couch, threads = CPUs | Couch, automatic | Emby |
|---|---|---|---|---|
| Transcode of R06, 120 s | first segment | 0.94 / 0.64 / 0.43 s | 1.59 / 0.94 / 0.56 s | 1.39 / 0.79 / 0.47 s |
| | seek to 50 % | 1.31 / 0.83 / 0.54 s | 2.02 / 1.08 / 0.66 s | 1.75 / 0.88 / 0.40 s |
| | seek to 75 % | 1.36 / 0.86 / 0.54 s | 2.02 / 1.09 / 0.53 s | 1.67 / 0.89 / 0.52 s |
| | CPU | 26.0 / 29.0 / 31.9 s | 36.1 / 36.8 / 36.9 s | 34.8 / 33.7 / 33.8 s |
| | peak anon. memory | 115 / 140 / 163 MiB | 303–304 MiB | 368 MiB |
| 4K transcode of R09 | first segment | 6.3 / 4.1 / 3.0 s | out of memory / out of memory / 5.0 s | out of memory / out of memory / 4.4 s |
| | peak anon. memory | 716 / 922 / 1141 MiB | 956 / 1965 / 2584 MiB | 679 / 1678 / 2602 MiB |

- With threads matching the quota, Couch meets every transcode budget except the seek on large (0.54 against 0.40–0.52 s): first segment, CPU and memory are below Emby's on all profiles.
- 4K now transcodes on 1 and 2 GiB, where both servers ran out of memory, and its first segment on large comes in 3.0 s against Emby's 4.4 s.
- Without a quota, #92 measured the opposite: automatic threads halve the encode time when the cores are free. The default therefore stays automatic until the owner decides otherwise.

## Seventh run: threads at the quota and remux keyframes from the index (2026-11-01)

Couch only, at e297beb, in the release image built from it, on all profiles, one after the other, same script, bench and media, with nothing else running on the host. Two changes since the sixth run: transcode threads now follow the CPU quota by default (bdfd6d6), and a remux reads its keyframes from the container index, with `PlaybackInfo` that sends `IsPlayback=false` no longer preparing the plan (e297beb). Couch ran with its defaults (no `COUCH_ARGS`). Emby was not rerun: its figures are from the sixth run, same bench and day.

Couch logged `FFmpeg transcode threads follow the CPU quota` with threads = 2 / 2 / 4, not 1 / 2 / 4: Go's `GOMAXPROCS` under a cgroup quota is rounded up with a minimum of 2, so on the 1-CPU profile the encoder runs 2 threads, not the 1 of the "Encoder threads under a CPU quota" run.

The API phase's 100 `PlaybackInfo` calls for R03 send `IsPlayback=false`, as Emby Web does for an item page, so they no longer prepare R03's plan. In the sixth run they did, and the remux phase then found it ready (its reads did not include the listing). The remux phase's own `PlaybackInfo` sends `IsPlayback=true`, so its first segment now includes preparing the plan: one index read and one FFprobe check.

Small / average / large where they differ:

| Phase | Measure | Couch now | Couch, sixth run | Emby |
|---|---|---|---|---|
| Startup and scan | wall / CPU / reads | 1.2 s / 0.8–0.9 s / 20.7 / 18.6 / 18.6 MiB | 1.2 s / 0.9 s / 85 MiB | 5.1 / 3.5 / 3.4 s / 2.3–2.5 s / 25 MiB |
| Idle 60 s | anon. memory / server RSS / CPU | 4 MiB / 11–12 MiB / 0 s | 4 MiB / 11–12 MiB / 0 s | 67–70 MiB / 201–205 MiB / 0.13–0.36 s |
| 700 API requests | reads | 0 MiB | 185–192 MiB | 0 MiB |
| Direct Stream of R04 | CPU / memory / seek to first byte | 0.32–0.34 s / 7 MiB / 0.7 ms | 0.32–0.33 s / 7 MiB / 0.6–0.7 ms | 0.53–0.74 s / 74–88 MiB / 1.9–2.7 ms |
| Remux of R03, 120 s | CPU / memory | 2.3 s / 22 MiB | 2.1–2.2 s / 22 MiB | 1.9–2.0 s / 74–88 MiB |
| | first segment | 0.40 / 0.40 / 0.30 s | 0.34 / 0.24 / 0.24 s | 0.34 / 0.33 / 0.32 s |
| | seek to 50 % / 75 % | 0.21 / 0.31, 0.21 / 0.21, 0.21 / 0.21 s | 0.31 / 0.33, 0.21 / 0.21, 0.21 / 0.21 s | 0.22 / 0.19, 0.20 / 0.20, 0.20 / 0.14 s |
| Transcode of R06, 120 s | CPU | 28.9 / 28.6 / 31.3 s | 36.1 / 36.8 / 36.9 s | 34.8 / 33.7 / 33.8 s |
| | first segment | 1.18 / 0.61 / 0.51 s | 1.59 / 0.94 / 0.56 s | 1.39 / 0.79 / 0.47 s |
| | seek to 50 % / 75 % | 1.42 / 1.44, 0.81 / 0.82, 0.51 / 0.51 s | 2.02 / 2.02, 1.08 / 1.09, 0.66 / 0.53 s | 1.75 / 1.67, 0.88 / 0.89, 0.40 / 0.52 s |
| | memory: container / server / FFmpeg | 140 / 140 / 163 MiB; 7 MiB; 133 / 133 / 155 MiB | 303 / 7 / 296 MiB | 368 / 91 / 277 MiB |
| | reads / temp folder peak | 39.9 / 39.0 / 39.0 MiB / 4.0–4.7 MiB | 39.0–39.2 MiB / 3.9–4.9 MiB | 38.9–39.0 MiB / 94–100 MiB |
| | output | 24 MiB in 40 segments | 24 MiB in 40 segments | 25 MiB in 40 segments |
| 4K transcode of R09, 18 s: first segment / CPU / memory / reads | small | 8.02 s / 61 s / 922 MiB / 49 MiB | out of memory, no segment | out of memory, no segment |
| | average | 4.10 s / 61 s / 922 MiB / 49 MiB | out of memory, no segment | out of memory, no segment |
| | large | 2.91 s / 68 s / 1141 MiB / 49 MiB | 4.99 s / 90.6 s / 2584 MiB / 52.7 MiB | 4.36 s / 90.7 s / 2602 MiB / 50.8 MiB |

API p95 in ms, Couch now against Emby, small / average / large: Views 0.5 against 1.7–2.2; Movies 2.5 against 3.7 / 2.6 / 2.6; Item 0.7 / 0.7 / 0.6 against 2.5–4.0; Latest 2.5 / 2.5 / 2.6 against 4.8 / 2.5 / 3.2; Resume 0.5 against 0.8–1.5; Search 1.3 / 1.6 / 1.6 against 2.6–4.6; PlaybackInfo 0.7 against 3.1–4.8.

- **Met now, newly:** transcode CPU (15–17 % below Emby's on 1 and 2 CPUs, 8 % on 4) and memory (140–163 against 368 MiB); the transcode first segment and both seeks on small and average; the 700 API requests' reads (0 MiB); 4K on large in 2.9 s with 25 % less CPU and 44 % of the memory. 4K also transcodes on 1 and 2 GiB, where both servers ran out of memory before; on small it peaks at 922 MiB of the 1 GiB limit and starts in 8.0 s, inside Emby Web's 10 s wait but close to it.
- **Still met:** idle, startup and scan, Direct Stream, remux memory, the transcode temp folder, and every API p95. Latest on average equals Emby's 2.5 ms at the 0.1 ms resolution.
- **Not met:**
  - on large, the transcode first segment (0.51 against 0.47 s) and the seek to 50 % (0.51 against 0.40 s), as in the threads trial;
  - remux CPU (2.3 against 1.9–2.0 s), the FFmpeg build plus the plan's FFprobe check, which moved from the API phase into this one;
  - remux first segment on small and average (0.40 against 0.34 / 0.33 s), as it now includes preparing the plan; on large it is 0.30 against 0.32 s;
  - remux seek to 50 % on average and large by 0.01 s (0.21 against 0.20 s); on small it is met (0.21 against 0.22 s);
  - transcode reads on small by 0.9 MiB (39.9 against 38.9–39.0 MiB); average and large equal Emby's.
- **Variance:** the phases that neither commit touched (startup, idle, Direct Stream, remux memory, the API's p95) agree with the sixth run within about 5 % or 0.1 ms.

## Eighth run: one FFprobe run for an indexed remux plan (2026-11-01, #29)

Couch only, in a release image built from d352009 plus this change, on all profiles, each twice, one after the other, same script, bench and media, with nothing else running on the host. Couch ran with its defaults. Emby's figures are from the sixth run.

The change: a remux plan read from the container index needed two FFprobe runs, one for the file's start time and duration and one checking the index's first and last keyframe. One run now does both: it reads the format's times and seeks past the end (`-read_intervals %+#1,99999999%+#1`) instead of to the start time plus the duration, which it does not know yet. On every MKV and MP4 of the media set (R03–R15 and the series episodes) that seek lands on the same last keyframe as the exact one. AVI and MPEG-TS, which have no index, still read the times first and then list the keyframes, as before.

Small / average / large, the range over the two runs where they differ:

| Phase | Measure | Couch now | Couch, seventh run | Emby |
|---|---|---|---|---|
| Remux of R03, 120 s | CPU / memory | 2.14–2.23 / 2.22–2.26 / 2.24–2.25 s / 22 MiB | 2.3 s / 22 MiB | 1.9–2.0 s / 74–88 MiB |
| | first segment | 0.37 / 0.26–0.27 / 0.27 s | 0.40 / 0.40 / 0.30 s | 0.34 / 0.33 / 0.32 s |
| | seek to 50 % / 75 % | 0.21 / 0.31–0.32, 0.21 / 0.21, 0.21 / 0.21 s | 0.21 / 0.31, 0.21 / 0.21, 0.21 / 0.21 s | 0.22 / 0.19, 0.20 / 0.20, 0.20 / 0.14 s |
| Transcode of R06, 120 s | CPU / memory | 29.1–29.2 / 28.9 / 31.4 s / 139–140 / 140 / 162–163 MiB | 28.9 / 28.6 / 31.3 s / 140 / 140 / 163 MiB | 34.8 / 33.7 / 33.8 s / 368 MiB |
| | first segment | 1.01–1.13 / 0.65–0.66 / 0.41 s | 1.18 / 0.61 / 0.51 s | 1.39 / 0.79 / 0.47 s |
| | seek to 50 % / 75 % | 1.33–1.42 / 1.43, 0.81 / 0.71–0.86, 0.51 / 0.51–0.52 s | 1.42 / 1.44, 0.81 / 0.82, 0.51 / 0.51 s | 1.75 / 1.67, 0.88 / 0.89, 0.40 / 0.52 s |
| | reads / temp folder peak | 39.0 MiB / 3.9–4.9 MiB | 39.9 / 39.0 / 39.0 MiB / 4.0–4.7 MiB | 38.9–39.0 MiB / 94–100 MiB |
| 4K transcode of R09, 18 s | first segment / CPU / memory | 7.8 / 4.1 / 2.9 s / 61 / 61 / 68 s / 922 / 922 / 1142 MiB | 8.0 / 4.1 / 2.9 s / 61 / 61 / 68 s / 922 / 922 / 1141 MiB | out of memory on small and average; 4.4 s / 91 s / 2602 MiB on large |

Startup and scan (1.2 s, 0.8–0.9 s CPU, 18.6 MiB read), idle (4 MiB, 11–12 MiB RSS, 0 s CPU), Direct Stream (0.31–0.34 s CPU, 7 MiB, 0.5–0.7 ms to the first byte of a seek) and the API's p95 (0.4–2.5 ms, every endpoint at or below Emby's) match the seventh run within 0.1 s, 0.1 ms or 2 MiB.

- **Met now, newly:** the remux first segment on average and large (0.26–0.27 against 0.33 / 0.32 s); the transcode first segment on large (0.41 against 0.47 s) and the transcode reads on small (39.0 MiB, Emby's upper figure). The transcode path does not run the changed code, so those two transcode figures are run-to-run variance: the seventh run's 0.51 s and 39.9 MiB were single runs, and both runs here agree.
- **Still not met:**
  - remux first segment on small, 0.37 against 0.34 s;
  - remux CPU, 2.1–2.3 against 1.9–2.0 s, mostly the FFmpeg build ("Where the remaining FFmpeg CPU goes");
  - remux seek to 50 % on average and large, 0.21 against 0.20 s;
  - transcode seek to 50 % on large, 0.51 against 0.40 s.

## VA-API: the target GPU (2026-11-01, #28)

The target hardware is this bench (#28): AMD Ryzen 7 255 with its Radeon 780M. Couch with `-hwaccel vaapi`, in a release image built with `--build-arg VAAPI=1` (Mesa's radeonsi VA-API driver), the render node passed in with `COUCH_DOCKER_ARGS="--device /dev/dri/renderD128 --group-add GID"`; otherwise the same script, bench, media and day, one run per profile, with nothing else running on the host. Decoding, scaling and audio stay on the CPU. The container's CPU and memory do not include the GPU's own work or its video memory, so the figures below are what the CPU quota and the memory limit pay, not the whole machine.

Small / average / large where they differ. Emby has no hardware encoding here (it needs Emby Premiere), so its column is its software encode from the sixth run:

| Phase | Measure | Couch, VA-API | Couch, `libx264` (eighth run) | Emby |
|---|---|---|---|---|
| Transcode of R06, 120 s | CPU | 13.5 / 13.2 / 14.7 s | 29.1–29.2 / 28.9 / 31.4 s | 34.8 / 33.7 / 33.8 s |
| | memory: container / FFmpeg | 65 / 65 / 76 MiB; 58 / 58 / 69 MiB | 139–163 MiB; 133–155 MiB | 368 MiB; 277 MiB |
| | first segment | 0.61 / 0.41 / 0.41 s | 1.01–1.13 / 0.65–0.66 / 0.41 s | 1.39 / 0.79 / 0.47 s |
| | seek to 50 % / 75 % | 0.94 / 0.91, 0.52 / 0.51, 0.41 / 0.41 s | 1.33–1.42 / 1.43, 0.81 / 0.71–0.86, 0.51 / 0.51–0.52 s | 1.75 / 1.67, 0.88 / 0.89, 0.40 / 0.52 s |
| | reads / temp folder peak / output | 39.0 MiB / 4.2–6.5 MiB / 24 MiB | 39.0 MiB / 3.9–4.9 MiB / 24 MiB | 38.9–39.0 MiB / 94–100 MiB / 25 MiB |
| 4K transcode of R09, 18 s | first segment | 3.44 / 1.89 / 1.30 s | 7.8 / 4.1 / 2.9 s | out of memory on small and average; 4.36 s on large |
| | CPU / memory | 26.6 / 26.3 / 33.3 s / 291 / 292 / 426 MiB | 61 / 61 / 68 s / 922 / 922 / 1142 MiB | 90.7 s / 2602 MiB on large |
| | reads / output | 47.5 MiB / 35 MiB | 48.9–49.3 MiB / 28 MiB | 50.8 MiB on large |

Startup, idle, the API, Direct Stream and remux do not encode and match the eighth run.

- **Against `libx264`:** transcoding takes 46–47 % of the CPU and of the memory on R06, and 43–49 % of the CPU and 32–37 % of the memory on 4K, whose first segment comes 2.2–2.3 times sooner and whose output is a quarter larger (35 against 28 MiB, within the same cap). What CPU remains is mostly software HEVC decoding.
- **Against Emby's budgets:** CPU, memory and the first segment are met on every profile, and the seek on small and average. The seek to 50 % on large is 0.41 against 0.40 s, the one transcode budget still over, by 0.01 s. 4K starts on every profile, where Emby runs out of memory on two.
- **Picture quality:** FFmpeg alone in the same image, the first 120 s of R06, both encoders with Couch's options, a keyframe every 3 s and the cap at the source's 1.35 Mbit/s, against the source, on 4 CPUs:

  | Encoder | Wall time | CPU | Peak RSS | Output | SSIM (All) | PSNR Y |
  |---|---|---|---|---|---|---|
  | `libx264` | 8.2 s | 32.5 s | 313 MiB | 17.6 MiB | 0.9789 | 40.27 dB |
  | `h264_vaapi` | 4.2 s | 14.1 s | 177 MiB | 17.9 MiB | 0.9765 | 39.10 dB |

  VA-API is 0.002 SSIM and 1.2 dB PSNR below `libx264` at the same bitrate cap, within the 0.01 SSIM condition of the budgets.

## HDR tone mapping (2026-11-03, #125)

FFmpeg alone in the release image built with `--build-arg VAAPI=1` (FFmpeg 8.1, Mesa's VA-API and Vulkan drivers), the first 18 s of R09 (HEVC Main 10 HDR10 3840×2076) at the source size, video only, with the filters and encoder options Couch builds, `-threads` and `-filter_threads` at the CPU count, `--memory 4g`, the render node passed in. Times and peak RSS from GNU time; each output's transfer checked with FFprobe (`smpte2084` without tone mapping, `bt709` with it). One run each.

| Encoder, tone mapping | 2 CPUs: wall / CPU / peak RSS | 4 CPUs: wall / CPU / peak RSS |
|---|---|---|
| `h264_vaapi`, off | 13.1 s / 26.0 s / 364 MiB | 8.3 s / 32.5 s / 499 MiB |
| `h264_vaapi`, libplacebo (Vulkan) | 15.6 s / 30.8 s / 456 MiB | 11.0 s / 40.3 s / 566 MiB |
| `libx264`, off | 28.8 s / 57.2 s / 945 MiB | 18.9 s / 63.5 s / 1164 MiB |
| `libx264`, `zscale` + `tonemap=mobius` | 55.7 s / 111.2 s / 1254 MiB | 36.1 s / 143.2 s / 1478 MiB |

- **GPU:** libplacebo adds 19–33 % wall time and 18–24 % CPU to a VA-API encode, and 92 / 67 MiB, and stays faster than real time on 2 CPUs. The frames go to the GPU and back for tone mapping, so decoding and the VA-API upload stay where they were. In Emby Web the whole job (with its AAC audio, no CPU limit, 16 CPUs) peaked at about 1 GiB.
- **CPU:** `zscale` doubles a `libx264` encode and adds about 300 MiB: 0.32× real time on 2 CPUs and 0.5× on 4, so 4K HDR stalls in playback. 1080p was not measured in the image.
- **VA-API's own tone mapping** (`tonemap_vaapi`) fails on the Radeon 780M (`VAAPI driver doesn't support HDR`), and the host has no OpenCL platform for `tonemap_opencl`; libplacebo is the GPU path that works here.
- **The reference Emby** does not tone-map with its settings on the reference NAS: its H.264 transcodes of an HDR10 and a Dolby Vision 8.1 item kept `smpte2084`/`bt2020` ([VALIDATION](VALIDATION.md), "HDR reference capture"). Its cost was not measured.

## Not measured yet

- Picture quality of the two encoders.
- More runs for variance: five Couch runs and two Emby runs so far; Couch's two runs here agree within 0.12 s on the transcode first segment on small and 0.1 s or 8 MiB elsewhere.
- A real NAS, where the read volumes above turn into time, and Emby's internet metadata, which the offline network rules out.
