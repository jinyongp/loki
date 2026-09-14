FROM ubuntu:24.04

ENV container=docker
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl dbus fonts-liberation git \
      libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 \
      libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libnspr4 libnss3 \
      libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 \
      libxfixes3 libxkbcommon0 libxrandr2 openssh-client pkg-config python3 \
      python3-pip python3-venv systemd systemd-sysv xdg-utils \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* \
 && systemctl mask systemd-remount-fs.service getty@.service console-getty.service

COPY run-acceptance.sh /usr/local/libexec/run-loki-go-acceptance
RUN chmod 0755 /usr/local/libexec/run-loki-go-acceptance

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
