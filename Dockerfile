# Release image: static Couch plus FFmpeg/FFprobe for scanning and HLS.
# Alpine's FFmpeg has the fd protocol Couch requires and libx264/aac.

FROM docker.io/library/golang:1.26-alpine3.24 AS build
WORKDIR /src
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/cmd ./cmd
COPY src/internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/couch ./cmd/couch

FROM docker.io/library/alpine:3.24
# VAAPI=1 adds Mesa's VA-API drivers (AMD) for -hwaccel vaapi, about 320 MB.
ARG VAAPI=
RUN apk add --no-cache ffmpeg ${VAAPI:+mesa-va-gallium} \
    && adduser -D -H -u 10001 couch \
    && mkdir /data \
    && chown couch:couch /data \
    && chmod 0700 /data
COPY --from=build /out/couch /usr/local/bin/couch
USER couch
VOLUME /data
EXPOSE 8097
ENTRYPOINT ["couch", "-data", "/data"]
CMD ["-listen", ":8097"]
