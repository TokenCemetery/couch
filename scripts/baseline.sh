#!/usr/bin/env bash
# Measures one server (Emby or Couch) in a resource-limited container on the
# real media set and appends one JSON line per phase to OUT (#14).
#
#   scripts/baseline.sh emby|couch small|average|large MEDIA_DIR OUT
#
# MEDIA_DIR holds the set from docs/COMPATIBILITY.md ("Real library files"):
# R03 (H.264 remux), R04 (Direct Stream), R06 (HEVC transcode), R09 (4K).
# Both servers run on the internal Docker network couch-bench, which has no
# route out, so nothing about the media leaves the host. Needs docker, curl,
# jq and cgroup v2 with the systemd driver. COUCH_IMAGE defaults to couch:dev.
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
now() { date +%s.%N; }

# phase_begin and phase_end bracket a phase: container CPU from cpu.stat and
# the peak of anonymous memory from memory.stat, sampled every 0.2 s (server
# and FFmpeg). memory.current would also count the page cache of the media
# being read, which is reclaimable and not process memory.
phase_begin() {
  phase=$1 t0=$(now) c0=$(cpu_usec)
  local file=$(cgroup)/memory.stat
  : > "$work/peak"
  ( max=0; while :; do v=$(awk '$1=="anon"{print $2}' "$file" 2>/dev/null || echo 0); [ "${v:-0}" -gt "$max" ] && max=$v && echo "$max" > "$work/peak"; sleep 0.2; done ) &
  sampler=$!
}
phase_end() {
  local extra=${1:-'{}'}
  kill "$sampler" 2>/dev/null || true; wait "$sampler" 2>/dev/null || true; sampler=""
  local t1 c1 peak; t1=$(now); c1=$(cpu_usec); peak=$(cat "$work/peak" 2>/dev/null || echo 0)
  record --arg phase "$phase" --argjson wall "$(awk "BEGIN{print $t1-$t0}")" \
    --argjson cpu "$(awk "BEGIN{print ($c1-$c0)/1e6}")" --argjson peak "$(awk "BEGIN{print ${peak:-0}/1048576}")" \
    --argjson extra "$extra" \
    '{server: $server, profile: $profile, phase: $phase, wall_s: ($wall*100|round/100), cpu_s: ($cpu*100|round/100), anon_peak_mib: ($peak|round)} + $extra'
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
  process=EmbyServer
  ;;
couch)
  mkdir -p "$work/data"
  printf '%s\n' "$password" | docker run --rm -i --user "$(id -u):$(id -g)" -v "$work/data:/data" "$couch_image" -init -username viewer >/dev/null
  docker create --name "$name" --network couch-bench --cpus "$cpus" --memory "$mem" --memory-swap "$mem" \
    --user "$(id -u):$(id -g)" -v "$work/data:/data" -v "$media:/media:ro" "$couch_image" -listen :8097 -media-dir /media >/dev/null
  docker start "$name" >/dev/null
  phase_begin startup_scan
  ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name")
  base=http://$ip:8097/emby
  # The scan finishes before Couch listens.
  wait_url "http://$ip:8097/healthz" || died startup_scan
  login
  process=couch
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
phase_begin direct_stream
if [ -n "$url" ]; then
  # Both servers return the URL without the /emby prefix, as Emby Web expects.
  bytes=$(curl -sf -o /dev/null -w '%{size_download}' "$base$url")
  phase_end "{\"delivery\": \"direct\", \"mib\": $((bytes / 1048576))}"
else
  phase_end "{\"delivery\": \"$(jq -r 'if .MediaSources[0].TranscodingUrl then "hls" else (.ErrorCode // "none") end' <<<"$info")\"}"
fi
alive || died direct_stream

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

phase_begin remux_h264
phase_end "$(hls "$(item_id R03)" 120)"
alive || died remux_h264
phase_begin transcode_hevc_720p
phase_end "$(hls "$(item_id R06)" 120)"
alive || died transcode_hevc_720p
phase_begin transcode_hevc_4k
phase_end "$(hls "$(item_id R09)" 18)"
alive || died transcode_hevc_4k

phase_begin end
phase_end "{\"server_rss_hwm_mib\": $(server_hwm_mib "$process")}"
