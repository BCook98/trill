# Trill sidecar

`trill serve` runs the private `trilld` daemon, which implements the [HTTPS API](../docs/API.md) using installed Xirp or Herdr providers and `mosh-server`. Xirp terminals attach through tmux; Herdr terminals use its direct client. Builds require Go 1.26.10 or a newer patched release; the setup CLI also uses a pinned QR encoder.

For prerequisites (including provider installation, `mosh-server`, and `tmux`), app installation, pairing, updates, and removal, start with the [setup guide](../docs/SETUP.md). App controls are in the [usage guide](../docs/USAGE.md); development and isolated probes are in [DEVELOPMENT.md](../docs/DEVELOPMENT.md).

For the installed `trill` command on PATH, run `../scripts/setup-companion.sh` from this directory. Use `../scripts/setup-companion.sh --help` to build/update binaries without configuring or starting services. Management commands include `trill setup`, `trill pair`, `trill status`, and `trill control --enable`; see [setup details](../docs/SETUP.md).

`trill version` reports the companion build, and `trill doctor` diagnoses the installation without changing it. Signed installations support `trill update --check` and `trill update`; source installations continue using the script above. See [companion distribution](../docs/COMPANION_RELEASES.md) for release setup, trust checks and recovery. Signing credentials are not yet configured in the repository.

For a local development build, keep the CLI and its private sibling together:

```sh
mkdir -p /tmp/trill-build
go build -o /tmp/trill-build/trill ./cmd/trillctl
go build -o /tmp/trill-build/trilld ./cmd/trilld
```

Run `/tmp/trill-build/trill --help` or `/tmp/trill-build/trill serve --help` to inspect the local build. Installed examples below use `trill` on PATH. `serve` executes the sibling daemon directly without a shell, preserving arguments, standard streams, signals, and exit status. Existing LaunchAgents can continue using the private `trillctl` and `trilld` paths.

Normal mode reads Xirp session and project metadata through `xirp session list --all --json` and `xirp project list --json`. It starts read-only. With `--allow-control`, session creation and lifecycle actions use fixed Xirp daemon API messages. Ended `completed` and `failed` sessions use `originalCliSessionId` or, if absent, `cliSessionId` for resume. Mosh bootstrap checks the authoritative `tmuxSession` from Xirp metadata and then checks that exact tmux target before starting `mosh-server`. No HTTP endpoint accepts a command, transcript, terminal input, or arbitrary daemon API message.

Create accepts an optional name and a UTF-8 goal of at most 8000 bytes, including line breaks. It asks Xirp for terminal mode. Coding-session creation can select a new worktree or the project folder; plain terminal creation uses the project folder. Each mutation is sent once. The sidecar returns `{ok:true}` only after a readback confirms the target session state. If the outcome cannot be confirmed, it returns HTTP 409 with an error asking the client to refresh sessions before retrying. For create, the Xirp response must identify the new session so a readback can be correlated; a pending or uncorrelated result is reported as unconfirmed.

For a network listener, supply a certificate and key and a reachable mosh bind address:

```sh
trill serve --listen 192.0.2.10:7443 --cert /absolute/path/cert.pem --key /absolute/path/key.pem --token-file /absolute/path/token --mosh-host 192.0.2.10 --udp-ports 60000:60010
```

Add `--allow-control` only when the client should control sessions and bootstrap mosh. The token file is generated on first launch with 48 random bytes encoded as hex and mode 0600. Existing token files must be regular 0600 files containing the same 96-character hex format. The token is never printed by the server. All API routes, including health, require `Authorization: Bearer <token>`. HTTP without TLS is permitted only with `--insecure-loopback` on a loopback IP address. The default listener is `127.0.0.1:7443`; the default mosh host is `127.0.0.1`.

## Session providers

The default backend is Xirp. `--backends xirp,herdr` combines Xirp with Herdr's `default` server; `--backends herdr` uses Herdr alone. `--herdr-sessions default,work` selects existing servers and `--herdr-path` chooses the installed CLI. Managed setup persists the same options, and `trill providers --backends ...` updates an existing installation without re-pairing. See [provider behavior and compatibility](../docs/PROVIDERS.md).

Catalogs return per-item capabilities and per-provider availability. A failed provider does not hide a healthy one's sessions. Herdr supports direct terminal attachment, native rename, and native terminal creation; unsupported history/lifecycle actions are not routed to Xirp. Native mutations are never automatically retried.

## Isolated terminal lab

`--tmux-socket` selects an explicit, absolute tmux socket and switches the entire backend to a real terminal lab. It lists only sessions on that socket, under project “Terminal lab” and agent “Shell.” It does not read Xirp metadata. Create, rename, stop, and resume return an unsupported error in this mode. A client can bootstrap mosh into a listed lab tmux session with `--allow-control`; lease deletion stops only that spawned mosh server. The sidecar never kills the tmux session.

Create a fresh socket and session yourself, then run the lab server:

```sh
mkdir -p /tmp/trill-lab
tmux -S /tmp/trill-lab/tmux.sock new-session -d -s terminal-probe
trill serve --listen 127.0.0.1:7443 --token-file /tmp/trill-lab/token --insecure-loopback --allow-control --tmux-socket /tmp/trill-lab/tmux.sock --mosh-host 127.0.0.1 --udp-ports 60000:60010
```

Only the socket you pass is used by lab tmux commands. Keep it separate from the normal tmux socket. Use a different `--listen` port if 7443 is in use. The mosh client receives `{host,port,key,leaseId}` from `POST /v1/sessions/{id}/mosh`; the key belongs in client memory, and `DELETE /v1/leases/{leaseId}` releases the server. `MOSH_SERVER_NETWORK_TMOUT` and `MOSH_SERVER_SIGNAL_TMOUT` are set to 600 seconds. Mosh itself exits if no client connects within 60 seconds. The sidecar also closes leases after `--lease-ttl` (default 12 hours) or on clean shutdown.

Checks:

```sh
go test -race ./...
go vet ./...
go build -o /tmp/trill-build/trill ./cmd/trillctl
go build -o /tmp/trill-build/trilld ./cmd/trilld
```

To exercise the real mosh launcher and PID cleanup, run `TRILL_REAL_MOSH_TEST=1 go test -race -run TestRealMoshOnIsolatedTmuxSocket ./...`. That opt-in test creates and destroys its own temporary tmux socket and leaves the normal socket untouched.
