# Security

## Reporting

Report suspected vulnerabilities privately to [trill@bcook.io](mailto:trill@bcook.io). Include the affected revision/version, platform, reproduction steps, expected versus actual behavior, and impact. Use disposable fixtures and redact bearer tokens, pairing invitations, private keys, personal paths, and terminal content. Coordinate disclosure by email before posting sensitive details publicly.

Trill is prerelease software. This policy describes the current implementation and its limits; it does not assert a completed security audit or absence of vulnerabilities. See [PUBLICATION_AUDIT.md](docs/PUBLICATION_AUDIT.md) for the recorded history-scan findings, fixture triage, media review, and scope.

## Trust model

The companion runs as the signed-in Mac user and trusts that user's provider installations, metadata, filesystem, and processes. It is not a sandbox for untrusted projects or a multi-user authorization service. Code running as that user can access companion state and the underlying sessions.

A paired device is trusted with the companion's exposed catalog. Control is a global local configuration choice, not a per-device or per-project role. With control enabled, every authorized device can use supported mutations, terminal input, project/worktree file reads, session photo storage, and localhost previews. Terminal access gives the authority of the remote shell and any tools or credentials available there.

Read-only mode permits authenticated health/catalog reads. It denies terminal bootstrap (which can resize a terminal), session mutations, file/media reads, photo uploads, and browser-grant creation. Native consent prompts are user-interface safeguards; a bearer-token holder can call permitted API routes directly.

Keep network access restricted to intended devices using the selected LAN or private network. TLS and pairing do not make an Internet-facing companion a hardened public service.

## Pairing, credentials, and TLS

Build the companion with Go 1.26.10 or a newer patched release. The module
minimum rejects older compilers, and CI checks reachable vulnerabilities with
`govulncheck`. Updating source or installing a compiler does not patch an
existing companion: rebuild its binaries and restart the service. Keep the
compiler baseline current across supported release branches.

Normal managed setup uses a pairing store. Locally approved invitations contain 256 bits of randomness, expire within ten minutes, and are consumed atomically once. Anyone holding an unused invitation can redeem it before expiry; protect QR images and pairing links like credentials.

Redeeming an invitation returns a separate random device bearer token. The companion stores SHA-256 hashes of invitations and tokens in a private, locked, atomically replaced state file. Device credentials have no automatic expiry. iOS stores saved computers and credentials in a device-only, nonsynchronizing Keychain item available when unlocked.

The app requires HTTPS outside explicit simulator loopback development. API redirects are refused. TLS uses system trust or an out-of-band SHA-256 leaf pin with hostname and validity checks. LAN identity replacement requires re-pairing; do not bypass a changed or expired certificate.

Manual daemon operation also supports a legacy shared token, including alongside pairing if explicitly configured. That token has no device identity and is not revoked by revoking a paired device. Removing or replacing its file does not change a running daemon's in-memory token: restart/reconfigure that daemon to disable or rotate it. Prefer pairing-only operation. Plain HTTP development mode is restricted to a loopback listener.

## Revocation and its limits

Run `trill devices` and `trill revoke DEVICE_ID` on the Mac, selecting the same `--state-dir` used for setup when applicable. Each later paired API request checks the persisted credential state without an authentication cache.

Background watchers poll approximately every 100 ms to retire revoked devices' Mosh leases and browser grants, including active proxied connections. Store errors fail closed for paired authentication and tracked streams. Polling and process teardown are asynchronous; the interval is not a guaranteed maximum disconnect time.

Revocation is not a transaction barrier for all work already authorized. Ordinary in-flight reads, video transfers, and provider mutations are not universally cancelled or reauthorized when the store changes. Mosh bootstrap, preview creation, and photo upload have additional credential checks, but revocation cannot undo completed commands, downloaded content, uploaded photos, or data already sent to a development server.

Forgetting a computer on the phone removes local credentials/consents and closes its local transports; it does not revoke the Mac's device record. Revoke a lost device on the Mac. Disabling control through managed setup restarts the owned daemon when loaded and disconnects its transports; underlying provider sessions remain alive. Neither disconnect nor revocation stops arbitrary processes previously started through the terminal.

## Terminal transport resource limits

The native Mosh transport retains at most 64 queued UDP datagrams before
authentication, discarding the oldest queued packet when a newer one arrives
at capacity. Connected UDP limits incoming traffic to the configured peer;
this queue limit also bounds memory when packets arrive faster than the app
can authenticate and consume them.

Fragment reassembly resets an instruction if retained contents would exceed
16 MiB, including incomplete instructions. Decompression rejects output above
16 MiB per instruction before appending the next chunk, for zlib, LZ4, and
LZFSE. Peer authentication alone does not
make compressed data safe to expand without a limit. Instructions above this
limit cannot be displayed; ordinary transport retransmission still applies
to packets dropped under receive pressure. These limits do not bound every
allocation in the terminal emulator or constitute an independent cryptographic
audit of the vendored transport.

## Files and uploads

Authenticated, control-gated file reads use server-selected project/worktree roots and descriptor-based confinement. General browsing may read hidden files and follow symlinks that remain within the root; it is not a secret-file filter. Treat project credentials and other sensitive files under those roots as accessible to trusted devices. Special files and traversal outside the root are rejected.

Reads cap text at 2 MiB, images at 10 MiB, directory results at 1000 entries, and video downloads at 100 MiB. Static HTML/SVG preview has stricter selected-folder, hidden-component, symlink, and asset-type restrictions. Video is downloaded privately for native playback rather than giving the media player the API token.

Photo uploads require a live eligible session and control. The companion chooses the filename and private session directory under `~/.local/share/trill/attachments`; clients cannot supply an arbitrary destination. Limits are 10 MiB and 40 megapixels per image, 250 MiB total storage, and 4096 storage entries. The iOS picker path re-encodes selected photos without source EXIF/location metadata; the server validates images but does not promise to strip metadata from arbitrary API uploads. Uploaded files remain until the Mac owner removes them, including after device revocation.

## Browser previews

Static document preview disables content JavaScript and uses an ephemeral custom WebKit scheme with native authenticated asset reads, restrictive CSP, and blocked HTTP/WebSocket resources. No API bearer token is given to page scripts.

Live preview deliberately grants access to a selected unprivileged HTTP port on the Mac's IPv4 loopback interface, excluding the companion listener. Session liveness is checked at grant creation, but the server does not establish that the port belongs to that session, project, or OS user. A control-enabled device can reach other HTTP services on eligible loopback ports. Select only trusted development servers.

A grant lasts at most 30 minutes and is bound to one port and companion origin. WebKit receives a temporary HttpOnly/Secure/SameSite cookie in a nonpersistent store, not the API bearer token. The proxy strips authorization/forwarding headers and its capability cookie before upstream forwarding, limits connections, and filters redirects and reserved response headers. Client resource rules constrain same-host cross-port HTTP/WebSocket requests.

Live development pages can run JavaScript, make requests, use supported WebSockets, and modify the selected service. A private cookie store does not make them read-only or prevent content from sending data to external services. Explicit browser teardown releases its grant; hiding and reopening retained workspace state can preserve it. Failed cleanup relies on grant expiry or revocation. External pages use WebKit's normal trust and platform networking policy.

## Verification boundaries

Source entry points are [HTTP/authentication](sidecar/http.go), [pairing storage](sidecar/pairing/store.go), [control changes](sidecar/setup/control.go), [files](sidecar/files.go), [uploads](sidecar/attachments.go), [preview grants](sidecar/preview.go), and [native WebKit policy](ios/Trill/WorkspaceView.swift). See [Architecture](docs/ARCHITECTURE.md) and [Validation](docs/VALIDATION.md) for implementation context and reproducible checks.

History scanning, dependency/cryptographic review, adversarial networking, device testing, and published-media inspection are distinct activities. A passing regression suite or test-fixture triage does not establish that other vulnerabilities are absent.
