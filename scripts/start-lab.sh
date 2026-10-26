#!/bin/sh
# Real disposable shell on a dedicated tmux socket. Never uses the user's default server.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
LAB="$ROOT/.local/terminal-lab"
mkdir -p "$LAB" "$ROOT/bin"
chmod 700 "$LAB"
(cd "$ROOT/sidecar" && go build -o "$ROOT/bin/trilld" ./cmd/trilld)
if ! tmux -S "$LAB/tmux.sock" has-session -t '=trill-terminal-lab' 2>/dev/null; then
  tmux -S "$LAB/tmux.sock" -f /dev/null new-session -d -s trill-terminal-lab -x 80 -y 24 /bin/zsh -f
fi
printf 'Terminal lab: http://127.0.0.1:7443\nPairing token file: %s/token\nThis is a real shell on an isolated tmux server.\n' "$LAB"
exec "$ROOT/bin/trilld" --listen 127.0.0.1:7443 --token-file "$LAB/token" \
  --insecure-loopback --allow-control --tmux-socket "$LAB/tmux.sock" \
  --mosh-host 127.0.0.1 --udp-ports 60100:60110
