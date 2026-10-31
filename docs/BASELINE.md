# Resource baseline: Emby and Couch (#14)

Status: first run, 2026-10-31. One run per server and profile; run-to-run variance is not measured yet. Requirements: [PRD](PRD.md) G-2; budgets that build on this: #15.

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
  - peak anonymous memory from `memory.stat`, sampled every 0.2 s. `memory.current` would also count the page cache of the media read, which is reclaimable.

  Also the server process's own peak RSS (`VmHWM`).
- **Phases:**
  1. Startup and library scan: Couch scans before it listens; Emby runs its wizard and adds the library with a scan.
  2. 60 s idle.
  3. 100 requests to each of seven API endpoints.
  4. A full Direct Stream download.
  5. HLS: time to the first segment, then segments fetched one after another until 120 s of video (18 s for 4K), as fast as the server delivers. Not real-time pacing.

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

## Findings

- **Idle and API:** Couch idles at about 1/18 of Emby's anonymous memory (4 vs 71 MiB) and about 1/18 of its RSS (11 vs 202 MiB), with no idle CPU. Its API answers are 1.5–7 times faster on the small profile. On two or more CPUs, five endpoints stay 1.3–4.7 times faster, and the movie list and Latest are slightly slower than Emby's.
- **Direct Stream and remux:** the cost is about the same. Couch holds less memory (7 vs 72–89 MiB, 22 vs 86–99 MiB).
- **Transcoding: Couch costs more.**
  - 1.6 times Emby's CPU for the same 120 s of 720p HEVC.
  - Its first segment comes 3 times later.
  - At 4K with 4 CPUs, the first segment comes after 27.8 s instead of 4.4 s, beyond Emby Web's 10 s wait (#97).

  Emby's FFmpeg command shows why. Its preset and CRF match Couch's (`veryfast`, CRF 23), but it adds `-x264opts subme=0:me_range=4:rc_lookahead=10:partitions=none` and makes 3 s segments with a keyframe every 3 s. Those options cut motion search and shorten the lookahead, which also lowers memory at 4K. Picture quality was not compared.
- **4K needs a large container on both servers:** both run out of memory at 1 and 2 GiB, because both transcode at the source size.

## Not measured yet

- I/O.
- Seek time and temp-file size.
- Picture quality of the two encoders.
- Several runs for variance.
- Emby's server process and its FFmpeg separately during transcoding; the table gives the container total, which is their sum.
- A real NAS, and Emby's internet metadata, which the offline network rules out.
