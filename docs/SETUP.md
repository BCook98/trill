# Set up Trill

## Prerequisites and supported versions

The session Mac needs [Homebrew](https://brew.sh/) and Herdr installed and running. The Homebrew companion does not require Go or Xcode; Go is only needed for [development builds](DEVELOPMENT.md). Trill integrates with Herdr protocol 22 (validated against 0.9.5); incompatible metadata fails explicitly. The phone needs iOS 18 or later. Building the native app requires Xcode with SDK 27.1 or later.

Herdr is the only provider. Terminal frames, input, scrolling and resize use an authenticated WebSocket on the companion’s HTTPS listener. There are no additional terminal ports or transport executables to install.

## Install the iPhone app

Open `ios/Trill.xcodeproj`, choose the Trill scheme and destination, select your signing team for a physical device, and Run. SwiftTerm is vendored. Inspect its build-info package plugin before trusting it. Normal builds do not require project regeneration.

## Choose a network

Both devices must reach the Mac. Keep the Mac awake and Herdr running.

- **LAN:** setup provisions a local certificate and advertises Bonjour. The phone trusts the exact certificate scanned in the invitation. Allow TCP 7443 through applicable firewall rules.
- **Existing Tailscale client:** setup can use the Mac’s connected client and trusted tailnet hostname. The phone must be on the same tailnet and ACLs must allow TCP 7443. Userspace networking forwards HTTPS to loopback.
- **Embedded Tailscale:** `--network tsnet --tsnet-hostname NAME` enrolls a dedicated companion node with its own identity. Follow the presented login link, using a distinct name for a development companion. State and pairing identity persist across restarts. It serves HTTPS on tailnet TCP 7443 and local loopback TCP 7443.

Trill does not edit firewall rules. Network access alone does not grant control or bypass pairing.

## Install the companion

On the session Mac, run as the user who owns the Herdr sessions:

```sh
brew install --cask bcook98/tap/trill
trill setup
trill status
trill doctor
```

Setup chooses a connected Tailscale client when available, otherwise LAN. Use `trill setup --help` for explicit network options. Homebrew installs the signed binaries and exposes `trill` on its PATH. Setup records stable Homebrew executable paths and installs only Trill’s owned user LaunchAgents; it never starts or replaces Herdr. Rerunning setup preserves identity, pairings and control permission and rejects conflicting changes.

The default server is Herdr’s existing `default`. Select additional servers or a custom installation:

```sh
trill providers --herdr-sessions default,work
trill providers --herdr-path /absolute/path/herdr --herdr-config-home /absolute/path/config-home
```

The config home is the base containing `herdr/`. Setup pins the effective `XDG_CONFIG_HOME`, or `~/.config`, so launchd uses the same installation. See [Herdr semantics](PROVIDERS.md).

Before upgrading an installation configured for a removed provider, use its previously installed CLI to select `trill providers --backends herdr`. The new companion rejects removed provider configurations and terminal flags explicitly. If already upgraded without migrating, preserve the old state and set up a fresh private `--state-dir`. Update app and companion together for the stream capability.

## Pair your iPhone

Type `pair` at setup’s local approval prompt or run `trill pair`. Scan the private QR in **Pair a computer**, or use `trill pair --url` and paste the link. Review the host before confirming. Invitations are single-use and expire after ten minutes. Permanent device credentials stay in the phone’s Keychain and are hashed on the Mac. Keep invitations out of logs and screenshots.

Setup begins read-only. Enable control explicitly:

```sh
trill control --enable
```

Selecting a live session opens it directly after the phone’s terminal-consent prompt. You can remember terminal permission per pairing. Photo upload permission is separate. Disabling control uses `trill control --disable`; it restarts only the owned companion and releases attachments while Herdr programs remain alive.

## Devices and configuration

```sh
trill devices
trill revoke DEVICE_ID
trill providers
trill control
trill status
```

Revocation denies new authenticated requests and releases that device’s tracked controllers and preview grants. Forgetting a computer on the phone removes its local credential but does not revoke it on the Mac. Provider selection and control changes preserve pairings.

Management commands accept `--state-dir /absolute/private/directory`. Do not reuse a production state directory or listener for development. The default state is `~/.config/trill`; uploaded images default to `~/.local/share/trill/attachments` and remain until manually removed.

## Updates and removal

```sh
brew update
brew upgrade --cask bcook98/tap/trill
trill stop
trill start
```

Restart after upgrading to load the new daemon. Use your usual `--state-dir` on both restart commands when using a custom directory. Homebrew owns binary upgrades; `trill update` prints the Homebrew upgrade command. Trill does not update Herdr. A restart closes phone attachments without closing Herdr terminals.

Remove the owned services before removing the binaries:

```sh
trill uninstall
brew uninstall --cask bcook98/tap/trill
```

Use the same `--state-dir` with `trill uninstall` for a custom configuration. It removes only Trill’s owned LaunchAgents; Homebrew removes its installed binaries and links. Private configuration, certificates, device records, tsnet identity and uploaded photos are retained. Review retained files on the Mac before manually deleting them. See [companion releases](COMPANION_RELEASES.md) for legacy standalone installations.

Source and standalone configurations are not automatically converted by installing the cask. Use the CLI recorded in the existing configuration until you explicitly migrate; preserve the old state and pairings. A fresh setup in a separate private `--state-dir` requires pairing again.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Pairing cannot reach Mac | Mac awake, same LAN/tailnet, correct HTTPS hostname, TCP 7443 and current certificate |
| Metadata works but terminal denied | `trill control --enable`, remembered phone permission, updated app and companion |
| No spaces or stale metadata | Selected Herdr server running, pinned config home/executable, `trill doctor`; warnings identify failed servers |
| Terminal busy | Another direct controller owns it; disconnect that client. Trill never takes over |
| Screen shows a cleanup warning | Network loss prevented confirmed release; wait for cleanup, then retry |
| Existing config rejected | Select Herdr with the current CLI and remove obsolete transport/lab options |
| Files unavailable | Herdr must report an authoritative cwd or project checkout; paths cannot escape it |
| Preview fails | Start the server yourself on IPv4 localhost and verify its port; preview does not launch it |

For foreground development and API details see [the companion reference](../sidecar/README.md), [API](API.md), and [usage](USAGE.md).
