#!/bin/sh
# Stops the service when the package is removed, not when it is upgraded:
# deb passes "remove", rpm passes 0 when no version stays installed.
set -e

case "$1" in
	remove | 0) ;;
	*) exit 0 ;;
esac
if [ -d /run/systemd/system ]; then
	systemctl disable --now couch || true
fi
