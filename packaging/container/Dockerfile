# syntax=docker/dockerfile:1.27.0@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e

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
      -o /out/loki ./cmd/loki \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w" -o /out/loki-launcher ./cmd/loki-launcher \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w" -o /out/loki-executor ./cmd/loki-executor

FROM --platform=$BUILDPLATFORM alpine:3.24.2@sha256:31b6477333eb8257db9e5d7c3a7264fd0467928756f0bbcc27d35bea5d28cdbd AS layout
RUN install -d -m 0755 /rootfs/etc /rootfs/opt/loki/bin /rootfs/opt/loki/libexec /rootfs/opt/loki/share/skills /rootfs/opt/loki/toolchain/bin /rootfs/usr/local/bin /rootfs/usr/share/doc/loki \
 && install -d -o 10000 -g 10001 -m 2770 /rootfs/workspace \
 && install -d -o 10000 -g 10000 -m 0700 /rootfs/var/cache/loki /rootfs/var/lib/loki/runner \
 && install -d -o 0 -g 0 -m 0700 /rootfs/var/lib/loki/runtime /rootfs/var/lib/loki/signing \
 && install -d -o 0 -g 10001 -m 0750 /rootfs/run/loki \
 && printf '%s\n' \
      'root:x:0:0:root:/root:/bin/sh' \
      'runner:x:10000:10000:Loki runner:/home/runner:/bin/sh' \
      'egress:x:10002:10002:Loki egress:/nonexistent:/sbin/nologin' \
      'executor:x:10004:10001:Loki executor:/nonexistent:/sbin/nologin' \
      > /rootfs/etc/passwd \
 && printf '%s\n' \
      'root:x:0:' \
      'runner:x:10000:' \
      'workspace:x:10001:runner' \
      'egress:x:10002:' \
      > /rootfs/etc/group \
 && install -d -o 10000 -g 10000 -m 0700 /rootfs/home/runner \
 && ln -s /opt/loki/bin/loki /rootfs/usr/local/bin/loki \
 && ln -s /opt/loki/libexec/devtools /rootfs/usr/local/bin/devtools \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/node \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/npm \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/npx \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/pnpm \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/python \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/python3 \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/uv \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/uvx

FROM alpine/git:2.54.0@sha256:3b44767883ac77bddae0160cc27b6b039345e23fa3504f4159efaa32264ab57f AS git-root

FROM alpine:3.24.2@sha256:31b6477333eb8257db9e5d7c3a7264fd0467928756f0bbcc27d35bea5d28cdbd
ARG TARGETARCH
ARG LOKI_VERSION
ARG LOKI_REVISION
ARG DEVTOOLS_VERSION
ARG DEVTOOLS_AMD64_SHA256
ARG DEVTOOLS_ARM64_SHA256
ARG RIPGREP_VERSION
ARG RIPGREP_AMD64_SHA256
ARG RIPGREP_ARM64_SHA256
ARG GH_VERSION
ARG GH_AMD64_SHA256
ARG GH_ARM64_SHA256
LABEL org.opencontainers.image.title="Loki" \
      org.opencontainers.image.version="$LOKI_VERSION" \
      org.opencontainers.image.revision="$LOKI_REVISION" \
      io.loki.devtools.version="$DEVTOOLS_VERSION" \
      io.loki.devtools.amd64.sha256="$DEVTOOLS_AMD64_SHA256" \
      io.loki.devtools.arm64.sha256="$DEVTOOLS_ARM64_SHA256" \
      io.loki.ripgrep.version="$RIPGREP_VERSION" \
      io.loki.ripgrep.amd64.sha256="$RIPGREP_AMD64_SHA256" \
      io.loki.ripgrep.arm64.sha256="$RIPGREP_ARM64_SHA256" \
      io.loki.gh.version="$GH_VERSION" \
      io.loki.gh.amd64.sha256="$GH_AMD64_SHA256" \
      io.loki.gh.arm64.sha256="$GH_ARM64_SHA256"

COPY --from=git-root / /
COPY --from=layout /rootfs/ /
COPY --from=build /out/loki /opt/loki/bin/loki
COPY --from=build /out/loki-launcher /opt/loki/bin/loki-launcher
COPY --from=build /out/loki-executor /opt/loki/bin/loki-executor
COPY --from=artifacts --chmod=0755 /devtools/${TARGETARCH}/devtools /opt/loki/bin/devtools
COPY --from=artifacts --chmod=0755 /ripgrep/${TARGETARCH}/rg /usr/bin/rg
COPY --from=artifacts --chmod=0755 /gh/${TARGETARCH}/gh /usr/bin/gh
COPY --from=artifacts /metadata/devtools-catalog.json /usr/share/doc/loki/devtools-catalog.json
COPY --from=artifacts /metadata/provenance.json /usr/share/doc/loki/provenance.json
COPY --chmod=0755 scripts/loki-devtools-launch /opt/loki/libexec/devtools
COPY bundled_skills/ /opt/loki/share/skills/
COPY config/loki-go.toml config/loki-gitconfig /usr/share/doc/loki/
COPY packaging/go/execution-contract.json packaging/go/egress-policy.json packaging/go/deployment-contract.json /usr/share/doc/loki/
COPY packaging/container/config/ /etc/loki/
COPY packaging/container/config/execution-contract.json /usr/share/doc/loki/container-execution-contract.json

ENV PATH=/opt/loki/bin:/usr/local/bin:/usr/bin:/bin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8
WORKDIR /workspace
ENTRYPOINT ["/opt/loki/bin/loki"]
CMD ["version"]
