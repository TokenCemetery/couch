#!/bin/sh
# Install wizard of the Couch package (docs/INSTALL.md#synology-dsm). The
# answers reach scripts/postinst as variables named after each key. The
# FFmpeg page shows only while SynoCommunity's FFmpeg 8 is missing.

SETUP=$(cat <<'EOF'
{
	"step_title": "Media",
	"invalid_next_disabled_v2": true,
	"items": [{
		"type": "textfield",
		"desc": "Couch gets read-only access to this shared folder and creates the Movies and TV Shows libraries from it. A shared folder that does not exist yet is created.",
		"subitems": [{
			"key": "wizard_media_share",
			"desc": "Shared folder",
			"defaultValue": "video",
			"validator": {
				"allowBlank": false,
				"regex": {
					"expr": "/^[^\\s\\/\"$`\\\\]+$/",
					"errorText": "The name of one shared folder, without spaces."
				}
			}
		}]
	}]
}, {
	"step_title": "First user",
	"invalid_next_disabled_v2": true,
	"items": [{
		"desc": "The first user is the administrator. A reinstall that kept the data keeps its users and ignores this page."
	}, {
		"type": "textfield",
		"desc": "User name",
		"subitems": [{
			"key": "wizard_username",
			"desc": "User name",
			"validator": {
				"allowBlank": false
			}
		}]
	}, {
		"type": "password",
		"desc": "Optional; leave it blank to sign in without a password",
		"subitems": [{
			"key": "wizard_password",
			"desc": "Password",
			"validator": {
				"allowBlank": true,
				"maxLength": 256
			}
		}]
	}]
}, {
	"step_title": "Online metadata",
	"items": [{
		"desc": "Optional. Couch fetches metadata from a provider only when its API key is set, and sends a provider without a key nothing. Keys can be added later in /var/packages/couch/var/couch.conf."
	}, {
		"type": "password",
		"desc": "TMDb: movies and TV shows",
		"subitems": [{
			"key": "wizard_tmdb_api_key",
			"desc": "API key or read access token",
			"validator": {
				"allowBlank": true,
				"regex": {
					"expr": "/^[A-Za-z0-9._-]*$/",
					"errorText": "Letters, digits, dots, dashes and underscores only."
				}
			}
		}]
	}, {
		"type": "password",
		"desc": "TheTVDB: TV shows",
		"subitems": [{
			"key": "wizard_tvdb_api_key",
			"desc": "API key",
			"validator": {
				"allowBlank": true,
				"regex": {
					"expr": "/^[A-Za-z0-9._-]*$/",
					"errorText": "Letters, digits, dots, dashes and underscores only."
				}
			}
		}]
	}, {
		"type": "password",
		"desc": "TheAudioDB: music",
		"subitems": [{
			"key": "wizard_audiodb_api_key",
			"desc": "API key",
			"validator": {
				"allowBlank": true,
				"regex": {
					"expr": "/^[A-Za-z0-9._-]*$/",
					"errorText": "Letters, digits, dots, dashes and underscores only."
				}
			}
		}]
	}]
}
EOF
)

FFMPEG=$(cat <<'EOF'
{
	"step_title": "FFmpeg 8",
	"items": [{
		"desc": "FFmpeg 8 is not installed. Couch installs and runs without it, but cannot remux or transcode, so files the browser cannot play directly do not play.<br><br>To add it after this install:<ol><li>In Package Center, open Settings > Package Sources and add SynoCommunity with the location <code style=\"user-select: all\">https://packages.synocommunity.com</code>, unless it is listed already.</li><li>Install FFmpeg 8 from the Community tab.</li><li>Stop and run Couch again in Package Center.</li></ol>"
	}]
}
EOF
)

if [ -x /var/packages/ffmpeg8/target/bin/ffmpeg ]; then
	printf '[%s]\n' "$SETUP" >"$SYNOPKG_TEMP_LOGFILE"
else
	printf '[%s, %s]\n' "$SETUP" "$FFMPEG" >"$SYNOPKG_TEMP_LOGFILE"
fi
