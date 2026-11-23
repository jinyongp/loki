# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
ARG LOKI_BASE
FROM ${LOKI_BASE}
ARG LOKI_BASE_ID
LABEL io.loki.derived.base="${LOKI_BASE_ID}"

# Add project runtimes under /usr/local or /opt/project.
# COPY --chmod=0755 project-runtime /usr/local/bin/project-runtime
