# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
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

FROM alpine:3.24.2@sha256:31b6477333eb8257db9e5d7c3a7264fd0467928756f0bbcc27d35bea5d28cdbd
ARG LOKI_VERSION
ARG LOKI_REVISION
ARG CHROMIUM_VERSION=152.0.7977.82-r0
LABEL org.opencontainers.image.title="Loki Browser" \
      org.opencontainers.image.source="https://github.com/jinyongp/loki" \
      org.opencontainers.image.licenses="Apache-2.0" \
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
