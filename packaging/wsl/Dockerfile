# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=linux/amd64 ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3

ARG DOCKER_CE_VERSION=5:29.8.1-1~ubuntu.24.04~noble
ARG CONTAINERD_VERSION=2.3.5-1~ubuntu.24.04~noble
ARG DOCKER_BUILDX_VERSION=0.37.1-1~ubuntu.24.04~noble
ARG DOCKER_COMPOSE_VERSION=5.5.1-1~ubuntu.24.04~noble

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      acl ca-certificates curl dbus iptables passwd sudo systemd systemd-sysv \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
 && chmod a+r /etc/apt/keyrings/docker.asc \
 && printf '%s\n' \
      'Types: deb' \
      'URIs: https://download.docker.com/linux/ubuntu' \
      'Suites: noble' \
      'Components: stable' \
      'Architectures: amd64' \
      'Signed-By: /etc/apt/keyrings/docker.asc' \
      > /etc/apt/sources.list.d/docker.sources \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      "containerd.io=$CONTAINERD_VERSION" \
      "docker-buildx-plugin=$DOCKER_BUILDX_VERSION" \
      "docker-ce=$DOCKER_CE_VERSION" \
      "docker-ce-cli=$DOCKER_CE_VERSION" \
      "docker-compose-plugin=$DOCKER_COMPOSE_VERSION" \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

RUN if getent group ubuntu >/dev/null; then \
      test "$(getent group ubuntu | cut -d: -f3)" = 1000; \
    else \
      groupadd --gid 1000 ubuntu; \
    fi \
 && if getent passwd ubuntu >/dev/null; then \
      test "$(id -u ubuntu)" = 1000 \
      && test "$(id -g ubuntu)" = 1000; \
    else \
      useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash ubuntu; \
    fi \
 && usermod --lock ubuntu \
 && install -d -o ubuntu -g ubuntu -m 0750 /home/ubuntu/workspace \
 && printf '%s\n' 'ubuntu ALL=(root) NOPASSWD: /usr/local/bin/loki' > /etc/sudoers.d/loki-operator \
 && chmod 0440 /etc/sudoers.d/loki-operator \
 && visudo -cf /etc/sudoers.d/loki-operator

COPY --chmod=0644 packaging/wsl/wsl.conf /etc/wsl.conf
COPY --chmod=0644 packaging/wsl/wsl-distribution.conf /etc/wsl-distribution.conf
COPY --chmod=0755 packaging/wsl/provision.sh /usr/lib/loki-appliance/provision
COPY --chmod=0644 packaging/wsl/loki-appliance-provision.service /usr/lib/systemd/system/loki-appliance-provision.service
COPY --from=release --chmod=0755 loki-linux-amd64 /usr/lib/loki-appliance/loki
COPY --from=release --chmod=0600 release-manifest.json /usr/lib/loki-appliance/release-manifest.json

RUN chown root:root \
      /etc/wsl.conf \
      /etc/wsl-distribution.conf \
      /usr/lib/loki-appliance/provision \
      /usr/lib/loki-appliance/loki \
      /usr/lib/loki-appliance/release-manifest.json \
      /usr/lib/systemd/system/loki-appliance-provision.service \
 && chmod 0644 /etc/wsl.conf /etc/wsl-distribution.conf \
 && chmod 0755 /usr/lib/loki-appliance/provision /usr/lib/loki-appliance/loki \
 && chmod 0600 /usr/lib/loki-appliance/release-manifest.json \
 && systemctl enable docker.service containerd.service loki-appliance-provision.service \
 && systemctl mask \
      systemd-resolved.service \
      systemd-networkd.service \
      NetworkManager.service \
      systemd-tmpfiles-setup.service \
      systemd-tmpfiles-clean.service \
      systemd-tmpfiles-clean.timer \
      systemd-tmpfiles-setup-dev-early.service \
      systemd-tmpfiles-setup-dev.service \
      tmp.mount \
 && rm -f /var/lib/dbus/machine-id \
 && truncate -s 0 /etc/machine-id \
 && find /boot -maxdepth 1 -type f \( -name 'vmlinuz*' -o -name 'initrd*' -o -name 'initramfs*' \) -delete \
 && test "$(id -u ubuntu)" = 1000 \
 && ! id -nG ubuntu | tr ' ' '\n' | grep -Fxq docker \
 && ! grep -Eq '^[^:]+:\$[^:]*:' /etc/shadow

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
