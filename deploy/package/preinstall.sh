#!/bin/sh
# Creates the couch system user before the package's files are unpacked.
set -e

if ! getent passwd couch >/dev/null; then
	useradd --system --user-group --home-dir /var/lib/couch --no-create-home --shell /usr/sbin/nologin couch
fi
