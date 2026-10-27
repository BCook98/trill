# Set up Trill

Trill has two parts: an iPhone app and a companion running as your user on the Mac that owns your sessions. Start with [prerequisites](#prerequisites-and-supported-versions), then [install the app](#install-the-iphone-app), [install the companion](#install-the-mac-companion), and [pair](#pair-your-iphone). Day-to-day controls are in the [usage guide](USAGE.md).

## Prerequisites and supported versions

| Component | Requirement |
| --- | --- |
| iPhone | iOS 18.0 or later, the project's deployment target. Duo-specific APIs are guarded for newer iOS versions. |
| App build tools | Xcode 27.1 or later with the iOS 27.1 SDK. This build requirement is separate from the minimum phone OS. |
| Companion host | macOS, with a logged-in user session for LaunchAgents. The repository does not declare a separate minimum macOS version; your Xcode and dependencies may require a newer release. |
| Go | 1.26.10 or a newer patched release to build the companion, as declared in `sidecar/go.mod`. Go's automatic toolchain selection can download the required version; `GOTOOLCHAIN=local` rejects older compilers. |
| Terminal transport | `mosh-server` on the Mac, provided by the Mosh package. Trill includes its own iOS Mosh client. |
| Xirp backend | Installed Xirp app and `xirp` CLI, a running provider, and `tmux` on PATH. Sessions must expose a live tmux terminal to attach. No minimum Xirp release is pinned; compatibility depends on its CLI metadata and daemon API. |
| Herdr backend (optional) | Installed `herdr` CLI and running selected servers. Direct attach requires matching CLI/server protocols, at least protocol 22. Herdr 0.9.5 is the version exercised by this repository's live probes, not a guarantee for every later release. |
| Network | Reachability from iPhone to Mac on HTTPS TCP 7443 and Mosh UDP 60000 to 60010. Choose LAN or an already configured Tailscale network. |

Full Xcode is needed to build the iOS app, not to run the installed Go companion. The [development guide](DEVELOPMENT.md) covers builds and tests; the [release guide](RELEASING.md) covers distribution.

### Install Go, Mosh, and tmux

Install [Go from the official instructions](https://go.dev/doc/install). If you use [Homebrew](https://brew.sh/), install the Mac terminal dependencies with:

```sh
brew install mosh tmux
go version
command -v mosh-server
command -v tmux
```

These package commands follow the official [Mosh installation guide](https://mosh.org/#getting) and [Homebrew tmux formula](https://formulae.brew.sh/formula/tmux). **The Mac also needs `mosh-server`.** Installing the iPhone app alone does not provide it. Xirp attachment also needs `tmux`; Herdr uses its own direct-terminal client instead of tmux.

Trill setup builds Trill's binaries. It does not install Go, Mosh, tmux, providers, or coding-agent CLIs.

### Install and prepare a provider

Choose one provider or both. Install and authenticate any coding agents through the provider's onboarding or their own installers on the Mac; Trill does not supply agent accounts or credentials.

**Xirp (default).** Download the macOS app from the official [Xirp download page](https://xirp.spotify.com/join-beta), or use its published installer:

```sh
curl -fsSL https://xirp.spotify.com/install.sh | sh
```

Open Xirp and complete its [getting-started flow](https://backstage.spotify.com/docs/xirp). Add a local project and start a terminal-backed session. Ensure its installed CLI is on PATH:

```sh
command -v xirp
xirp session list --all --json
xirp project list --json
```

Those listing commands are the metadata calls Trill uses. A project or transcript without a running terminal cannot be attached until the provider starts or resumes it.

**Herdr (optional).** Follow the official [Herdr installation guide](https://herdr.dev/docs/install/). With Homebrew:

```sh
brew install herdr
```

Start `herdr` from your project directory and follow its [quick start](https://herdr.dev/docs/quick-start/) to create a workspace and terminal. Keep its server running. Inspect the existing server names with:

```sh
command -v herdr
herdr session list --json
```

Trill selects Herdr's `default` server unless you name others. It never starts, updates, restarts, or takes over a Herdr server. See [provider configuration and compatibility](PROVIDERS.md) for named servers and the supported actions.

## Install the iPhone app

From a checkout of this repository, open `ios/Trill.xcodeproj` in **Xcode 27.1+**, select the **Trill** scheme, choose your iPhone or simulator, and Run. Use your own signing team for a physical device; signing and distribution details belong in [RELEASING.md](RELEASING.md).

Swift Package Manager resolves pinned remote dependencies; SwiftTerm and swift-mosh are vendored in the checkout. Xcode may ask you to trust SwiftTerm's build-info package plugin. Inspect that plugin before allowing it. No project regeneration is required for a normal build.

To check a simulator build from the repository root:

```sh
./scripts/build-ios.sh
```

The script requires the iOS 27.1+ simulator SDK and builds into `.build/ios`; it does not install or launch the app. It honors `DEVELOPER_DIR` and otherwise uses the active `xcode-select` installation. For an Xcode installed elsewhere:

```sh
DEVELOPER_DIR="/Applications/Xcode.app/Contents/Developer" ./scripts/build-ios.sh
```

This selects tools only for that command; it does not change global `xcode-select`.

## Choose a network

| Mode | Before setup | Address used by the phone |
| --- | --- | --- |
| LAN | Put both devices on the same reachable network, allow Trill Local Network access, and ensure Bonjour/mDNS works. | `https://my-mac.local:7443`, with the certificate pin included in pairing. |
| Tailscale | Install and connect Tailscale on both devices, enable tailnet HTTPS certificates, and allow the phone to reach the Mac. | `https://machine.tailnet.ts.net:7443`, using a system-trusted certificate. |

Setup defaults to `--network auto`: it prefers an **already connected** Tailscale node and otherwise detects an active LAN interface. Setup does not sign into Tailscale, reconnect it, change accounts or policy, or enable Serve/Funnel. There is no Trill public tunnel.

For Tailscale, follow the official [HTTPS certificate prerequisites](https://tailscale.com/docs/how-to/set-up-https-certificates). The local CLI must be allowed to export a certificate. Publicly issued machine hostnames appear in certificate transparency logs. Setup verifies the certificate chain, hostname, and validity before omitting a pin. Export or trust failure stops setup rather than substituting a self-signed certificate.

### Use a second tailnet

Setup uses the `tailscale` CLI on PATH, then the Tailscale app. To use a different tailnet, such as a second `tailscaled` with its own socket, name its CLI and socket:

```sh
./scripts/setup-companion.sh --tailscale-cli /path/to/tailscale --tailscale-socket /path/to/tailscaled.sock
```

Either flag selects Tailscale. Renewal uses the same CLI and socket. Certificate export requires that `tailscaled` to run with `--statedir`.

If the node's tailnet IP is not assigned to a Mac interface, as with `--tun=userspace-networking`, the companion and `mosh-server` listen on `127.0.0.1` for both IPv4 and IPv6 tailnet addresses. The phone still connects to the tailnet hostname and IP, and `tailscaled` forwards that traffic to loopback.

For LAN, setup reads the Mac's Bonjour name and creates a certificate for its `.local` hostname and selected IP. Use the generated hostname: self-signed numeric-IP HTTPS can be rejected by iOS before pinning. The hostname must actually resolve from the phone. Guest Wi-Fi isolation and VPN DNS can prevent that.

Allow TCP **7443** and UDP **60000 to 60010** through applicable Mac firewall, LAN, and tailnet rules. HTTPS reachability alone does not prove that the Mosh UDP path works. Trill does not edit firewall rules.

## Install the Mac companion

Clone the repository if you have not already:

```sh
git clone https://github.com/BCook98/trill.git
cd trill
```

From its root, run setup as the user who owns the provider sessions:

```sh
./scripts/setup-companion.sh
```

For Herdr alone or both providers, choose the corresponding command **instead**:

```sh
./scripts/setup-companion.sh --backends herdr
# Or:
./scripts/setup-companion.sh --backends xirp,herdr
```

To explicitly select a network or LAN interface on first setup:

```sh
./scripts/setup-companion.sh --network lan
./scripts/setup-companion.sh --network lan --ip 192.168.1.20
./scripts/setup-companion.sh --network lan --hostname my-mac.local
./scripts/setup-companion.sh --network tailscale
```

The IP override must belong to an active interface; a Tailscale override must belong to the connected node. `--hostname` is LAN-only. Combine your chosen network and provider flags in the same initial command.

Setup builds the public `trill` CLI, private `trilld` daemon, and legacy private `trillctl` compatibility binary in `~/.local/share/trill/bin`. It exposes only the `~/.local/bin/trill` symlink, creates private state, provisions HTTPS, and installs/starts two user LaunchAgents: the companion and certificate renewal. It downloads a pinned Go QR encoder during the build; pairing QR contents stay local.

Keep the public command directory on PATH:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Add that line to your shell profile if needed; the installer does not edit profiles. Setup preserves a dependency PATH for launchd, including the invoking PATH, `~/.local/bin`, and standard Homebrew locations.

Use `TRILL_BIN_DIR` with an absolute path to customize the binary directory. The public symlink then points there. Foreign public links/files and binary directories without Trill's ownership marker are refused. Keep the same override when updating.

### Resume an interrupted setup

Rerun setup with the **same state directory**. Valid saved settings, TLS identity, control choice, and pairings are reused. Setup can finish missing owned service installation and start unloaded agents; already loaded companions are not restarted. To skip another invitation:

```sh
trill setup --no-pair
```

`--no-start` creates or validates configuration without changing services; `--no-pair` skips invitation creation. If pairing was cancelled, use `trill pair` later.

Only explicitly supplied flags are compared with saved settings. Conflicting flags fail before service/invitation changes. Remove them to resume; use the dedicated control/provider commands below to change those settings. `--network auto` on a rerun reuses the saved network.

## Pair your iPhone

1. At setup's local prompt on the Mac, type `pair` to approve one invitation.
2. In Trill, choose **Pair a computer → Scan pairing code**.
3. Review the name/address, set this device's name if desired, and tap **Pair**.
4. Select a session to view its details. Terminal access remains disabled until you enable control.

Each invitation is single-use and expires after ten minutes. Each phone gets its own revocable credential stored in the device-only Keychain. Setup does not print permanent bearer credentials. Treat QR images and links as temporary access secrets.

For later invitations, or a simulator without a camera:

```sh
trill pair
trill pair --url
trill pair --png "$HOME/Desktop/trill-pair.png" --open
```

Each command approves and creates a separate invitation. With `--url`, transfer the link privately and use **Paste pairing link** on the phone/simulator. PNG output requires a new path and is created with mode 0600; remove saved QR images after use. `--json` produces invitation JSON; `--yes` explicitly approves one invitation without a prompt for local automation.

To pair another Mac, run its own setup, then choose **Sessions → More → Computers → Add computer** on the phone.

### Enable terminal access and session control

New companions start read-only. To open terminals, browse session files/previews, upload photos, and change sessions:

```sh
trill control --enable
```

Trill also asks for terminal permission on the phone; photo upload has a separate per-computer approval. For a new configuration, `--allow-control` on the initial setup opts in immediately. To return to metadata-only access:

```sh
trill control --disable
```

Changing control restarts only the owned companion if loaded and disconnects its transports and Mosh leases. Underlying tmux/Herdr sessions stay alive. Re-pairing is unnecessary. A no-op choice does not restart anything. If interrupted, use `trill start` with the same state directory to apply the saved choice.

### Manage devices and providers

```sh
trill status
trill devices
trill revoke DEVICE_ID
trill providers
trill providers --backends xirp,herdr
```

`trill devices` shows device names, pairing status, pairing dates in UTC, and full device IDs. Status describes pairing access, not whether a phone is currently connected. Use `trill devices --json` for machine-readable output.

Replace `DEVICE_ID` with an ID from `devices`. Revocation persists and releases that device's Mosh leases, leaving provider sessions and other devices' leases alone. Forgetting a computer on the phone only removes its local pairing; use revocation on the Mac to invalidate access.

Provider changes preserve pairings and control permission. They restart the owned companion when loaded and release its Mosh connections. For selected existing Herdr servers, add `--herdr-sessions default,work`; `--herdr-path` and `--herdr-config-home` handle custom installations. See [PROVIDERS.md](PROVIDERS.md).

## Update Trill

Update your checkout to the release/revision you intend to use. Rebuild and run the iPhone app through Xcode. To rebuild/install companion binaries **without configuring state or starting services**, run from the repository root:

```sh
./scripts/setup-companion.sh --help
```

Despite the name, this script builds and installs binaries before forwarding `--help` to setup. Keep the same `TRILL_BIN_DIR` if customized. All builds must succeed before executable replacement; binaries are published with atomic renames.

A running daemon continues using its old executable until restarted. When you are ready to disconnect Trill transports and load the update:

```sh
trill stop
trill start
trill status
```

These service operations preserve the underlying provider sessions and saved pairings. An ordinary setup rerun deliberately does not restart a loaded companion. Existing configurations retain their recorded private daemon/renewal paths; the installer still updates the legacy `trillctl` binary for those installations.

Update app and companion together when adopting features such as video preview. Update Xirp or Herdr separately using their own documented update process; Trill does not update providers.

## Stop or uninstall

To pause Trill's two owned LaunchAgents:

```sh
trill stop
# Resume later:
trill start
```

To stop and remove those LaunchAgents:

```sh
trill uninstall
```

This disconnects Trill transports and preserves provider sessions. **Uninstall keeps private state, certificates, device records, installed binaries, the public symlink, and uploaded photos.** It does not uninstall Xirp, Herdr, tmux, or Mosh. No configuration directory is recursively deleted by the CLI.

For complete removal, first uninstall through the correct state directory, then forget the computer on each phone and remove the Trill app if desired. Review and manually remove only the Trill-owned binary directory, its matching public symlink, your private state directory, and uploaded photos if you no longer need them. Default locations are listed below; custom installations may differ. Keep any images or state you need before deleting them.

## State, services, and certificates

| Default path | Purpose |
| --- | --- |
| `~/.local/bin/trill` | Managed public symlink |
| `~/.local/share/trill/bin` | Binaries and ownership marker |
| `~/.config/trill/config.json` | Network, permissions, provider and executable settings |
| `~/.config/trill/devices.json` | Device metadata and credential/invitation hashes |
| `~/.config/trill/tls.pem` | Certificate and private key |
| `~/.config/trill/app.trill.companion.log` | Companion diagnostics |
| `~/.config/trill/app.trill.companion.renew.log` | Certificate renewal diagnostics |
| `~/.local/share/trill/attachments` | Uploaded session photos |
| `~/Library/LaunchAgents/app.trill.companion.plist` | Companion service |
| `~/Library/LaunchAgents/app.trill.companion.renew.plist` | Renewal service |

State defaults to `~/.config/trill` independently of `XDG_CONFIG_HOME`. Custom state uses `--state-dir /absolute/path` on **every** management command, including stop, start, renew, and uninstall. Only one configuration can own the stable LaunchAgent labels at a time.

Private state directories use mode 0700 and private files use 0600. Unsafe ownership, symlinks, writable ancestors, and service ownership conflicts are rejected. Service commands verify the matching plist and loaded job; they do not stop an unknown service or kill an existing port listener.

Tailscale renewal runs at load and every six hours, requesting export when fewer than 30 days remain or validation fails. A coherent certificate/key bundle is replaced atomically and reloaded for new TLS handshakes without restarting terminal connections. Failed exports preserve the old bundle. You can request a check with `trill renew`.

LAN certificates last one year and cannot automatically rotate without changing the phone's pin. Within 30 days of expiry, renewal asks for a new configuration and pairing. Check expiry with `trill status`; there is no notification service.

Changing network, selected IP, Bonjour name, or certificate identity requires stopping/uninstalling the old owned configuration, choosing a fresh private state directory, running setup there, and pairing again. Keep the old state if needed. Setup does not silently migrate or replace an existing identity.

## Troubleshooting

Start with `trill status`, **Sessions → More → Connection details → Check connection**, and the companion's private log. Do not publish device credentials, certificate private keys, or active pairing links when asking for help.

| Symptom | What to check |
| --- | --- |
| Build reports a missing Duo API or SDK | Use Xcode 27.1+ and the iOS 27.1+ SDK. Check `DEVELOPER_DIR`; the minimum phone OS of iOS 18 does not make an older SDK sufficient. |
| `trill: command not found` | Add `$HOME/.local/bin` to PATH, or use the full executable path printed by the installer. |
| No projects/sessions, or a provider warning | Check provider installation and running state. Use the read-only provider listing commands above. Select Herdr explicitly if Xirp is not installed. A failed provider does not hide healthy providers. |
| Read-only access / control disabled | Run `trill control --enable` on the Mac with the correct state directory, then refresh. |
| Session listed but terminal will not open | Check `mosh-server`, `tmux` for Xirp, a live attachable terminal, and UDP reachability. Ended Xirp sessions may need resume; metadata alone is insufficient. |
| Herdr terminal occupied / HTTP 409 | Disconnect the competing direct controller or other device. Trill never takes over an external controller. Normal desktop Herdr TUI use can coexist. Check matching CLI/server protocols if the provider is unavailable. |
| LAN pairing cannot connect | Allow Local Network access, use the generated `.local` hostname, and check Bonjour, Wi-Fi isolation, firewall, and VPN DNS. Avoid substituting a numeric-IP self-signed URL. |
| Tailscale HTTPS export/trust fails | Check connection, clock, tailnet HTTPS settings, and CLI export permission. Setup stops on failure; fix the cause and retry. For saved state, use `trill renew` and rerun setup. |
| QR expired, used, or hard to scan | Create a fresh `trill pair`, or use `--url` / `--png`. Existing paired phones do not need another invitation. |
| Setup saved state but service installation failed | Resolve the printed listener/permissions/ownership problem and rerun setup with the same state directory. Do not delete a valid identity to retry. |
| Saved flags conflict | Remove conflicting flags to resume. Use `control` or `providers` for those changes; network/identity changes require the explicit new-state flow. |
| `tls.pem` exists without `config.json` | Preserve the orphan identity and use a fresh state directory. Setup refuses to overwrite it. |
| Interrupted permission/provider update | Run the printed `trill start --state-dir ...` recovery command. Unrelated ownership/configuration differences remain errors. |
| Service loaded but not running | Inspect the private companion log and dependencies on its saved PATH. A port collision is an error; Trill does not kill the listener. |
| LAN certificate expired | Check the clock or restore the original bundle. Replacement identity requires stop/uninstall, new state, and re-pairing. |
| Localhost preview fails | Start the development server on the Mac, verify its port and IPv4 loopback listener (`127.0.0.1`), then Reload. Trill does not start servers; re-pairing does not fix a stopped server. |
| Input or photo upload was interrupted | Review the terminal and draft before retrying. Trill does not automatically reinsert text; uploaded photos can remain on the Mac. |
| Terminal did not return after iOS closed Trill | Reopen the app and explicitly open the terminal. Foreground recovery covers background suspension, not process eviction. |

For custom foreground deployments, use `trill serve --help` and the [sidecar reference](../sidecar/README.md). Isolated lab and transport probes belong in [DEVELOPMENT.md](DEVELOPMENT.md); they send input and should only target disposable lab sessions.
