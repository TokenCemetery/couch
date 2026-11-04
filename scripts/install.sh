#!/bin/sh
# Installs or upgrades Couch as a systemd service; see docs/INSTALL.md.
# Usage: sudo scripts/install.sh [BINARY]  (default bin/couch-linux-ARCH for the host,
# built by make release, with its .sha256 next to it).
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

case "$(uname -m)" in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) arch=$(uname -m) ;;
esac
binary=${1:-$root/bin/couch-linux-$arch}

[ "$(id -u)" = 0 ] || fail "run as root, for example with sudo"
[ -f "$binary" ] || fail "$binary not found; build it with make release"
[ -f "$binary.sha256" ] || fail "$binary.sha256 not found"
(cd "$(dirname "$binary")" && sha256sum --check --quiet "$(basename "$binary").sha256") || fail "$binary does not match its SHA-256"
for tool in ffmpeg ffprobe; do
	command -v "$tool" >/dev/null || echo "install.sh: $tool not found, so HLS is disabled; install it with: apt install ffmpeg" >&2
done

if ! getent passwd couch >/dev/null; then
	useradd --system --user-group --home-dir /var/lib/couch --no-create-home --shell /usr/sbin/nologin couch
fi
# The render group opens the GPU for -hwaccel vaapi.
if getent group render >/dev/null; then
	usermod --append --groups render couch
fi
install -d -o couch -g couch -m 0700 /var/lib/couch

# Replaced by rename, so a running server keeps its file until it restarts.
install -m 0755 "$binary" /usr/local/bin/couch.new
mv -f /usr/local/bin/couch.new /usr/local/bin/couch
install -m 0644 "$root/deploy/couch.service" /etc/systemd/system/couch.service
if [ ! -e /etc/default/couch ]; then
	install -m 0600 "$root/deploy/couch.default" /etc/default/couch
fi
systemctl daemon-reload

if systemctl is-active --quiet couch; then
	systemctl restart couch
	echo "Couch upgraded and restarted."
else
	echo "Couch installed. Set the options in /etc/default/couch, create the first user, then run: systemctl enable --now couch"
fi
