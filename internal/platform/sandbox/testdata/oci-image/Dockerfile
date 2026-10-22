# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY modules ./modules
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/loki ./cmd/loki \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/loki-job-bootstrap ./cmd/loki-job-bootstrap

FROM alpine/git:2.54.0@sha256:832b1cd1a271509f3d5272a1a62d4cb2ab1a53426ebde1f6c2cb7349f907dc6f AS git-root

# The reboot fixture exercises native broker/launcher units and shell Jobs.
# Keeping Git and the management CLI out of this workload avoids cloning their
# unrelated layers through the disposable guest's vfs storage driver under TCG.
# The canonical OCI acceptance image remains the default final stage below.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS native-reboot
COPY --from=build /out/loki-job-bootstrap /opt/loki/bin/loki-job-bootstrap
WORKDIR /workspace
CMD ["/bin/sh"]

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY --from=git-root / /
COPY --from=build /out/loki /opt/loki/bin/loki
COPY --from=build /out/loki-job-bootstrap /opt/loki/bin/loki-job-bootstrap
COPY config/gitconfig /usr/share/doc/loki/gitconfig
COPY packaging/native/execution-contract.json /usr/share/doc/loki/
COPY --chmod=0644 internal/platform/sandbox/testdata/oci-image/egress-policy.json /usr/share/doc/loki/egress-policy.json
ENV PATH=/opt/loki/bin:/usr/local/bin:/usr/bin:/bin
WORKDIR /workspace
CMD ["/bin/sh"]
