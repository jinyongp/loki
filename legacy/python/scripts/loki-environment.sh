export FNM_DIR=/srv/workspace/loki/.loki/fnm
export CARGO_HOME=/srv/workspace/loki/.loki/cargo
export RUSTUP_HOME=/srv/workspace/loki/.loki/rustup
export PATH="$CARGO_HOME/bin:$PATH"
if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then
  eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"
fi
if [ -x /home/linuxbrew/.linuxbrew/bin/fnm ]; then
  eval "$(/home/linuxbrew/.linuxbrew/bin/fnm env --use-on-cd --shell bash)"
fi
if command -v zoxide >/dev/null 2>&1; then
  eval "$(zoxide init bash)"
fi
