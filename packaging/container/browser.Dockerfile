# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
ARG TARGETOS
ARG TARGETARCH
ARG LOKI_VERSION
ARG LOKI_REVISION
ARG LOKI_DATE
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath \
      -ldflags="-s -w -X loki/internal/buildinfo.Version=$LOKI_VERSION -X loki/internal/buildinfo.Commit=$LOKI_REVISION -X loki/internal/buildinfo.Date=$LOKI_DATE" \
      -o /out/loki ./cmd/loki

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
ARG LOKI_VERSION
ARG LOKI_REVISION
ARG CHROMIUM_VERSION=142.0.7444.59-r0
LABEL org.opencontainers.image.title="Loki Browser" \
      org.opencontainers.image.version="$LOKI_VERSION" \
      org.opencontainers.image.revision="$LOKI_REVISION" \
      io.loki.browser.name="chromium" \
      io.loki.browser.version="$CHROMIUM_VERSION"

RUN apk add --no-cache "chromium=$CHROMIUM_VERSION" font-opensans \
 && addgroup -g 10001 -S workspace \
 && addgroup -g 10003 -S browser \
 && adduser -S -D -H -u 10003 -G browser browser \
 && addgroup browser workspace \
 && install -d -o 10003 -g 10003 -m 0700 /var/lib/loki/browser /var/lib/loki/browser-downloads \
 && install -d -o 0 -g 10001 -m 0750 /run/loki \
 && install -d -m 0755 /opt/loki/bin

COPY --from=build /out/loki /opt/loki/bin/loki

ENV PATH=/opt/loki/bin:/usr/bin:/bin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8
USER 10003:10003
WORKDIR /var/lib/loki/browser
ENTRYPOINT ["/opt/loki/bin/loki"]
CMD ["version"]
