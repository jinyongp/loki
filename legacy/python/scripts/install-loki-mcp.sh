#!/bin/sh
set -eu

REPO_DIR=${1:-$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)}
REPO_DIR=$(CDPATH= cd -- "$REPO_DIR" && pwd)
SOURCE_DIR="$REPO_DIR/legacy/python"

test "$(id -u)" -eq 0
test -f "$SOURCE_DIR/pyproject.toml"
test -d "$REPO_DIR/bundled_skills"
test -f "$REPO_DIR/config/gitconfig"

systemctl stop loki-mcp.service loki-runtime.service \
  loki-browser.service loki-port-guard.service loki-signing-agent.service \
  2>/dev/null || true

if id runner >/dev/null 2>&1; then
  usermod -aG workspace runner
  install -d -o runner -g workspace -m 2770 \
    /srv/workspace/loki/.loki \
    /srv/workspace/loki/.agents \
    /srv/workspace/loki/.agents/skills \
    /srv/workspace/loki/.loki/fnm \
    /srv/workspace/loki/.loki/corepack \
    /srv/workspace/loki/.loki/go \
    /srv/workspace/loki/.loki/cargo \
    /srv/workspace/loki/.loki/cargo/bin \
    /srv/workspace/loki/.loki/rustup \
    /srv/workspace/loki/.loki/pnpm-store \
    /srv/workspace/loki/.pnpm-store
  for cache_dir in \
    /srv/workspace/loki/.loki/corepack \
    /srv/workspace/loki/.loki/go \
    /srv/workspace/loki/.loki/cargo \
    /srv/workspace/loki/.loki/rustup \
    /srv/workspace/loki/.loki/pnpm-store \
    /srv/workspace/loki/.pnpm-store
  do
    chown -R runner:workspace "$cache_dir"
    chmod -R g+rwX "$cache_dir"
    find "$cache_dir" -type d -exec chmod g+s {} +
  done
  if test ! -e /srv/workspace/loki/.agents/skills/devtools; then
    install -d -o runner -g workspace -m 2770 /srv/workspace/loki/.agents/skills/devtools
    install -o runner -g workspace -m 0660 \
      "$REPO_DIR/bundled_skills/devtools/SKILL.md" \
      /srv/workspace/loki/.agents/skills/devtools/SKILL.md
  fi
  if test -x /home/linuxbrew/.linuxbrew/bin/rustup; then
    for proxy in cargo rustc rustdoc rustfmt; do
      ln -sfn "/home/linuxbrew/.linuxbrew/opt/rustup/bin/$proxy" "/srv/workspace/loki/.loki/cargo/bin/$proxy"
      chown -h runner:workspace "/srv/workspace/loki/.loki/cargo/bin/$proxy"
    done
  fi
fi
chmod 2770 /srv/workspace /srv/workspace/loki

install -d -o root -g root -m 0755 /var/lib/loki /var/log/loki /run/loki

if ! id loki-browser >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/loki/browser --shell /usr/sbin/nologin --no-create-home loki-browser
fi
usermod -aG workspace loki-browser
install -d -o loki-browser -g loki-browser -m 0700 /var/lib/loki/browser
install -d -o loki-browser -g loki-browser -m 0700 \
  /var/lib/loki/browser/browser-use-user-data-dir-persistent
install -d -o loki-browser -g workspace -m 2770 /srv/workspace/loki/.browser-downloads

install -d -o root -g root -m 0755 /opt/loki-mcp
rm -rf /opt/loki-mcp/venv
python3 -m venv /opt/loki-mcp/venv
/opt/loki-mcp/venv/bin/python -m pip install --no-cache-dir --upgrade pip
/opt/loki-mcp/venv/bin/python -m pip install --no-cache-dir "$SOURCE_DIR"
install -d -o root -g root -m 0755 /opt/loki-mcp/share /opt/loki-mcp/share/skills
find /opt/loki-mcp/share/skills -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -a "$REPO_DIR/bundled_skills/." /opt/loki-mcp/share/skills/
chown -R root:root /opt/loki-mcp/share/skills
find /opt/loki-mcp/share/skills -type d -exec chmod 0755 {} +
find /opt/loki-mcp/share/skills -type f -exec chmod 0644 {} +

test -x /opt/loki-browser/chromium/chrome-linux64/chrome
rm -rf /opt/loki-browser/venv
python3 -m venv /opt/loki-browser/venv
/opt/loki-browser/venv/bin/python -m pip install --no-cache-dir --upgrade pip
/opt/loki-browser/venv/bin/python -m pip install --no-cache-dir "$SOURCE_DIR/browser_sidecar"

install -d -o root -g workspace -m 0750 /etc/loki
install -d -o root -g root -m 0700 /var/lib/loki/signing
if test ! -f /var/lib/loki/signing/id_ed25519; then
  ssh-keygen -q -t ed25519 -N '' -C 'loki-mcp commit signing' \
    -f /var/lib/loki/signing/id_ed25519
fi
chown root:root /var/lib/loki/signing/id_ed25519 /var/lib/loki/signing/id_ed25519.pub
chmod 0600 /var/lib/loki/signing/id_ed25519
chmod 0644 /var/lib/loki/signing/id_ed25519.pub
install -o root -g root -m 0644 /var/lib/loki/signing/id_ed25519.pub /etc/loki/signing_key.pub
install -o root -g root -m 0644 "$REPO_DIR/config/gitconfig" /etc/loki/gitconfig
git_identity_name=$(runuser -u runner -- env HOME=/home/runner git config --global --includes user.name || true)
git_identity_email=$(runuser -u runner -- env HOME=/home/runner git config --global --includes user.email || true)
if test -z "$git_identity_name" || test -z "$git_identity_email"; then
  echo 'runner Git user.name and user.email must be configured before installing Loki' >&2
  exit 1
fi
git_public_key=$(cat /var/lib/loki/signing/id_ed25519.pub)
printf '%s %s\n' "$git_identity_email" "$git_public_key" > /etc/loki/allowed_signers
chown root:root /etc/loki/gitconfig /etc/loki/allowed_signers /etc/loki/signing_key.pub
chmod 0644 /etc/loki/gitconfig /etc/loki/allowed_signers /etc/loki/signing_key.pub
install -d -o root -g root -m 0755 /usr/share/doc/loki-mcp
install -o root -g root -m 0644 "$SOURCE_DIR/config/loki-mcp.toml" /usr/share/doc/loki-mcp/config.example.toml
if test ! -f /etc/loki/config.toml; then
  install -o root -g root -m 0644 "$SOURCE_DIR/config/loki-mcp.toml" /etc/loki/config.toml
fi
if ! grep -q '^artifact_base_url = ' /etc/loki/config.toml; then
  loki_artifact_line=$(sed -n '/^artifact_base_url = /{p;q;}' "$SOURCE_DIR/config/loki-mcp.toml")
  if test -n "$loki_artifact_line"; then
    loki_config_temp=$(mktemp /etc/loki/config.toml.XXXXXX)
    awk -v artifact_line="$loki_artifact_line" '
      { print }
      /^public_hosts = / { print artifact_line }
    ' /etc/loki/config.toml > "$loki_config_temp"
    install -o root -g root -m 0644 "$loki_config_temp" /etc/loki/config.toml
    rm -f "$loki_config_temp"
  fi
fi
if ! grep -q '^preview_base_domain = ' /etc/loki/config.toml; then
  loki_preview_line=$(sed -n '/^preview_base_domain = /{p;q;}' "$SOURCE_DIR/config/loki-mcp.toml")
  if test -n "$loki_preview_line"; then
    loki_config_temp=$(mktemp /etc/loki/config.toml.XXXXXX)
    awk -v preview_line="$loki_preview_line" '
      { print }
      /^artifact_base_url = / { print preview_line }
    ' /etc/loki/config.toml > "$loki_config_temp"
    install -o root -g root -m 0644 "$loki_config_temp" /etc/loki/config.toml
    rm -f "$loki_config_temp"
  fi
fi
if test ! -f /etc/loki/token; then
  /opt/loki-mcp/venv/bin/python -c 'import secrets; print(secrets.token_urlsafe(48))' > /etc/loki/token
fi
chown root:workspace /etc/loki/token
chmod 0640 /etc/loki/token

install -d -o runner -g workspace -m 0700 /var/log/loki/mcp
chown -R runner:workspace /var/log/loki/mcp
install -d -o root -g workspace -m 2710 /var/lib/loki/project-state
python3 "$SOURCE_DIR/scripts/repair-task-metadata.py"
install -d -o root -g root -m 0700 /var/lib/loki/runtime
install -d -o root -g root -m 0700 /var/lib/loki/runtime/inbox
install -d -o root -g root -m 0700 /var/log/loki/runtime
install -d -o runner -g runner -m 0700 \
  /var/lib/loki/local-deploy \
  /var/lib/loki/local-deploy/materializations \
  /var/lib/loki/local-deploy/sessions \
  /var/lib/loki/local-deploy/snapshots
install -d -o runner -g runner -m 0700 /home/runner/.config/gh
if test ! -f /home/runner/.gitconfig; then
  install -o runner -g runner -m 0600 /dev/null /home/runner/.gitconfig
fi
install -d -o root -g root -m 0755 /usr/local/libexec
install -d -o root -g root -m 0755 /usr/local/bin
install -o root -g root -m 0644 "$SOURCE_DIR/scripts/loki-environment.sh" /etc/profile.d/loki-environment.sh
rm -f /etc/profile.d/loki-fnm.sh
if id runner >/dev/null 2>&1; then
  sed -i '\|^\. /etc/profile\.d/loki-fnm\.sh$|d' /home/runner/.bashrc
fi
if id runner >/dev/null 2>&1 && ! grep -qxF '. /etc/profile.d/loki-environment.sh' /home/runner/.bashrc; then
  printf '\n. /etc/profile.d/loki-environment.sh\n' >> /home/runner/.bashrc
  chown runner:runner /home/runner/.bashrc
fi
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-mcp-launch" /usr/local/libexec/loki-mcp-launch
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki.py" /usr/local/bin/loki
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-action-runner.py" /usr/local/libexec/loki-action-runner
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-project-bootstrap.py" /usr/local/libexec/loki-project-bootstrap
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-task" /usr/local/libexec/loki-task
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-docker-action-runner.py" /usr/local/libexec/loki-docker-action-runner
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-signing-agent-launch" /usr/local/libexec/loki-signing-agent-launch
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-signing-agent-proxy.py" /usr/local/libexec/loki-signing-agent-proxy
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-browser-launch" /usr/local/libexec/loki-browser-launch
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-cloudflare-jwks-refresh" /usr/local/libexec/loki-cloudflare-jwks-refresh
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-egress-proxy" /usr/local/libexec/loki-egress-proxy
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-policy" /usr/local/sbin/loki-policy
install -o root -g root -m 0755 "$SOURCE_DIR/scripts/loki-checkpoint" /usr/local/sbin/loki-checkpoint
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-mcp.service" /etc/systemd/system/loki-mcp.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-runtime.service" /etc/systemd/system/loki-runtime.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-signing-agent.service" /etc/systemd/system/loki-signing-agent.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-cloudflare-jwks.service" /etc/systemd/system/loki-cloudflare-jwks.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-cloudflare-jwks.timer" /etc/systemd/system/loki-cloudflare-jwks.timer
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-egress-proxy.service" /etc/systemd/system/loki-egress-proxy.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-port-guard.service" /etc/systemd/system/loki-port-guard.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-browser-proxy.service" /etc/systemd/system/loki-browser-proxy.service
install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-browser.service" /etc/systemd/system/loki-browser.service
if command -v cloudflared >/dev/null 2>&1 && id loki-tunnel >/dev/null 2>&1; then
  install -o root -g root -m 0644 "$SOURCE_DIR/systemd/loki-cloudflared.service" /etc/systemd/system/loki-cloudflared.service
fi

systemctl daemon-reload
systemctl enable --now loki-signing-agent.service
systemctl restart loki-signing-agent.service
systemctl enable --now loki-runtime.service
systemctl restart loki-runtime.service
systemctl start loki-cloudflare-jwks.service
systemctl enable --now loki-cloudflare-jwks.timer
systemctl enable --now loki-egress-proxy.service
systemctl restart loki-egress-proxy.service
systemctl enable --now loki-port-guard.service
systemctl restart loki-port-guard.service
systemctl enable --now loki-browser-proxy.service
systemctl restart loki-browser-proxy.service
systemctl enable --now loki-browser.service
systemctl restart loki-browser.service
systemctl enable loki-mcp.service
systemctl restart loki-mcp.service
if test -f /etc/loki/cloudflared/token.env; then
  systemctl enable --now loki-cloudflared.service
  systemctl restart loki-cloudflared.service
fi
