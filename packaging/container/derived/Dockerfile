# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
ARG LOKI_BASE
FROM ${LOKI_BASE}
ARG LOKI_BASE_ID
LABEL io.loki.derived.base="${LOKI_BASE_ID}"

# Add project runtimes under /usr/local or /opt/project.
# COPY --chmod=0755 project-runtime /usr/local/bin/project-runtime
