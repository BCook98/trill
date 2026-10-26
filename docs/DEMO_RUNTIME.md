# Run the scripted demo server

The demo uses Trill's real HTTPS API, pairing store, file/upload/preview handlers and upstream Mosh transport. Its projects, sessions and terminal commands are synthetic. No Xirp, Herdr, tmux, coding agent, AI account or shell is started. It cannot establish that real-provider integration works.

## Local setup

Install Go using the [development toolchain](DEVELOPMENT.md), and install `mosh-server`. The API smoke harness also needs `mosh-client`. Build the single Go executable:

```sh
GOTOOLCHAIN=go1.26.10 sh scripts/build-demo.sh
```

Start a foreground sandbox in a new, short, absolute directory. This directory is owned by the demo; don't point it at a project or an installed companion's configuration. Existing directories without a matching ownership marker are rejected.

```sh
.build/demo/trill demo serve --state-dir /tmp/trill-review-demo
```

Defaults: HTTPS on 127.0.0.1:19443, fixture preview on 127.0.0.1:18080, Mosh UDP 60300–60310, mixed provider capabilities, two active terminal leases. A private self-signed certificate is generated and pinned through ordinary pairing. The process stays in the foreground. If a port is occupied, choose another; the demo never stops an existing listener.

In another terminal:

```sh
.build/demo/trill demo pair --state-dir /tmp/trill-review-demo
# For a simulator, paste this private link through the native pairing flow:
.build/demo/trill demo pair --state-dir /tmp/trill-review-demo --url
```

Each command creates a separate single-use ten-minute invitation. A permanent bearer token is never printed. The demo starts with control enabled; `serve --read-only` exercises server-side read-only restrictions. Phone consent remains unchanged.

For a physical iPhone, loopback is not reachable. Restart only this sandbox with explicit LAN listen/advertised/Mosh addresses and use its new pairing invitation. Use a fresh state directory or a matching supplied certificate if the hostname changes. For example, replace 192.0.2.10 with an address actually assigned to the Mac:

```sh
.build/demo/trill demo serve --state-dir /tmp/trill-phone-demo \
  --listen 192.0.2.10:19443 --address https://192.0.2.10:19443 \
  --mosh-host 192.0.2.10 --mosh-bind 192.0.2.10
```

## Try the app

Select a demo session, approve terminal access, then type `help`. Use Write → Insert to insert text without executing it; Return submits it. Available commands include:

```text
help
ls
cat README.md
demo heading Hello from my phone
demo task
clear
```

`demo heading` changes the real fixture file `web/index.html`. Browse that file and choose its static preview, or enter `localhost:18080` in Browser for the interactive website. The live website has navigation, a counter and WebSocket updates. Files includes generated image/video media. The demo accepts images up to 8 megapixels and 10 MiB, within a shared 64 MiB upload quota. Trill already resizes photos to a 2048-pixel longest edge before uploading. Upload a sample photo through Write and use `demo photo <uploaded-path>` to acknowledge it; paths outside that session's upload directory are rejected. No command is passed to a shell, and unsupported input returns help.

The terminal child handles literal bracketed paste, Unicode editing, Ctrl/Shift/special-key diagnostics, resize and redraw. See [terminal behavior](../sidecar/demoterm/README.md). A new Mosh lease restores the parent-owned screen and project state. A disconnected scripted task keeps progressing in the parent. Restarting the sandbox preserves its data and registrations until explicit reset or idle expiry.

Choose `--profile xirp`, `--profile herdr` or `--profile mixed` when creating the sandbox. These are capability fixtures, not provider installations. Xirp-style sessions offer creation/worktree/archive/stop/resume actions; Herdr-style sessions offer terminal creation and rename. Synthetic worktrees are separate fixture directories, not Git worktrees.

## Inspect, inject faults and clean up

```sh
.build/demo/trill demo status --state-dir /tmp/trill-review-demo
.build/demo/trill demo status --state-dir /tmp/trill-review-demo --json
.build/demo/trill demo fault capacity --state-dir /tmp/trill-review-demo
.build/demo/trill demo fault clear --state-dir /tmp/trill-review-demo
.build/demo/trill demo stop --state-dir /tmp/trill-review-demo
.build/demo/trill demo reset --state-dir /tmp/trill-review-demo --yes
```

`stop` uses the private owned Unix socket. It never sends a signal to an arbitrary PID. `reset` requires the server to be stopped, removes only owned fixture data/registrations/uploads and preserves the TLS identity. Neither command changes the installed companion or user sessions.

Faults are controlled only through local IPC:

| Fault | Behavior |
| --- | --- |
| `busy`, `capacity` | Next terminal bootstrap returns the corresponding production error. |
| `create-unconfirmed` | Creates the synthetic session, then returns an unconfirmed result; refresh can find it without creating a duplicate. |
| `provider-unavailable` | Hides Herdr data; the Xirp side of a mixed catalog remains available. |
| `preview-expired` | Revokes this sandbox’s preview grants so the app must renew them. |
| `drop-leases` | Stops only this sandbox's tracked Mosh connections; session state survives. |
| `api-unavailable` | Authenticated API requests return 503 until cleared. |
| `preview-unavailable` | Fixture upstream returns 503 until cleared. |
| `upload-rejected` | Uploads return the storage-quota error until cleared. |
| `invitation-expired` | Next invitation is issued with an elapsed lifetime through the real pairing store. |
| `bootstrap-delayed` | Next authenticated bootstrap waits three seconds, cancellably. |
| `clear` | Removes armed faults. |

A local HTTP error is not a packet-loss test. Network interruption and cellular roaming need the separate test arrangements described in the scope.

The default 24-hour idle lifetime is configurable with `--retention` (minimum 1 hour). Authenticated use and active terminal polling keep it alive. Inactivity clears owned data and exits; the hosting template restarts a fresh environment. Explicit stop preserves data. Restart alone does not renew inactivity retention. Cleanup errors fail closed rather than claiming deletion.

## Automated checks

Build first, then run the transport/API smoke harness. The output path must be new:

```sh
python3 scripts/demo-api-smoke.py --trill "$PWD/.build/demo/trill" \
  --output "$PWD/.local/demo-api-smoke.json"
```

The harness creates its own private sandbox and free ports, validates each TLS connection against the exact fixture certificate, pairs real devices, invokes lifecycle actions, types through a real `mosh-client`, checks Insert-versus-Return semantics and reconnect persistence, uploads synthetic media, exercises the proxy, and tests failures/revocation. It stops only its own processes. The JSON report contains the executable hash and explicit outcomes; temporary state and server logs remain private for inspection. It does not drive iOS or prove provider behavior.

Run Go checks from `sidecar`:

```sh
GOTOOLCHAIN=go1.26.10 go test ./...
GOTOOLCHAIN=go1.26.10 go vet ./...
GOTOOLCHAIN=go1.26.10 go test -race ./demoapp ./demoterm .
```

Use the [native smoke runner](DEMO_SMOKE.md) to drive the existing iOS app through IDB. Native UI automation, real-provider integration, and physical-device release checks have separate results. A typed text test does not prove dictation, and simulator recovery does not prove physical iOS suspension or cellular roaming.

## Reviewer hosting

[Fly.io deployment](../deploy/fly/README.md) runs the reviewer sandbox on one small machine with managed HTTPS and a dedicated Mosh IPv4. Public transport qualification and the command for repeating it are recorded in that guide. The [generic Linux templates](../deploy/demo/README.md) are an alternative for a host you manage. Both provide a private invitation page. Publishing reviewer credentials in App Store Connect is a separate step. A host must carry both HTTPS and Mosh UDP. Supply reviewer credentials privately through App Store Connect and explain the synthetic backend. See the [scope and coverage matrix](DEMO_SERVER.md) for what remains separate from this fixture.

Fly deployments use an explicit `--fly-edge-tls` mode. Fly Proxy terminates HTTPS on port 443 and forwards to the private HTTP listener on port 8080. The app trusts that fixed deployment topology and the private Fly network; it never derives its origin from forwarding headers. Port 8080 must not be exposed publicly. Pairing uses the platform-trusted Fly certificate, so the invitation fingerprint is empty. Local sandboxes continue to use pinned TLS.
