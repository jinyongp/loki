# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

ARG SOURCE_DATE_EPOCH=0
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
      -o /out/loki ./cmd/loki \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w" -o /out/loki-launcher ./cmd/launcher \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w" -o /out/loki-executor ./cmd/executor

FROM --platform=$BUILDPLATFORM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS layout
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
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/uvx \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/rustc \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/cargo \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/rustfmt \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/cargo-fmt \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/clippy-driver \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/cargo-clippy \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/rust-analyzer \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/go \
 && ln -s /opt/loki/bin/loki /rootfs/opt/loki/toolchain/bin/gofmt

FROM alpine/git:2.54.0@sha256:ae0f6f4bce38d2b8c40becc0d6241a08d9f57186ea03029de2189a2b5e722d94 AS git-root

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
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
      org.opencontainers.image.source="https://github.com/jinyongp/loki" \
      org.opencontainers.image.licenses="Apache-2.0" \
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
COPY --chmod=0755 scripts/maintainer/devtools-launch /opt/loki/libexec/devtools
COPY bundled_skills/ /opt/loki/share/skills/
COPY config/runtime.toml config/gitconfig /usr/share/doc/loki/
COPY packaging/native/execution-contract.json packaging/native/egress-policy.json packaging/native/deployment-contract.json packaging/native/toolchain-catalog.json /usr/share/doc/loki/
COPY packaging/images/config/ /etc/loki/
COPY packaging/images/config/execution-contract.json /usr/share/doc/loki/container-execution-contract.json

ENV PATH=/opt/loki/bin:/usr/local/bin:/usr/bin:/bin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8
WORKDIR /workspace
ENTRYPOINT ["/opt/loki/bin/loki"]
CMD ["version"]
