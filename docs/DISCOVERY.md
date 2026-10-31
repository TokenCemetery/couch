# Emby compatibility discovery

Date: 2026-09-18. Status: preliminary discovery; the public API and HTML of the live Emby have been checked, authenticated scenarios have not been explored yet.

## Confirmed facts

- At the start of discovery the `couch` project was empty. The first Go API slice is now implemented; verification boundaries are described in [VALIDATION.md](VALIDATION.md). No Git commits were created.
- Jellyfin sources are available in `/Users/olegshulyakov/Projects/GitHub/jellyfin/jellyfin`, commit `50866380c95758244f79bd4fbb01b9474e80bd81`.
- `SharedVersion.cs` contains `13.0.0`. This is the version of the examined checkout, not confirmation of a stable release.
- `Jellyfin.Api/Controllers` has 60 controllers. Routes of the main scenarios and some models were examined; no full code audit was done.
- The README explicitly separates the server from `jellyfin-web`: frontend sources are absent from the checked-out repository.
- `LICENSE` contains GNU GPL version 2. The new project's license is GPL-2.0 (#11); terms for using third-party code have not been chosen yet.
- The name `ds918.local` did not resolve. The address `http://192.168.1.11:8096/` is reachable, including from the sandbox (checked 2026-09-19; earlier, connections from inside the sandbox failed).
- The built-in browser runs Emby Web 4.10.0.40 without changes to its code. This made browser acceptance possible, which had been blocked by browser runtime refusal.
- With the user's permission, an authenticated reference contract was captured: 30 sanitized fixtures in `docs/fixtures/emby-4.10.0.40/`. Capture sessions were closed, token revocation confirmed.
- The reference uses short numeric string `Id`s and a separate `Guid`; for collections `Guid`, `PresentationUniqueKey` and `DisplayPreferencesId` are the same. Dates use the .NET format with seven fractional digits. Couch keeps its own stable hex identifiers: clients treat `Id` as an opaque string.
- `GET /System/Info/Public` and `GET /emby/System/Info/Public` returned HTTP 200 and identical JSON: version `4.10.0.40`, server name `DS918+`, a string `Id`, empty `LocalAddresses` and `RemoteAddresses` arrays. The instance identifier is not stored here.
- `GET /` returned HTTP 302 with `Location: web/index.html`; the target page returned HTTP 200. The HTML contains `data-appversion="4.10.0.40"`, `data-culture="ru"`, `lang="ru"` and a link `apploader.js?v=4.10.0.40`. This is the version the page declares; JavaScript execution and login were not checked.
- The page's import map references `modules/emby-apiclient/apiclient.js`, `connectionmanager.js` and `modules/browserdeviceprofile.js`. The API client, fetchhelper and querystring were examined and run against the Go server; connection manager and browser device profile have not been checked yet.
- The collection, performance, OS, hardware and versions of other clients are unknown. The name `DS918+` alone does not confirm the device model.

## Contract sources

Observations of the current Emby Web: the API client adds `/emby`, uses varying path case, passes device data and the token through `X-Emby-*` query parameters, and logs in with a `Username`/`Pw` form. For JSON requests the fetch wrapper replaces the content type with `text/plain` and adds `reqformat=json`. Version 4.10 settings are read through `/usersettings/{id}`, partial update through `/usersettings/{id}/Partial`.

The reference's public `/Users/Public` returns an array; the observed entry has `Name`, `ServerId`, `Prefix`, `Id`, `HasPassword`, `HasConfiguredPassword`, `LastLoginDate`, `LastActivityDate`. Personal values were not stored. `/Branding/Configuration` returned empty `LoginDisclaimer` and `CustomCss`.

For each behavior we record the source: observation of a specific Emby, official documentation or the Jellyfin implementation. Matching routes do not prove compatible responses and behavior.

| Area | Jellyfin: path relative to checkout | What to check on Emby |
|---|---|---|
| Server info, login | `Jellyfin.Api/Controllers/SystemController.cs`, `UserController.cs` | Required fields, version, server identifier, login errors |
| Authorization | `Jellyfin.Api/Auth/CustomAuthenticationHandler.cs` | Header schemes, binding of token to user and device, revocation |
| Library | `Jellyfin.Api/Controllers/ItemsController.cs`, `UserLibraryController.cs`, `UserViewsController.cs` | Filters, sorting, pagination, empty results, permissions |
| Series | `Jellyfin.Api/Controllers/TvShowsController.cs` | Seasons, episodes, next episode |
| DTO | `MediaBrowser.Model/Dto/BaseItemDto.cs`, `MediaSourceInfo.cs` | ID types, key case, dates, missing field / null / empty array |
| Playback selection | `Jellyfin.Api/Controllers/MediaInfoController.cs`, `MediaBrowser.Model/MediaInfo/PlaybackInfoResponse.cs` | Client profile, tracks, capabilities, URLs, errors |
| Streams | `Jellyfin.Api/Controllers/VideosController.cs`, `DynamicHlsController.cs` | Range, seek, HLS, connection drop, cancellation |
| Position and sessions | `Jellyfin.Api/Controllers/PlaystateController.cs`, `SessionController.cs` | Retries and event order, positions, session termination |
| Events | `Jellyfin.Api/Middleware/WebSocketHandlerMiddleware.cs`, `MediaBrowser.Controller/Net/WebSocketMessages/` | Connection, messages, heartbeat, reconnection |
| FFmpeg | `MediaBrowser.MediaEncoding/Transcoding/TranscodeManager.cs` | Launch, limits, stopping, temporary file cleanup |

The official [Emby REST API documentation](https://dev.emby.media/doc/restapi/index.html) describes the `/emby` prefix, JSON/XML and the API Browser. Which variants the target client uses must be recorded in the compatibility matrix.

The [authentication description](https://dev.emby.media/doc/restapi/User-Authentication.html) lists `/Users/Public`, `/Users/AuthenticateByName`, `Authorization: Emby …`, `X-Emby-Token` and token revocation through `/Sessions/Logout`. This is the baseline contract to verify, not an observation of our instance.

The [PlaybackInfo documentation](https://dev.emby.media/reference/RestAPI/MediaInfoService/postItemsByIdPlaybackinfo.html) ties the device profile and track selection to the returned sources, session identifier and playback URLs. A simple response with a file link is not enough to cover these scenarios.

## Approaches considered

| Approach | Advantage | Cost / limitation |
|---|---|---|
| Go server following Emby contracts, Jellyfin as reference — recommended | Compatibility and resources can be checked on small completed scenarios | Requires discovery of actual requests and version differences |
| Porting Jellyfin subsystems to Go one by one | Ready examples of complex media logic | Large volume; porting does not prove compatibility with modern Emby |
| Go proxy in front of Jellyfin | Quick experiment with API translation | The .NET server and its resource use remain; not a final solution |

Proposed initial architecture: one Go process, local data storage, FFprobe/FFmpeg for media, bounded background queues. The choice of DB and libraries is deferred until the first scenario's requirements. Microservices are not justified for it.

## Next discovery

1. Use the verified address `http://192.168.1.11:8096/` for further discovery; public info has already been read.
2. Record versions of the other target apps; the server and Emby Web HTML declare `4.10.0.40`.
3. Record requests and responses for the login, library browsing and playback scenarios; WebSocket and player behavior separately.
4. Store only sanitized fixtures: no passwords, tokens, personal titles or paths. For related IDs keep a consistent mapping.
5. For state-changing scenarios use an agreed test user and test library; do not replay writes on the production instance automatically.
6. Take baseline resource measurements and define a specific load for comparison.

## Open decisions

### Initial catalog contract

Methods of the original `apiclient.js` were verified by reading from the reference: `getItems(userId, options)` calls `/Users/{id}/Items` or `/Items`; `getItem(userId, itemId)` calls the corresponding item route; `getLatestItems(options)` returns the `/Users/{id}/Items/Latest` response. This is information about client requests, not an authenticated observation of production Emby responses.

Routes and fields were checked against [ItemsService](https://dev.emby.media/reference/RestAPI/ItemsService/getItems.html), [UserLibraryService](https://dev.emby.media/reference/RestAPI/UserLibraryService/getUsersByUseridItemsById.html) and local Jellyfin controllers. Couch implements a limited set of filters; unknown ones return 400. A temporary DateCreated from mtime is not claimed to be identical to Emby's history.

Technical metadata comes from [FFprobe](https://ffmpeg.org/ffprobe.html). To pass an open file, the [fd protocol](https://ffmpeg.org/ffmpeg-protocols.html#fd) was chosen: it supports seek on a regular file and allows disabling other protocols. Opening is confined by [os.Root](https://pkg.go.dev/os#Root); this is not protection against native decoder vulnerabilities and not isolation of mounts/hardlinks.

### What remains to be determined

- The user confirmed: the first client is the built-in Emby Web. Priority of the other apps has not been chosen yet.
- Target platform: `ds918` in the hostname is not enough to infer the NAS model, CPU, OS and hardware acceleration.
- Library size, number of concurrent streams, formats in use and observed Emby failures.
- Distribution model; the provenance of used sources and frontend resources is recorded before they are included in the project.
- Whether migration of users, history and settings is needed. API compatibility alone does not mean database compatibility.

Related documents: [requirements](PRD.md), [roadmap](ROADMAP.md).

## PlaybackInfo refinement and local UI (2026-09-19)

In the original `apiclient.js`, the `getPlaybackInfo(itemId, options, deviceProfile)` method sends options through the query and the DeviceProfile as JSON. Previously Couch considered only the profile from the body. Now both parameter sources are checked; conflicting values are rejected.

`browserdeviceprofile.js` 4.10.0.40 contains CodecProfiles for Video/VideoAudio: limits on H.264 level/profile, width, bitrate, HEVC tags, audio channel count and IsSecondaryAudio. IsRequired appears as a boolean and as the string `"false"`, Value as a string and as a number. These forms are covered by tests. Schemas and the NoCompatibleStream error code were checked against the [official PlaybackInfo API](https://dev.emby.media/reference/RestAPI/MediaInfoService/postItemsByIdPlaybackinfo.html). The semantics of ApplyConditions and missing required values were checked against local Jellyfin models; they are not considered an authenticated observation of modern Emby.

The user provided `../emby-webui`. SHA-256 hashes of the apiclient/fetchhelper/querystring modules match the previously verified 4.10.0.40 resources. The local index.html contains the original `<html>` without data-appversion; apploader.js reads this attribute for dashboardVersion and the cache version. The `-web-dir` adapter adds the attribute in the HTTP response without changing files. The frontend itself is not copied into the Couch project.

A repeat browser check is unavailable right now: the connection to the built-in browser was refused. Local UI verification is limited to serving resources and running the original API modules without DOM/player.
