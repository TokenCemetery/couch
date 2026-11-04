# Resource baseline: Emby and Couch (#14)

Current figures that the [ROADMAP](ROADMAP.md) budgets compare against. Earlier runs are in git history.

## Method

`scripts/baseline.sh emby|couch small|average|large MEDIA_DIR OUT` starts one server fresh in a resource-limited container and appends one JSON line per phase.

- **Profiles:** small 1 CPU / 1 GiB, average 2 / 2, large 4 / 4 (`--cpus`, `--memory`, swap off).
- **Isolation:** internal Docker network with no route out. Emby keeps its defaults, minus internet metadata, real-time monitoring and missing-episode import.
- **Client:** every PlaybackInfo sends Emby Web's Chromium profile ([fixture](fixtures/emby-4.10.0.40/deviceprofile-chromium.json)).
- **Media:** the 18 real files in [COMPATIBILITY](COMPATIBILITY.md). R04 (MP4 H.264/AAC, 503 MiB) for Direct Stream, R03 (MKV H.264 1080p, AC3 5.1) for remux, R06 (MP4 HEVC 720p, AC3) for transcode, R09 (MKV HEVC 10-bit HDR10 3840×2076) for 4K.
- **Cache:** media evicted from the page cache before each reading phase.
- **Phases:** startup and scan; 60 s idle; 100 requests to each of seven endpoints; full Direct Stream download; HLS first segment, then segments back to back to 120 s of video (18 s for 4K); seeks (five 1 MiB ranges in the direct stream; new HLS session to 50 % and 75 %).
- **Measures, whole container:** wall time, CPU (`cpu.stat`), peak anonymous memory (`memory.stat`, 0.2 s samples), disk I/O (`io.stat`), transcode folder peak. Also the server process alone (CPU, `RssAnon`) against the sum of FFmpeg/FFprobe processes.

Bench: AMD Ryzen 7 255 (16 threads), 22 GiB, Linux 7.0, Docker 29.9, cgroup v2, NVMe. Emby `emby/embyserver:4.10.0.40` (FFmpeg 5.1); Couch release image (Alpine FFmpeg 8.1), defaults. Nothing else ran on the host.

## Results (2026-11-01)

Couch: eighth run, two runs per profile, with transcode threads at the CPU quota and remux keyframes from the container index. Emby: sixth run. Small / average / large where they differ.

|Phase|Measure|Couch|Emby|
|---|---|---|---|
|Startup + scan of 18 files|wall / CPU / reads|1.2 s / 0.8–0.9 s / 18.6 MiB|5.1 / 3.5 / 3.4 s / 2.3–2.5 s / 25 MiB|
|Idle 60 s|anon. memory / server RSS / CPU|4 MiB / 11–12 MiB / 0 s|67–70 MiB / 201–205 MiB / 0.13–0.36 s|
|700 API requests|reads|0 MiB|0 MiB|
|Direct Stream of R04|CPU / memory / seek to first byte|0.31–0.34 s / 7 MiB / 0.5–0.7 ms|0.53–0.74 s / 74–88 MiB / 1.9–2.7 ms|
|Remux of R03, 120 s|CPU / memory|2.1–2.3 s / 22 MiB|1.9–2.0 s / 74–88 MiB|
||first segment|0.37 / 0.27 / 0.27 s|0.34 / 0.33 / 0.32 s|
||seek to 50 %|0.21 s|0.22 / 0.20 / 0.20 s|
|Transcode of R06, 120 s|CPU|29.1 / 28.9 / 31.4 s|34.8 / 33.7 / 33.8 s|
||memory|139–163 MiB|368 MiB|
||first segment|1.01–1.13 / 0.65 / 0.41 s|1.39 / 0.79 / 0.47 s|
||seek to 50 %|1.33–1.42 / 0.81 / 0.51 s|1.75 / 0.88 / 0.40 s|
||reads / temp folder peak|39.0 MiB / 3.9–4.9 MiB|38.9–39.0 MiB / 94–100 MiB|
|4K transcode of R09, 18 s|first segment|7.8 / 4.1 / 2.9 s|out of memory / out of memory / 4.4 s|
||CPU / memory|61 / 61 / 68 s; 922 / 922 / 1142 MiB|91 s / 2602 MiB on large|

API p95 is 0.4–2.6 ms for Couch against 0.8–4.8 ms for Emby; every endpoint is at or below Emby's on every profile.

## Why the remaining gaps exist

- **Remux CPU** (+0.2–0.3 s): Alpine's FFmpeg build. With each server's own binary on the same work, Alpine's x264 costs 4 % more CPU and its audio encoders 7–17× the system time. Debian and Ubuntu glibc builds were no cheaper and 3.8× larger, so the image stays on Alpine. Emby encodes remux audio to MP3; with Alpine's build MP3 would cost more than AAC, so Couch keeps AAC.
- **Remux first segment on small, transcode seek on large:** within 0.03–0.11 s of Emby; run-to-run spread is about 0.1 s.
- **Transcode threads:** FFmpeg sizes threads from the host's CPUs, not the container quota. x264 frame threads delay segment 0 by as many frames as there are threads, and extra threads add memory. Couch caps threads at `GOMAXPROCS` (the quota rounded up, at least 2) by default (#92); without a quota, FFmpeg's own count encodes twice as fast.
- **x264 options:** Couch uses Emby's `subme=0:me_range=4:rc_lookahead=10:partitions=none` on `veryfast` CRF 23 (#99). Against plain `veryfast` on R06 this costs a third less CPU and a fifth less memory, within 0.002 SSIM, for a file a third larger.
- **Scan reads:** FFprobe through `fd:` reads in unbounded blocks; `-blocksize 65536` brings MP4 header reads down to what reading by path costs (85 → 18.6 MiB for the set).

## VA-API on the target GPU (#28)

Radeon 780M, image built with `--build-arg VAAPI=1`, render node passed in, one run per profile. The container's figures exclude GPU work and video memory.

|Phase|Measure|Couch VA-API|Couch `libx264`|Emby|
|---|---|---|---|---|
|Transcode of R06, 120 s|CPU|13.5 / 13.2 / 14.7 s|29.1 / 28.9 / 31.4 s|34.8 / 33.7 / 33.8 s|
||memory|65 / 65 / 76 MiB|139–163 MiB|368 MiB|
||first segment|0.61 / 0.41 / 0.41 s|1.01–1.13 / 0.65 / 0.41 s|1.39 / 0.79 / 0.47 s|
||seek to 50 %|0.94 / 0.52 / 0.41 s|1.33–1.42 / 0.81 / 0.51 s|1.75 / 0.88 / 0.40 s|
|4K transcode of R09, 18 s|first segment|3.4 / 1.9 / 1.3 s|7.8 / 4.1 / 2.9 s|4.4 s on large only|
||CPU / memory|27 / 26 / 33 s; 291 / 292 / 426 MiB|61–68 s; 922–1142 MiB|91 s / 2602 MiB|

Every transcode budget is met except the seek on large (0.41 against 0.40 s). Most remaining CPU is software HEVC decoding. Quality, first 120 s of R06 at the source's 1.35 Mbit/s cap: `libx264` SSIM 0.9789, `h264_vaapi` 0.9765, within the 0.01 SSIM condition.

## HDR tone mapping (#125)

FFmpeg alone in the VA-API image, first 18 s of R09 at source size, video only, threads at the CPU count:

|Encoder, tone mapping|2 CPUs: wall / CPU / RSS|4 CPUs: wall / CPU / RSS|
|---|---|---|
|`h264_vaapi`, off|13.1 s / 26.0 s / 364 MiB|8.3 s / 32.5 s / 499 MiB|
|`h264_vaapi`, libplacebo|15.6 s / 30.8 s / 456 MiB|11.0 s / 40.3 s / 566 MiB|
|`libx264`, off|28.8 s / 57.2 s / 945 MiB|18.9 s / 63.5 s / 1164 MiB|
|`libx264`, `zscale` + `mobius`|55.7 s / 111.2 s / 1254 MiB|36.1 s / 143.2 s / 1478 MiB|

libplacebo keeps a VA-API encode faster than real time on 2 CPUs; `zscale` runs 4K at 0.3–0.5× real time, so it stalls in playback. `tonemap_vaapi` fails on the 780M and there is no OpenCL platform. The reference Emby does not tone-map with its settings.

## Not measured

- A NAS, where the read volumes turn into time; Emby with internet metadata.
- More Emby runs for variance (two so far).
