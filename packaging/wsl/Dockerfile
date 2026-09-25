# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=linux/amd64 ubuntu:24.04@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3 AS rootfs

ARG SOURCE_DATE_EPOCH=0
ARG UBUNTU_ACL_VERSION=2.3.2-1build1.1
ARG UBUNTU_CA_CERTIFICATES_VERSION=20260601~24.04.1
ARG UBUNTU_CURL_VERSION=8.5.0-2ubuntu10.15
ARG UBUNTU_DBUS_VERSION=1.14.10-4ubuntu4.1
ARG UBUNTU_IPTABLES_VERSION=1.8.10-3ubuntu2
ARG UBUNTU_PASSWD_VERSION=1:4.13+dfsg1-4ubuntu3.2
ARG UBUNTU_SYSTEMD_VERSION=255.4-1ubuntu8.17
ARG UBUNTU_SYSTEMD_SYSV_VERSION=255.4-1ubuntu8.17
ARG DOCKER_CE_VERSION=5:29.8.1-1~ubuntu.24.04~noble
ARG CONTAINERD_VERSION=2.3.6-1~ubuntu.24.04~noble
ARG DOCKER_BUILDX_VERSION=0.37.1-1~ubuntu.24.04~noble
ARG DOCKER_COMPOSE_VERSION=5.5.1-1~ubuntu.24.04~noble

ENV DEBIAN_FRONTEND=noninteractive

COPY --chmod=0444 packaging/wsl/apt-delta.lock /tmp/loki-wsl-apt-delta.lock

RUN dpkg-query -W -f='${binary:Package}=${Version}\n' | LC_ALL=C sort > /tmp/loki-wsl-base-packages \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      "acl=$UBUNTU_ACL_VERSION" \
      "ca-certificates=$UBUNTU_CA_CERTIFICATES_VERSION" \
      "curl=$UBUNTU_CURL_VERSION" \
      "dbus=$UBUNTU_DBUS_VERSION" \
      "iptables=$UBUNTU_IPTABLES_VERSION" \
      "passwd=$UBUNTU_PASSWD_VERSION" \
      "systemd=$UBUNTU_SYSTEMD_VERSION" \
      "systemd-sysv=$UBUNTU_SYSTEMD_SYSV_VERSION" \
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
 && dpkg-query -W -f='${binary:Package}=${Version}\n' | LC_ALL=C sort > /tmp/loki-wsl-final-packages \
 && comm -13 /tmp/loki-wsl-base-packages /tmp/loki-wsl-final-packages > /tmp/loki-wsl-apt-delta.actual \
 && comm -23 /tmp/loki-wsl-base-packages /tmp/loki-wsl-final-packages > /tmp/loki-wsl-apt-removed.actual \
 && if ! cmp -s /tmp/loki-wsl-apt-delta.lock /tmp/loki-wsl-apt-delta.actual; then \
      echo "WSL apt package delta changed" >&2; \
      cat /tmp/loki-wsl-apt-delta.actual >&2; \
      exit 1; \
    fi \
 && if test -s /tmp/loki-wsl-apt-removed.actual; then \
      echo "WSL apt transaction removed base packages" >&2; \
      cat /tmp/loki-wsl-apt-removed.actual >&2; \
      exit 1; \
    fi \
 && rm -f /tmp/loki-wsl-base-packages /tmp/loki-wsl-final-packages /tmp/loki-wsl-apt-delta.actual /tmp/loki-wsl-apt-removed.actual /tmp/loki-wsl-apt-delta.lock \
 && rm -f /etc/apt/keyrings/docker.asc /etc/apt/sources.list.d/docker.sources \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/log/apt \
 && rm -f /var/log/dpkg.log /var/log/alternatives.log

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
 && chage --lastday -1 ubuntu \
 && install -d -o ubuntu -g ubuntu -m 0750 /home/ubuntu/workspace

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

FROM scratch
COPY --from=rootfs --exclude=etc/resolv.conf / /

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
