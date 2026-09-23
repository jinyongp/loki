# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
ARG LOKI_BASE
FROM ${LOKI_BASE}
ARG LOKI_BASE_ID
LABEL io.loki.derived.base="${LOKI_BASE_ID}"

# Add project runtimes under /usr/local or /opt/project.
# COPY --chmod=0755 project-runtime /usr/local/bin/project-runtime
