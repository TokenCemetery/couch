# Running Couch as a systemd service

The container ([README](../README.md#quick-start-docker-compose)) is the main way to run Couch. This manual covers the alternative: the static binary as a systemd service on Linux amd64 or arm64, written for Ubuntu 26.04. Packages for [Synology DSM](#synology-dsm) and a [macOS](#macos) disk image are covered at the end. The unit is verified with `systemd-analyze`, but the install has not yet been run on the target host ([VALIDATION](VALIDATION.md#install-114-117), #117). Couch serves plain HTTP on port 8097 for the LAN; TLS and a reverse proxy are not covered.

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

Create the first user, the administrator, with the password on standard input, at most 1024 bytes; an empty one leaves it without a password. The input is hidden and does not end up in the shell history:

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

## Synology DSM

`couch-VERSION-synology-x86_64.spk` (Intel and AMD models) and `couch-VERSION-synology-armv8.spk` (Realtek RTD1296 and RTD1619B, Marvell Armada 37xx) install Couch from Package Center on DSM 7.1 or later, without Container Manager. They are built from [deploy/synology](../deploy/synology) but have not yet been installed on a NAS (#144). The package has no web client, since there is no way to give it a copy of Emby Web on DSM: Couch serves only the API on port 8097 until it has a web client of its own, and `http://NAS:8097/` answers with `"WebClientConfigured": false` once it runs.

FFmpeg 8 comes from SynoCommunity and is optional: without it Couch runs, but HLS remux and transcoding are disabled. To add it, open **Package Center → Settings → Package Sources**, add SynoCommunity with the location `https://packages.synocommunity.com`, then install **FFmpeg 8** from the **Community** tab. If it is installed after Couch, stop and run Couch in Package Center to use it. `-hwaccel` is not supported in the package.

The packages are not signed: set **Package Center → Settings → General → Trust Level** to **Any publisher**, then use **Manual Install** with the file for your model. The wizard asks for:

- the shared folder with the media (default `video`), which becomes the Movies and TV Shows libraries. The package's user `couch` gets read-only access to it, and a shared folder that does not exist yet is created;
- the first user and its password, which can be left blank to sign in without one;
- optionally the API keys for TMDb, TheTVDB and TheAudioDB, which turn on online metadata.

The wizard also lists the FFmpeg steps above while FFmpeg 8 is missing. With the DSM firewall on, allow **Couch** in **Control Panel → Security → Firewall**.

The package keeps its files in `/var/packages/couch`:

- `target/bin/couch`: the binary;
- `var/data`: the state, as `/var/lib/couch` above;
- `var/couch.conf`: the options, written once by the wizard. The variables are those of `/etc/default/couch`, with `COUCH_ARGS` in double quotes because the file is read by the shell; `-data /var/packages/couch/var/data` is added. Stop and run Couch after a change. To add or change an API key later, edit it over SSH as an administrator with `sudo vi /var/packages/couch/var/couch.conf`, for example `COUCH_TMDB_API_KEY=...`;
- `var/couch.out`: the output of the last start, which explains a start that failed.

An upgrade is a manual install of the newer file and keeps the options and the state. Take a backup first, over SSH as an administrator:

```sh
sudo -u couch /var/packages/couch/target/bin/couch -data /var/packages/couch/var/data -backup /var/packages/couch/var/backup-$(date +%F)
```

Uninstalling asks whether to keep the data for a later reinstall or delete it; the media is never touched. A reinstall that kept the data keeps its users and options and ignores the wizard's answers.

## macOS

`couch-VERSION-macos.dmg` holds a universal binary for Apple silicon and Intel Macs. It is not signed or notarized, so remove the quarantine flag before the first start. FFmpeg comes from Homebrew:

```sh
brew install ffmpeg
cp /Volumes/Couch/couch /usr/local/bin/
xattr -d com.apple.quarantine /usr/local/bin/couch
```

Then create the first user and start Couch as in the [README](../README.md#build-from-source). There is no launchd service; real-time folder monitoring works only on Linux, so use `-rescan-interval`.
