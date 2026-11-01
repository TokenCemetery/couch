#!/usr/bin/env bash
# Measures one server (Emby or Couch) in a resource-limited container on the
# real media set and appends one JSON line per phase to OUT (#14).
#
#   scripts/baseline.sh emby|couch small|average|large MEDIA_DIR OUT
#
# MEDIA_DIR holds the set from docs/COMPATIBILITY.md ("Real library files"):
# R03 (H.264 remux), R04 (Direct Stream), R06 (HEVC transcode), R09 (4K).
# Both servers run on the internal Docker network couch-bench, which has no
# route out, so nothing about the media leaves the host. Before each phase
# that reads media, its files are evicted from the page cache (dd
# iflag=nocache), so reads come from the disk as on a fresh server. Needs docker, curl,
# jq and cgroup v2 with the systemd driver. COUCH_IMAGE defaults to couch:dev;
# COUCH_ARGS adds flags to the Couch server, for example "-ffmpeg-threads 2";
# COUCH_DOCKER_ARGS adds options to its container, for example the GPU for
# "-hwaccel vaapi": "--device /dev/dri/renderD128 --group-add GID" with the
# host GID of the render group.
set -euo pipefail

server=$1 profile=$2 media=$(realpath "$3") out=$4
case $profile in
  small) cpus=1 mem=1g ;;
  average) cpus=2 mem=2g ;;
  large) cpus=4 mem=4g ;;
  *) echo "profile must be small, average or large" >&2; exit 2 ;;
esac
emby_image=emby/embyserver:4.10.0.40
couch_image=${COUCH_IMAGE:-couch:dev}
name=bench-$server
here=$(dirname "$(realpath "$0")")
device_profile="$here/../docs/fixtures/emby-4.10.0.40/deviceprofile-chromium.json"
work=$(mktemp -d)
chmod 700 "$work"
password=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')
auth='MediaBrowser Client="baseline", Device="baseline", DeviceId="baseline", Version="1"'
sampler=""

cleanup() {
  [ -n "$sampler" ] && kill "$sampler" 2>/dev/null || true
  docker rm -f "$name" >/dev/null 2>&1 || true
  # KEEP=1 leaves the server's state (and Emby's FFmpeg logs) for inspection.
  if [ -n "${KEEP:-}" ]; then echo "state kept in $work" >&2; else rm -rf "$work"; fi
}
trap cleanup EXIT

docker network inspect couch-bench >/dev/null 2>&1 || docker network create --internal couch-bench >/dev/null
docker rm -f "$name" >/dev/null 2>&1 || true

record() { jq -nc --arg server "$server" --arg profile "$profile" "$@" >> "$out"; }
cgroup() { echo "/sys/fs/cgroup/system.slice/docker-$(docker inspect -f '{{.Id}}' "$name").scope"; }
cpu_usec() { awk '$1=="usage_usec"{print $2}' "$(cgroup)/cpu.stat"; }
# io_bytes prints "read written" summed over the container's block devices.
io_bytes() { awk '{for (i = 2; i <= NF; i++) { split($i, kv, "="); if (kv[1] == "rbytes") r += kv[2]; if (kv[1] == "wbytes") w += kv[2] } } END { print r + 0, w + 0 }' "$(cgroup)/io.stat"; }
# server_pid prints the server process's host PID, if it has started.
server_pid() {
  local p
  for p in $(cat "$1/cgroup.procs"); do
    [ "$(cat "/proc/$p/comm" 2>/dev/null)" = "$process" ] && { echo "$p"; return; }
  done
  return 0
}
# proc_cpu_usec PID: the process's own user and system CPU, without children.
proc_cpu_usec() { awk -v hz="$(getconf CLK_TCK)" '{sub(/.*\) /, ""); print ($12 + $13) * 1e6 / hz}' "/proc/$1/stat" 2>/dev/null || echo 0; }
evict() { for f in "$@"; do dd if="$media/$f" iflag=nocache count=0 status=none; done; }
evict_all() { find "$media" -type f -print0 | while IFS= read -r -d '' f; do dd if="$f" iflag=nocache count=0 status=none; done; }
now() { date +%s.%N; }

# phase_begin and phase_end bracket a phase: container CPU from cpu.stat and
# the peak of anonymous memory from memory.stat, sampled every 0.2 s (server
# and FFmpeg). memory.current would also count the page cache of the media
# being read, which is reclaimable and not process memory. The server process
# is also measured on its own: its CPU from /proc, so the rest of the
# container's CPU is FFmpeg and FFprobe, and its anonymous memory (RssAnon)
# next to the sum over the ffmpeg and ffprobe processes running at once.
phase_begin() {
  phase=$1 t0=$(now) c0=$(cpu_usec)
  read -r r0 w0 < <(io_bytes)
  local cg; cg=$(cgroup)
  local file=$cg/memory.stat pid
  pid=$(server_pid "$cg"); s0=0; [ -n "$pid" ] && s0=$(proc_cpu_usec "$pid")
  : > "$work/peak"; : > "$work/temp"; : > "$work/srv"; : > "$work/ff"
  ( max=0 tmax=0 smax=0 fmax=0
    while :; do
      v=$(awk '$1=="anon"{print $2}' "$file" 2>/dev/null || echo 0)
      [ "${v:-0}" -gt "$max" ] && max=$v && echo "$max" > "$work/peak"
      s=0 f=0
      for p in $(cat "$cg/cgroup.procs" 2>/dev/null); do
        c=$(cat "/proc/$p/comm" 2>/dev/null) || continue
        k=$(awk '/^RssAnon/{print $2}' "/proc/$p/status" 2>/dev/null)
        case $c in
          "$process") s=${k:-0} ;;
          ffmpeg | ffprobe) f=$((f + ${k:-0})) ;;
        esac
      done
      [ "$s" -gt "$smax" ] && smax=$s && echo "$smax" > "$work/srv"
      [ "$f" -gt "$fmax" ] && fmax=$f && echo "$fmax" > "$work/ff"
      t=$(du -sb "$tempdir" 2>/dev/null | cut -f1)
      [ "${t:-0}" -gt "$tmax" ] && tmax=$t && echo "$tmax" > "$work/temp"
      sleep 0.2
    done ) &
  sampler=$!
}
phase_end() {
  local extra=${1:-'{}'}
  kill "$sampler" 2>/dev/null || true; wait "$sampler" 2>/dev/null || true; sampler=""
  local t1 c1 peak temp r1 w1 pid s1=0 srv ff; t1=$(now); c1=$(cpu_usec); peak=$(cat "$work/peak" 2>/dev/null); temp=$(cat "$work/temp" 2>/dev/null)
  pid=$(server_pid "$(cgroup)"); [ -n "$pid" ] && s1=$(proc_cpu_usec "$pid")
  srv=$(cat "$work/srv" 2>/dev/null); ff=$(cat "$work/ff" 2>/dev/null)
  read -r r1 w1 < <(io_bytes)
  record --arg phase "$phase" --argjson wall "$(awk "BEGIN{print $t1-$t0}")" \
    --argjson cpu "$(awk "BEGIN{print ($c1-$c0)/1e6}")" --argjson peak "$(awk "BEGIN{print ${peak:-0}/1048576}")" \
    --argjson read "$(awk "BEGIN{print ($r1-$r0)/1048576}")" --argjson written "$(awk "BEGIN{print ($w1-$w0)/1048576}")" \
    --argjson temp "$(awk "BEGIN{print ${temp:-0}/1048576}")" --argjson extra "$extra" \
    --argjson scpu "$(awk "BEGIN{print ($s1-$s0)/1e6}")" --argjson srv "$(awk "BEGIN{print ${srv:-0}/1024}")" --argjson ff "$(awk "BEGIN{print ${ff:-0}/1024}")" \
    '{server: $server, profile: $profile, phase: $phase, wall_s: ($wall*100|round/100), cpu_s: ($cpu*100|round/100), anon_peak_mib: ($peak|round),
      server_cpu_s: ($scpu*100|round/100), ffmpeg_cpu_s: (($cpu-$scpu)*100|round/100), server_anon_peak_mib: ($srv|round), ffmpeg_anon_peak_mib: ($ff|round),
      read_mib: ($read*10|round/10), written_mib: ($written*10|round/10), temp_peak_mib: ($temp*10|round/10)} + $extra'
}
alive() { [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = true ]; }
died() {
  record --arg phase "$1" --arg oom "$(docker inspect -f '{{.State.OOMKilled}}' "$name")" '{server: $server, profile: $profile, phase: $phase, died: true, oom_killed: ($oom=="true")}'
  exit 0
}
# The server process alone: its peak RSS over the run, from the host's /proc.
server_hwm_mib() {
  local pid
  pid=$(docker top "$name" -eo pid,comm | awk -v c="$1" '$2==c{print $1; exit}')
  awk '/^VmHWM/{printf "%d", $2/1024}' "/proc/$pid/status"
}

api() { curl -sf -H "X-Emby-Authorization: $auth" -H "X-Emby-Token: $token" "$@"; }
login() {
  token=$(jq -n --arg p "$password" '{Username: "viewer", Pw: $p}' |
    curl -sf -H "X-Emby-Authorization: $auth" -H 'Content-Type: application/json' -d @- "$base/Users/AuthenticateByName" | jq -r .AccessToken)
  uid=$(api "$base/Users/Me" 2>/dev/null | jq -r .Id || true)
  [ -n "$uid" ] && [ "$uid" != null ] || uid=$(api "$base/Users" | jq -r '.[0].Id')
}
wait_url() { for _ in $(seq 600); do curl -sf -m 2 "$1" >/dev/null && return 0; alive || return 1; sleep 0.5; done; return 1; }

# --- startup and scan -------------------------------------------------------
case $server in
emby)
  mkdir -p "$work/config"
  docker create --name "$name" --network couch-bench --cpus "$cpus" --memory "$mem" --memory-swap "$mem" \
    -e UID="$(id -u)" -e GID="$(id -g)" -e GIDLIST="$(id -g)" \
    -v "$work/config:/config" -v "$media:/media:ro" "$emby_image" >/dev/null
  tempdir=$work/config/transcoding-temp
  process=EmbyServer
  evict_all
  docker start "$name" >/dev/null
  phase_begin startup_scan
  ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name")
  base=http://$ip:8096/emby
  wait_url "$base/System/Info/Public" || died startup_scan
  h=(-H "X-Emby-Authorization: $auth" -H 'Content-Type: application/json')
  curl -sf "${h[@]}" -d '{"UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}' "$base/Startup/Configuration" >/dev/null
  jq -n --arg p "$password" '{Name: "viewer", Password: $p}' | curl -sf "${h[@]}" -d @- "$base/Startup/User" >/dev/null
  curl -sf "${h[@]}" -d '{"EnableAutomaticPortMapping":false}' "$base/Startup/RemoteAccess" >/dev/null
  curl -sf "${h[@]}" -X POST "$base/Startup/Complete" >/dev/null
  login
  # Emby's defaults, except internet metadata: the network is offline anyway,
  # and lookups would only add timeouts to the scan.
  api -H 'Content-Type: application/json' -d '{"LibraryOptions":{"PathInfos":[{"Path":"/media"}],"EnableInternetProviders":false,"DownloadImagesInAdvance":false,"ImportMissingEpisodes":false,"EnableRealtimeMonitor":false}}' \
    "$base/Library/VirtualFolders?Name=Media&RefreshLibrary=true" >/dev/null
  sleep 2
  for _ in $(seq 1800); do
    state=$(api "$base/ScheduledTasks?IsHidden=false" | jq -r '.[] | select(.Key=="RefreshLibrary") | .State')
    [ "$state" = Idle ] && break; alive || died startup_scan; sleep 0.5
  done
  ;;
couch)
  mkdir -p "$work/data"
  printf '%s\n' "$password" | docker run --rm -i --user "$(id -u):$(id -g)" -v "$work/data:/data" "$couch_image" -init -username viewer >/dev/null
  docker create --name "$name" --network couch-bench --cpus "$cpus" --memory "$mem" --memory-swap "$mem" \
    --user "$(id -u):$(id -g)" ${COUCH_DOCKER_ARGS:-} -v "$work/data:/data" -v "$media:/media:ro" "$couch_image" -listen :8097 -media-dir /media ${COUCH_ARGS:-} >/dev/null
  tempdir=$work/data/transcode
  process=couch
  evict_all
  docker start "$name" >/dev/null
  phase_begin startup_scan
  ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name")
  base=http://$ip:8097/emby
  # The scan finishes before Couch listens.
  wait_url "http://$ip:8097/healthz" || died startup_scan
  login
  ;;
*) echo "server must be emby or couch" >&2; exit 2 ;;
esac
items=$(api "$base/Users/$uid/Items?Recursive=true&IncludeItemTypes=Movie,Episode")
phase_end "$(jq -c '{items: (.Items|length)}' <<<"$items")"
item_id() { jq -r --arg n "$1" '.Items[] | select(.Name==$n) | .Id' <<<"$items"; }

# --- idle -------------------------------------------------------------------
phase_begin idle
sleep 60
phase_end "{\"server_rss_hwm_mib\": $(server_hwm_mib "$process")}"

# --- API latency ------------------------------------------------------------
r03=$(item_id R03)
declare -A endpoints=(
  [views]="$base/Users/$uid/Views"
  [latest]="$base/Users/$uid/Items/Latest?Limit=16"
  [resume]="$base/Users/$uid/Items/Resume?Limit=12"
  [movies]="$base/Users/$uid/Items?Recursive=true&IncludeItemTypes=Movie&SortBy=SortName&Limit=50&Fields=PrimaryImageAspectRatio,BasicSyncInfo"
  [item]="$base/Users/$uid/Items/$r03"
  [search]="$base/Users/$uid/Items?SearchTerm=R1&Recursive=true&Limit=24"
)
phase_begin api
latency='{}'
percentiles() { sort -n | awk '{a[NR]=$1} END{printf "{\"p50_ms\": %.1f, \"p95_ms\": %.1f}", a[int(NR*0.5)+1]*1000, a[int(NR*0.95)]*1000}'; }
for key in "${!endpoints[@]}"; do
  p=$(for _ in $(seq 100); do api -o /dev/null -w '%{time_total}\n' "${endpoints[$key]}"; done | percentiles)
  latency=$(jq -c --arg k "$key" --argjson p "$p" '. + {($k): $p}' <<<"$latency")
done
p=$(for _ in $(seq 100); do
  jq -c '{DeviceProfile: .}' "$device_profile" | api -o /dev/null -w '%{time_total}\n' -H 'Content-Type: application/json' -d @- "$base/Items/$r03/PlaybackInfo?UserId=$uid&IsPlayback=false"
done | percentiles)
latency=$(jq -c --argjson p "$p" '. + {playbackinfo: $p}' <<<"$latency")
phase_end "$latency"
alive || died api

# playback_info ITEM prints the server's choice for the Chromium profile.
playback_info() {
  jq -c '{DeviceProfile: .}' "$device_profile" |
    api -H 'Content-Type: application/json' -d @- "$base/Items/$1/PlaybackInfo?UserId=$uid&IsPlayback=true&AutoOpenLiveStream=true&MaxStreamingBitrate=200000000&StartTimeTicks=0"
}

# --- Direct Stream ----------------------------------------------------------
info=$(playback_info "$(item_id R04)")
url=$(jq -r '.MediaSources[0].DirectStreamUrl // empty' <<<"$info")
evict R04.mp4
phase_begin direct_stream
if [ -n "$url" ]; then
  # Both servers return the URL without the /emby prefix, as Emby Web expects.
  bytes=$(curl -sf -o /dev/null -w '%{size_download}' "$base$url")
  phase_end "{\"delivery\": \"direct\", \"mib\": $((bytes / 1048576))}"
else
  phase_end "{\"delivery\": \"$(jq -r 'if .MediaSources[0].TranscodingUrl then "hls" else (.ErrorCode // "none") end' <<<"$info")\"}"
fi
alive || died direct_stream

# --- seek in Direct Stream ----------------------------------------------------
# A player seeks in a direct stream with a byte range; time to its first byte.
if [ -n "$url" ]; then
  size=$(jq -r '.MediaSources[0].Size' <<<"$info")
  evict R04.mp4
  phase_begin seek_direct
  ttfb=$(for f in 0.1 0.3 0.5 0.7 0.9; do
    off=$(awk "BEGIN{printf \"%d\", $size*$f}")
    curl -sf -o /dev/null -r "$off-$((off + 1048575))" -w '%{time_starttransfer}\n' "$base$url"
  done | sort -n | awk '{a[NR]=$1} END{printf "%.1f", a[3]*1000}')
  phase_end "{\"seek_ttfb_median_ms\": $ttfb}"
  alive || died seek_direct
fi

# hls ITEM SECONDS: time to the first segment, then the following segments
# one after another as fast as they come, until SECONDS of video (by the
# playlist's EXTINF) are fetched; the encoding is stopped afterwards. Servers
# cut segments of different lengths, so the amount is measured in seconds.
hls() {
  local start info url session master dir main n=0 bytes=0 got ttfs="" seconds=0 mode=remux reasons
  start=$(now)
  info=$(playback_info "$1")
  url=$(jq -r '.MediaSources[0].TranscodingUrl // empty' <<<"$info")
  session=$(jq -r '.PlaySessionId' <<<"$info")
  if [ -z "$url" ]; then
    echo "{\"delivery\": \"$(jq -r 'if .MediaSources[0].DirectStreamUrl then "direct" else (.ErrorCode // "none") end' <<<"$info")\"}"
    return
  fi
  # Emby names why it transcodes; Couch encodes the video only when the URL
  # sets VideoBitrate (Emby sets it on every HLS URL).
  reasons=$(grep -o 'TranscodeReasons=[^&]*' <<<"$url" | cut -d= -f2 | sed 's/%2C/,/gI')
  if [ -n "$reasons" ]; then
    grep -q VideoCodecNotSupported <<<"$reasons" && mode=transcode
  else
    grep -qi 'VideoBitrate=' <<<"$url" && mode=transcode
  fi
  master=$(curl -sf -m 300 "$base$url")
  dir=${url%%\?*}; dir=$base${dir%/*}
  main=$dir/$(grep -v '^#' <<<"$master" | head -1)
  while read -r dur s; do
    got=$(curl -sf -m 300 -o /dev/null -w '%{size_download}' "$dir/$s") || break
    bytes=$((bytes + got)); n=$((n + 1)); seconds=$(awk "BEGIN{print $seconds+$dur}")
    [ -z "$ttfs" ] && ttfs=$(awk "BEGIN{printf \"%.2f\", $(now)-$start}")
    awk "BEGIN{exit !($seconds >= $2)}" && break
  done < <(curl -sf -m 300 "$main" | awk -F'[:,]' '/^#EXTINF/{d=$2; next} !/^#/ && NF {print d, $0}')
  api -X POST -o /dev/null "$base/Videos/ActiveEncodings/Delete?DeviceId=baseline&PlaySessionId=$session" || true
  echo "{\"delivery\": \"$mode\", \"reasons\": \"$reasons\", \"segments\": $n, \"video_s\": $(awk "BEGIN{printf \"%.1f\", $seconds}"), \"mib\": $((bytes / 1048576)), \"first_segment_s\": ${ttfs:-null}}"
}

# hls_seek ITEM: starts a session and fetches the first segment, then seeks
# as a player does, by requesting the segment at 50 % and later at 75 % of the
# playlist; each seek is timed to its segment, and the segment after it is
# fetched to check playback continues. The server restarts its encoding there.
hls_seek() {
  local info url session master dir main durs=() segs=() total=0 k cum seek50 seek75 t frac
  info=$(playback_info "$1")
  url=$(jq -r '.MediaSources[0].TranscodingUrl // empty' <<<"$info")
  session=$(jq -r '.PlaySessionId' <<<"$info")
  [ -n "$url" ] || { echo '{"delivery": "not hls"}'; return; }
  master=$(curl -sf -m 300 "$base$url")
  dir=${url%%\?*}; dir=$base${dir%/*}
  main=$dir/$(grep -v '^#' <<<"$master" | head -1)
  while read -r d seg; do durs+=("$d"); segs+=("$seg"); done < <(curl -sf -m 300 "$main" | awk -F'[:,]' '/^#EXTINF/{d=$2; next} !/^#/ && NF {print d, $0}')
  total=$(printf '%s\n' "${durs[@]}" | awk '{s+=$1} END{print s}')
  curl -sf -m 300 -o /dev/null "$dir/${segs[0]}"
  for frac in 0.5 0.75; do
    cum=0; k=0
    while [ "$k" -lt $((${#segs[@]} - 1)) ] && awk "BEGIN{exit !($cum < $total*$frac)}"; do cum=$(awk "BEGIN{print $cum+${durs[$k]}}"); k=$((k + 1)); done
    t=$(curl -sf -m 300 -o /dev/null -w '%{time_total}' "$dir/${segs[$k]}" || echo null)
    curl -sf -m 300 -o /dev/null "$dir/${segs[$((k + 1))]}" || t=null
    [ "$frac" = 0.5 ] && seek50=$t || seek75=$t
  done
  api -X POST -o /dev/null "$base/Videos/ActiveEncodings/Delete?DeviceId=baseline&PlaySessionId=$session" || true
  echo "{\"seek_50_s\": $seek50, \"seek_75_s\": $seek75}"
}

evict R03.mkv
phase_begin remux_h264
phase_end "$(hls "$(item_id R03)" 120)"
alive || died remux_h264
evict R06.mp4
phase_begin transcode_hevc_720p
phase_end "$(hls "$(item_id R06)" 120)"
alive || died transcode_hevc_720p
evict R03.mkv
phase_begin seek_remux_h264
phase_end "$(hls_seek "$(item_id R03)")"
alive || died seek_remux_h264
evict R06.mp4
phase_begin seek_transcode_hevc_720p
phase_end "$(hls_seek "$(item_id R06)")"
alive || died seek_transcode_hevc_720p
evict R09.mkv
phase_begin transcode_hevc_4k
phase_end "$(hls "$(item_id R09)" 18)"
alive || died transcode_hevc_4k

phase_begin end
phase_end "{\"server_rss_hwm_mib\": $(server_hwm_mib "$process")}"
