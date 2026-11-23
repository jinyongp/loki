# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183

FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS go-tools
ARG TARGETOS
ARG TARGETARCH
ARG DEVTOOLS_VERSION=0.50.0
ARG DEVTOOLS_COMMIT=237f87813c06cd86a8ae89d1f4f6526c8ff50aff
ARG GH_VERSION=2.105.0
ARG GH_COMMIT=e1219499a578b69e3437adaa31cd60614e23c802
ARG GH_DATE=2026-11-18

RUN git clone --depth 1 --branch "v$DEVTOOLS_VERSION" https://github.com/jinyongp/devtools.git /src/devtools \
 && test "$(git -C /src/devtools rev-parse HEAD)" = "$DEVTOOLS_COMMIT"
RUN cd /src/devtools \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=$DEVTOOLS_VERSION -X main.commit=$DEVTOOLS_COMMIT" \
      -o /out/devtools ./cmd/devtools \
 && if test "$(go env GOHOSTARCH)" = "$TARGETARCH"; then /out/devtools --format json version | grep -q "\"version\":\"$DEVTOOLS_VERSION\""; fi

RUN git clone --depth 1 --branch "v$GH_VERSION" https://github.com/cli/cli.git /src/gh \
 && test "$(git -C /src/gh rev-parse HEAD)" = "$GH_COMMIT"
RUN cd /src/gh \
 && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/cli/cli/v2/internal/build.Version=$GH_VERSION -X github.com/cli/cli/v2/internal/build.Date=$GH_DATE" \
      -o /out/gh ./cmd/gh \
 && /out/gh version | grep -q '^gh version 2\.105\.0 '

FROM --platform=$TARGETPLATFORM rust:1.100.0-alpine3.24@sha256:8d0e82abfdf9a38071e07cabc5afba4ddbe077631a80e4e742a760c38beadc33 AS ripgrep
ARG RIPGREP_VERSION=16.0.0
ARG RIPGREP_COMMIT=669cb4c755db20b26c3af3d923c042ae224c7496
RUN apk add --no-cache git build-base pcre2-dev perl
ENV PCRE2_SYS_STATIC=1
RUN git clone --depth 1 --branch "$RIPGREP_VERSION" https://github.com/BurntSushi/ripgrep.git /src/ripgrep \
 && test "$(git -C /src/ripgrep rev-parse HEAD)" = "$RIPGREP_COMMIT"
RUN cd /src/ripgrep \
 && RUSTFLAGS="-C target-feature=-crt-static" \
      cargo rustc --locked --profile release-lto --features pcre2 -- -C target-feature=+crt-static \
 && /src/ripgrep/target/release-lto/rg --version | grep -q '^ripgrep 16\.0\.0 '

FROM scratch AS go-export
COPY --from=go-tools /out/devtools /devtools
COPY --from=go-tools /out/gh /gh

FROM scratch AS ripgrep-export
COPY --from=ripgrep /src/ripgrep/target/release-lto/rg /rg

# A single BuildKit graph schedules independent Go and Rust inputs together.
# Keep the individual export targets for callers that need only one family.
FROM scratch AS inputs-export
COPY --from=go-tools /out/devtools /devtools
COPY --from=go-tools /out/gh /gh
COPY --from=ripgrep /src/ripgrep/target/release-lto/rg /rg
