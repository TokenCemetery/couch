# First implementation stage

2026-09-18: the user approved implementation and chose Emby Web as the first client.

1. Record the public contract of Emby 4.10.0.40 and the actual requests of its API client.
2. Implement a local Go server: info, login/logout, persistent sessions, user, settings and an empty library.
3. Allow connecting the original web resources through a separate optional `/web/` adapter. Go serves API requests; the adapter receives only static resources and does not forward tokens, cookies or the request body upstream.
4. Check login, wrong password, access without a token, revocation, restart, another user's userId, settings persistence, token conflict and upstream isolation. Run the race detector, vet and build.
5. Check the original API client separately from the UI. If a browser is available, check the screens. Record the actual verification boundaries.

The initial implementation uses the Go standard library. A JSON store with schema v1, atomic file replacement and directory locking suits one account and a small number of M1 sessions; it is not meant for the M2 media catalog. Passwords use PBKDF2-HMAC-SHA256 with a per-user salt; only token hashes remain in the store. The listener is reachable only on loopback by default.

The first user is created by a local command and does not get administrative rights through the API. Administration, WebSocket, scanning and playback are not part of this stage. Unknown routes return an error, not a fake success.

At the time of the first slice, M0 remained open: there was no authenticated exchange, no measurements and no resource budgets; M1 awaited browser acceptance. Later the exchange and acceptance were completed — current status is in [ROADMAP](ROADMAP.md).

## Initial M2 catalog

After another browser runtime refusal, an independent catalog slice was implemented, without declaring M1 complete:

1. Optional `-media-dir`: reading one movie folder at startup, without changing files.
2. Sequential FFprobe with a timeout, output limit and access only to the open descriptor. Traversal in batches, skipping symlinks/special/broken files, explicit catalog limits.
3. Compatible Views, Items, Items/{id}, root and Latest routes; search, filters and bounded pagination. DTOs are built after the page is selected.
4. Tests for isolation, errors, limits, ID stability and requests of the original API client on synthetic video.

The in-memory catalog is a temporary solution for a limited read-only scenario; user data stays in the existing store. A persistent index and file appearance history are not implemented yet.

## Playback and state (2026-09-19)

Implemented after the reference contract was captured with the user's authorization and Emby Web could run in the browser against Couch. Each decision below is confirmed by real client behavior, not inferred from documentation:

1. The DTO layer (`src/internal/server/dto.go`) reproduces the reference fields, including arrays the client dereferences without guards. `Subviews` is returned only in the library item response, as in the reference.
2. Home screen (`src/internal/server/home.go`): `HomeSections` and `Sections/{id}/Items`. The server fills the rows; only sections Couch can populate are declared.
3. `PlaybackInfo` negotiates Direct Stream by the client's `DeviceProfile`. Couch does not transcode, so on a profile mismatch the source is declared unsupported rather than promised and then refused.
4. File serving uses `http.ServeContent`: Range, If-Range, HEAD, 206 and 416 come from the standard library, and the file is streamed. The catalog holds an open `os.Root`, so reopening a file cannot escape the chosen directory.
5. `SupportsDirectPlay` is deliberately `false`: in the Emby model it means the client reads the file by `MediaSource.Path`. HTTP delivery is Direct Stream.
6. Progress, "played" and "favorites" are stored in `state.ItemState` next to the user; the number of items is limited. The position survives a restart; finishing playback resets it.

Later a basic WebSocket without external dependencies was added: authorization, handshake, KeepAlive, ping/pong, limits on message size and connection count. At that slice images and series were still missing; their implementation is described below. Transcoding was not implemented then, nor was localization of row titles; home screen headings were later translated through embedded files `src/internal/server/locales/*.yaml` (#18).

## HTTP serving reliability (2026-09-19)

For video, the overall timeout was replaced by a renewable write deadline. Validators use the current size/mtime of the open file, not the ID and scan metadata; the ETag is weak. Regressions for ranges, caching, long transfers and stalled writes were added. The integration scenario was extended with checks of the stream and position after restart. Results and limitations are in [VALIDATION](VALIDATION.md).

## Playback negotiation and local UI

PlaybackInfo accepts query/JSON parameters and checks limits, the source, tracks and basic DeviceProfile conditions. Unsupported playback returns NoCompatibleStream without a URL; transcoding is not faked.

`-web-dir` serves the provided Emby Web from a separate folder through `os.Root`, without listings or escaping the root. The mode is mutually exclusive with `-web-upstream`. index.html is prepared in memory; user files are not changed. API module integration can run without the original Emby through `COUCH_WEB_DIR`. UI completeness and browser acceptance are verified separately — see [VALIDATION](VALIDATION.md).

## User state events (2026-09-20)

UserDataChanged is published after a successful write of a favorite, a played mark or progress. The write and the queuing of events are ordered; only the changed record is copied, not the whole store. Broadcast is limited by UserId and a queue of 32 messages per connection; overflow closes the slow client. There is no event history; on reconnect an API read is required.

The token is rechecked when the socket registers, before an event is sent, on an incoming frame and on ping. Logout closes the connections of that specific session. Server.Close closes hijacked connections that HTTP Shutdown does not manage. Other WebSocket events and remote control remain outside the implemented slice.

## M2 checkpoint (2026-09-21)

The audit fixed UserData filters, Resume pagination, loss of a missing position and duplicate/late playback reports. The last 64 PlaySessionIds are saved together with state; without an ID, arrival order is used. Series/Season/Episode with stable IDs and the Seasons/Episodes API were added. Naming format and limitations are described in the [README](../README.md).

As agreed with the user, local Primary covers are protected by signed ImageTags: the original UI passes the tag but not the API token. The HMAC binds the session, object and revision; the key is derived from the stored token hash. Every serve checks the session, including cache revalidation. Logout/expiry revoke the link; a normal restart preserves it. Size and header are limited; the original JPEG/PNG is served without pixel decoding or transformations. The revision uses path/size/mtime, not a content hash.

The browser revealed global search dependencies: ItemTypes and the GroupProgramsBySeries/IncludeSearchTypes parameters. ItemTypes returns unique types after filtering, regardless of the page; boolean parameters are validated. TV program grouping is not implemented: Live TV is outside the current slice. The full getSearchResults is included in the integration regression.

M2 is not closed: automated backend checks do not replace the remaining browser acceptance. Audit result: [M2-AUDIT](M2-AUDIT.md).
