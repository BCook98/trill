# Herdr dual-support prototype

Tested on 24 October 2026, macOS arm64, Herdr 0.9.5 (protocol 22), using the installed Homebrew mosh client/server. The integration includes an opt-in Go sidecar backend, iOS capability gating, and a probe using the same Swift MoshCore library as the app. Xirp remains the default backend.

**Finding: tmux is not required for Trill's mosh transport.** Launching `herdr terminal attach` under `mosh-server` works. The normal desktop Herdr TUI can remain connected and type into the same terminal. The one-controller restriction applies to competing direct attachments; it did not exclude the desktop TUI in this experiment.

## Reproduce

Prerequisites: macOS or Linux, Python 3.9+, `mosh-server` and `mosh-client` on PATH, and a Herdr binary. No coding agent, account, or running personal Herdr server is needed. The recorded run used the official macOS arm64 0.9.5 release:

```sh
curl -fL https://github.com/herdrdev/herdr/releases/download/v0.9.5/herdr-macos-aarch64 -o /tmp/trill-herdr-0.9.5
shasum -a 256 /tmp/trill-herdr-0.9.5
# fb77c2232a05a8035421382c894392f4174e4e38a18e48af3dd0290157c3a7e2
chmod +x /tmp/trill-herdr-0.9.5
python3 prototypes/herdr/probe.py --herdr /tmp/trill-herdr-0.9.5
```

The probe creates a fresh directory under `/tmp`, sets isolated XDG config/state directories, removes inherited Herdr/Xirp/tmux routing variables, disables update checks, and starts a named headless server with a plain `/bin/sh` workspace. It opens artificial PTYs for desktop/direct/mosh clients. All input and shutdown operations target this disposable server. It leaves diagnostic metadata/logs in its temporary directory and writes a JSON report, without recording mosh keys. Temporary processes are cleaned up on normal completion or a caught failure. A forced termination of the harness can interrupt cleanup.

The mosh listener binds only to `127.0.0.1`, using UDP 60120–60130. Run one probe at a time. No tmux server is started or used.

## Observed results

All 13 checks passed in the 24 October 2026 run. Keep generated reports, including temporary paths and session identifiers, in ignored local storage:

| Area | Evidence |
| --- | --- |
| Metadata | Real workspace/pane listings and snapshot expose pane and terminal IDs. |
| Desktop coexistence | Desktop TUI and direct attach can both send input; desktop input also appears through mosh. |
| Terminal transport | `mosh-client → mosh-server → herdr terminal attach → original shell` handles input/output. |
| Resize | `stty size` in the original shell reports 18×62 through direct attach and 16×55 through mosh. |
| Ownership | A second direct attach exits with the expected ownership error; no takeover is requested. |
| Detach | Ctrl+B, q exits the direct client; reconnect retains a shell variable. |
| Desktop handoff | The current probe measures the synthetic shell’s PTY before attachment and after release, checking that Herdr restores desktop dimensions automatically. |
| Lease replacement | SIGTERM to the old mosh server leaves the terminal alive; a fresh mosh lease attaches without takeover and reads the same shell variable. |

The output is Herdr's rendered ANSI surface. This is a real interactive shell connection, but it is not a raw replay of the application's original PTY output. The harness drains terminal output during detach; waiting for exit without draining the artificial PTY caused an early harness failure. Assertions remove ANSI and spaces because Herdr can position runs of characters with cursor commands instead of emitting intervening spaces.

## Production integration and reproduction

The provider implementation now supports combined catalogs, persisted setup, per-item capabilities, native Herdr rename and terminal creation, protocol compatibility, partial failures, and occupied-terminal checks. See [provider configuration and supported actions](../../docs/PROVIDERS.md). The recorded results remain specific to Herdr 0.9.5 on macOS arm64.

To build and run the integration check after obtaining the binary above:

```sh
(cd sidecar && go build -o ../.local/herdr-prototype/trilld ./cmd/trilld)
swift build --package-path tools/TransportProbe --product HerdrTransportProbe
python3 prototypes/herdr/through-sidecar.py --herdr /tmp/trill-herdr-0.9.5 --agent codex
```

Omit `--agent codex` for the shell-only transport check. `--agent claude` is also available with authorized credentials. The recorded run completed Codex's response/reconnect check; Claude's response/reconnect check remains unverified.

The harness starts temporary Herdr state and two loopback companion processes. The production companion combines read-only Xirp metadata with the disposable Herdr server and an intentionally missing named server. It deliberately gives companion processes a different XDG environment while pinning the selected Herdr location. It verifies partial availability, renames the disposable terminal, creates and verifies a new native terminal, then closes only that new pane. The lab companion confines all sessions to the disposable directory and tests an occupied external direct controller before running Swift transport checks. It never mutates an existing Xirp session.

Swift uses the real Go lease runner and the same MoshCore actor as iOS. Checks cover input/output, exact resize to 31×93, suspend/resume, lease replacement with shell state preserved, full-screen Codex bracketed-paste prompt/reply before and after reconnect, and lease cleanup. The report records only check results and local diagnostic paths, not mosh keys or bearer tokens.

All temporary clients and servers are cleaned up on normal completion or caught failure; forced termination can interrupt cleanup. Logs remain under the reported temporary directory. Concurrent probe runs need different UDP ranges (60140–60150 and 60160–60170).

The probe is not a physical iPhone test. Network roaming, packet loss, UIKit terminal behavior, and the full keyboard/scrolling matrix still need release validation. Trill never adds `--takeover`; a client that acquires the terminal after preflight remains authoritative.

Use the generated Go/Swift run report to inspect the results of a new run; keep it in ignored local storage.

## Sources

- [Herdr CLI reference](https://herdr.dev/docs/cli-reference/): direct attach and terminal control commands.
- [Herdr socket API](https://herdr.dev/docs/socket-api/): metadata, events, methods, and protocol compatibility.
- [Herdr persistence/remote documentation, pinned 0.9.0](https://github.com/herdrdev/herdr/blob/v0.9.5/docs/versions/0.9.0/website/src/content/docs/persistence-remote.mdx): terminal attachment and observer semantics.
- [Herdr 0.9.5 server source](https://github.com/herdrdev/herdr/blob/v0.9.5/src/server/headless.rs): direct attachment ownership and resize locks.
- [Herdr 0.9.5 release](https://github.com/herdrdev/herdr/releases/tag/v0.9.5): tested binary. Its checksum matched the release manifest at `https://herdr.dev/latest.json` when downloaded.


### Paired reconnect regression

The sidecar probe also creates two disposable paired devices using the real pairing store. It deliberately leaves one Mosh connection unreleased, then reconnects as the same device and verifies that a shell variable survives. The other device must receive HTTP 409. This exercises recovery after app termination or a lost release request without `--takeover`; it requires Go on PATH for the private demo invitation helper.
