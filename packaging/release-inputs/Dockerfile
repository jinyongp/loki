# syntax=docker/dockerfile:1.27.0@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS go-tools
ARG TARGETOS
ARG TARGETARCH
ARG DEVTOOLS_VERSION=0.18.0
ARG DEVTOOLS_COMMIT=6d93f0a3c24976a108cf9c4aa374dbe6c467559e
ARG GH_VERSION=2.101.0
ARG GH_COMMIT=0cf1092493af067646fc5f3db9421c6a6ec9c938
ARG GH_DATE=2026-09-15

RUN git clone --depth 1 --branch "v$DEVTOOLS_VERSION" https://github.com/jinyongp/devtools.git /src/devtools \
 && test "$(git -C /src/devtools rev-parse HEAD)" = "$DEVTOOLS_COMMIT"
RUN cd /src/devtools \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=$DEVTOOLS_VERSION -X main.commit=$DEVTOOLS_COMMIT" \
      -o /out/devtools ./cmd/devtools

RUN git clone --depth 1 --branch "v$GH_VERSION" https://github.com/cli/cli.git /src/gh \
 && test "$(git -C /src/gh rev-parse HEAD)" = "$GH_COMMIT"
RUN cd /src/gh \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/cli/cli/v2/internal/build.Version=$GH_VERSION -X github.com/cli/cli/v2/internal/build.Date=$GH_DATE" \
      -o /out/gh ./cmd/gh

FROM --platform=$TARGETPLATFORM rust:1.98.1-alpine3.24@sha256:c913be57168b9240b86f373f94060152a2e09ea16a72e0801a02ee3a262ca446 AS ripgrep
ARG RIPGREP_VERSION=15.2.0
ARG RIPGREP_COMMIT=e89fff89ac9af12e8d4ce9d5fd07beb408ca730f
RUN apk add --no-cache git pcre2-dev
RUN git clone --depth 1 --branch "$RIPGREP_VERSION" https://github.com/BurntSushi/ripgrep.git /src/ripgrep \
 && test "$(git -C /src/ripgrep rev-parse HEAD)" = "$RIPGREP_COMMIT"
RUN cd /src/ripgrep && cargo build --locked --release

FROM scratch
COPY --from=go-tools /out/devtools /devtools
COPY --from=go-tools /out/gh /gh
COPY --from=ripgrep /src/ripgrep/target/release/rg /rg
