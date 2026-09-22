# syntax=docker/dockerfile:1.27.0@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e
ARG LOKI_BASE
FROM ${LOKI_BASE}
ARG LOKI_BASE_ID
LABEL io.loki.derived.base="${LOKI_BASE_ID}"

# Add project runtimes under /usr/local or /opt/project.
# COPY --chmod=0755 project-runtime /usr/local/bin/project-runtime
