#!/bin/sh
# Prepares the state directory and restarts a running service after an upgrade.
set -e

# The render group opens the GPU for -hwaccel vaapi.
if getent group render >/dev/null; then
	usermod --append --groups render couch
fi
install -d -o couch -g couch -m 0700 /var/lib/couch

if [ -e /etc/systemd/system/couch.service ]; then
	echo "Warning: /etc/systemd/system/couch.service from scripts/install.sh overrides the package's unit and starts /usr/local/bin/couch; remove both, then run: systemctl daemon-reload" >&2
fi
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	if systemctl is-active --quiet couch; then
		systemctl restart couch
		echo "Couch upgraded and restarted."
	else
		echo "Couch installed. Set the options in /etc/default/couch, create the first user, then run: systemctl enable --now couch"
	fi
fi
