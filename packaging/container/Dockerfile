# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

ARG SOURCE_DATE_EPOCH=0
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

FROM --platform=$BUILDPLATFORM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS layout
RUN install -d -m 0755 /rootfs/etc /rootfs/opt/loki/bin /rootfs/opt/loki/libexec /rootfs/opt/loki/share/skills /rootfs/usr/local/bin /rootfs/usr/share/doc/loki \
 && install -d -o 10000 -g 10001 -m 2770 /rootfs/workspace \
 && install -d -o 10000 -g 10000 -m 0700 /rootfs/var/cache/loki /rootfs/var/lib/loki/runner \
 && install -d -o 0 -g 0 -m 0700 /rootfs/var/lib/loki/runtime /rootfs/var/lib/loki/signing \
 && install -d -o 0 -g 10001 -m 0750 /rootfs/run/loki \
 && printf '%s\n' \
      'root:x:0:0:root:/root:/bin/sh' \
      'runner:x:10000:10000:Loki runner:/home/runner:/bin/sh' \
      'egress:x:10002:10002:Loki egress:/nonexistent:/sbin/nologin' \
      > /rootfs/etc/passwd \
 && printf '%s\n' \
      'root:x:0:' \
      'runner:x:10000:' \
      'workspace:x:10001:runner' \
      'egress:x:10002:' \
      > /rootfs/etc/group \
 && install -d -o 10000 -g 10000 -m 0700 /rootfs/home/runner \
 && ln -s /opt/loki/bin/loki /rootfs/usr/local/bin/loki \
 && ln -s /opt/loki/libexec/devtools /rootfs/usr/local/bin/devtools

FROM alpine/git@sha256:0b5f57d22181e8b8fbe8ac5ca8754faa0d577f101b9857418f1acc43955ad464
ARG TARGETARCH
ARG LOKI_VERSION
ARG LOKI_REVISION
ARG DEVTOOLS_VERSION
ARG DEVTOOLS_AMD64_SHA256
ARG DEVTOOLS_ARM64_SHA256
ARG RIPGREP_VERSION
ARG RIPGREP_AMD64_SHA256
ARG RIPGREP_ARM64_SHA256
LABEL org.opencontainers.image.title="Loki" \
      org.opencontainers.image.version="$LOKI_VERSION" \
      org.opencontainers.image.revision="$LOKI_REVISION" \
      io.loki.devtools.version="$DEVTOOLS_VERSION" \
      io.loki.devtools.amd64.sha256="$DEVTOOLS_AMD64_SHA256" \
      io.loki.devtools.arm64.sha256="$DEVTOOLS_ARM64_SHA256" \
      io.loki.ripgrep.version="$RIPGREP_VERSION" \
      io.loki.ripgrep.amd64.sha256="$RIPGREP_AMD64_SHA256" \
      io.loki.ripgrep.arm64.sha256="$RIPGREP_ARM64_SHA256"

COPY --from=layout /rootfs/ /
COPY --from=build /out/loki /opt/loki/bin/loki
COPY --from=artifacts --chmod=0755 /devtools/${TARGETARCH}/devtools /opt/loki/bin/devtools
COPY --from=artifacts --chmod=0755 /ripgrep/${TARGETARCH}/rg /usr/bin/rg
COPY --from=artifacts /metadata/devtools-catalog.json /usr/share/doc/loki/devtools-catalog.json
COPY --from=artifacts /metadata/provenance.json /usr/share/doc/loki/provenance.json
COPY --chmod=0755 scripts/loki-devtools-launch /opt/loki/libexec/devtools
COPY bundled_skills/ /opt/loki/share/skills/
COPY config/loki-go.toml config/loki-gitconfig /usr/share/doc/loki/
COPY packaging/go/execution-contract.json packaging/go/egress-policy.json packaging/go/deployment-contract.json /usr/share/doc/loki/

ENV PATH=/opt/loki/bin:/usr/local/bin:/usr/bin:/bin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8
WORKDIR /workspace
ENTRYPOINT ["/opt/loki/bin/loki"]
CMD ["version"]
