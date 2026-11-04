# Running Couch as a systemd service

The container ([README](../README.md#quick-start-docker-compose)) is the main way to run Couch. This manual covers the alternative: the static binary as a systemd service on Linux amd64 or arm64, written for Ubuntu 26.04. The unit is verified with `systemd-analyze`, but the install has not yet been run on the target host ([VALIDATION](VALIDATION.md#install-114-117), #117). Couch serves plain HTTP on port 8097 for the LAN; TLS and a reverse proxy are not covered.

## What gets installed

`scripts/install.sh` sets up:

- `/usr/local/bin/couch`: the binary, checked against its SHA-256 first;
- the system user and group `couch`, also added to the `render` group for `-hwaccel vaapi`;
- `/var/lib/couch`: the state (`couch.db` with the catalog index, logs), mode 0700, owned by `couch`;
- `/etc/systemd/system/couch.service`: the unit from [deploy/couch.service](../deploy/couch.service). The service runs as `couch` and can write only `/var/lib/couch`. It sees the rest of the system and `/home` read-only, gets no capabilities and no devices besides `/dev/dri/renderD128`, and restarts after a crash or `SIGKILL`;
- `/etc/default/couch`: the options, from [deploy/couch.default](../deploy/couch.default). An existing file is kept.

FFmpeg and FFprobe come from the distribution (Ubuntu 26.04: FFmpeg 8.0.1, with the `fd` protocol Couch needs). Without them Couch runs, but HLS remux and transcoding are disabled. For `-hwaccel vaapi` on AMD, Ubuntu 26.04 ships Mesa's VA-API driver in `mesa-libgallium`, which is installed with the desktop.

```sh
sudo apt install ffmpeg
```

## Install

The [GitHub releases](https://github.com/TokenCemetery/couch/releases) carry deb and rpm packages and archives for amd64 and arm64, with `SHA256SUMS.txt`. The packages install the binary as `/usr/bin/couch` and the unit as `/usr/lib/systemd/system/couch.service`; the rest is as listed above. They recommend FFmpeg, which on Fedora comes from RPM Fusion:

```sh
sha256sum --check --ignore-missing SHA256SUMS.txt
sudo apt install ./couch_0.2.0_amd64.deb     # Debian, Ubuntu
sudo dnf install ./couch-0.2.0-1.x86_64.rpm  # Fedora, RHEL
```

Removing the package stops and disables the service, and keeps `/var/lib/couch`, `/etc/default/couch` and the `couch` user. To switch from `install.sh` to a package, first remove `/etc/systemd/system/couch.service` and `/usr/local/bin/couch`: a unit in `/etc` overrides the package's, and its `ExecStart` would keep starting the old binary.

The archive `couch-VERSION-linux-ARCH.tar.gz` holds the binary with its checksum, `scripts/install.sh` and the files it installs:

```sh
tar -xzf couch-0.2.0-linux-amd64.tar.gz
cd couch-0.2.0-linux-amd64
sudo scripts/install.sh
```

Or build the binaries and their checksums from a checkout on a machine with Go 1.26+ (it can be another one; copy `bin/couch-linux-ARCH` and `bin/couch-linux-ARCH.sha256` to the host next to a checkout of this repository):

```sh
make release
sudo scripts/install.sh
```

`install.sh` takes the binary's path as its argument; the default is `bin/couch-linux-ARCH` for the host's architecture. Set the options in `/etc/default/couch`:

```sh
COUCH_ARGS=-listen :8097 -media-dir /srv/media -web-dir /opt/emby-webui
```

`-data /var/lib/couch` is added by the unit. The values are split at spaces, so paths must not contain any. The other options are listed in the [README](../README.md#configuration). `COUCH_TMDB_API_KEY`, `COUCH_TVDB_API_KEY` and `COUCH_AUDIODB_API_KEY` in the same file turn on online metadata, and the file is readable only by root.

The `couch` user must be able to read the media and Emby Web, including every parent folder. A home folder (mode 0750 on Ubuntu) usually blocks it, so keep them in a folder such as `/srv` or `/opt`, or grant read access with an ACL, for example `sudo setfacl -R -m u:couch:rX /srv/media`.

## First start

Create the first user, the administrator, with the password on standard input, at least 12 bytes. The input is hidden and does not end up in the shell history:

```sh
read -rsp 'New Couch user password: ' couch_password; printf '\n'
printf '%s\n' "$couch_password" | sudo -u couch couch -data /var/lib/couch -init -username viewer
unset couch_password
sudo systemctl enable --now couch
```

The `read` line is for bash; in zsh it is `read -rs 'couch_password?New Couch user password: '`. Then open `http://HOST:8097/` from a device on the LAN and sign in. If the firewall is on: `sudo ufw allow from 192.168.1.0/24 to any port 8097 proto tcp` (with your LAN's network).

## Logs and status

```sh
systemctl status couch
journalctl -u couch -f
```

The same log is in `/var/lib/couch/logs/couch.log` and on Emby Web's logs page. Rescan the libraries: `sudo systemctl reload couch`.

## Upgrade

Take a backup, then install the new package, or unpack the new release archive (or build the new binary) and run the install script again. It replaces the binary and the unit, keeps `/etc/default/couch` and the state, and restarts the running service:

```sh
sudo -u couch couch -data /var/lib/couch -backup /var/lib/couch/backup-$(date +%F)
git pull
make release
sudo scripts/install.sh
```

Couch upgrades the database on its first start and refuses a database from a newer version without changing it. To go back, build and install the previous version, then restore the backup taken before the upgrade.

## Backup and restore

`-backup` writes a consistent copy while Couch runs ([README](../README.md#operations)). It holds password and token hashes, so keep it as private as `/var/lib/couch`:

```sh
sudo -u couch couch -data /var/lib/couch -backup /var/lib/couch/backup-2026-11-01
```

Restore it as the `couch` user, so the files keep their owner:

```sh
sudo systemctl stop couch
sudo -u couch sh -c 'cd /var/lib/couch && rm -f couch.db couch.db-journal user-image-* && cp backup-2026-11-01/* .'
sudo systemctl start couch
```

## Client addresses

Couch sees each client's own LAN address, so the sign-in limit per address and the activity log work as described. Nothing in front of it forwards connections.

## Uninstall

With a package, `sudo apt remove couch` or `sudo dnf remove couch`. With `install.sh`:

```sh
sudo systemctl disable --now couch
sudo rm /etc/systemd/system/couch.service /usr/local/bin/couch /etc/default/couch
sudo systemctl daemon-reload
```

The state stays in `/var/lib/couch`. To delete it and the user as well, after keeping a backup if needed: `sudo rm -r /var/lib/couch` and `sudo userdel couch`.

## macOS

`couch-VERSION-macos.dmg` holds a universal binary for Apple silicon and Intel Macs. It is not signed or notarized, so remove the quarantine flag before the first start. FFmpeg comes from Homebrew:

```sh
brew install ffmpeg
cp /Volumes/Couch/couch /usr/local/bin/
xattr -d com.apple.quarantine /usr/local/bin/couch
```

Then create the first user and start Couch as in the [README](../README.md#build-from-source). There is no launchd service; real-time folder monitoring works only on Linux, so use `-rescan-interval`.
