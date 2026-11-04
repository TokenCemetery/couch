#!/bin/sh
# Forgets the removed unit. The state in /var/lib/couch and the couch user stay.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
fi
