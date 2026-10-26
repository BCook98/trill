# Hosting the reviewer sandbox

These are generic Linux deployment templates. For the hosted reviewer sandbox, use the [Fly.io configuration](../fly/README.md). Local smoke testing needs no cloud host. Use a separate fixed-price Linux VM for review, with a traffic allowance and no cloud instance credentials. No Xirp, Herdr, tmux, Codex, or AI accounts are needed.

Build `sidecar/cmd/trillctl` as `trill` using the repository's supported Go toolchain. Install the tested upstream Mosh package on the VM and record its exact version and package checksum alongside the Go executable checksum. Do not silently update either during a review. The binary also provides the restricted terminal child; there is no separate shell executable configured by Trill.

The service template expects an unprivileged `trill-demo` user, the executable at `/usr/local/bin/trill`, and a private `/etc/trill-demo` directory. Supply `cert.pem` and `key.pem` for the advertised HTTPS hostname, and a random password of at least 24 characters in `reviewer-password`. The password file must be owned by `trill-demo`, mode0600. Keep credentials out of Git and command arguments. Configure `demo.env` from the example; the service never creates cloud resources or changes DNS.

Expose only the chosen HTTPS port and UDP 60300–60303 to reviewers. The preview listener stays on IPv4 loopback; the administrative API is a private Unix socket. UDP must be supported by the host/network: an HTTP tunnel alone is insufficient. Mosh advertises the public hostname while binding the VM's local interfaces, so NAT can be handled by an explicit UDP port mapping.

Before exposing the host:

- Set host/provider rules to deny outbound new connections and access to instance metadata/private networks, while allowing replies to HTTPS/Mosh and loopback fixture traffic. Rule syntax depends on the host; do not copy a blanket network-denial rule that also blocks UDP replies. Certificate renewal and package updates should use a separately controlled operator path.
- Enforce an aggregate disk quota for the service state. The application bounds uploads and fixture state, but that is not an operating-system disk quota. Keep system/service logs bounded too.
- Retain the unit's memory/process/CPU limits; measure a full two-terminal workload before choosing VM size. The template's 256 MiB cap is a starting ceiling, not a measured guarantee of minimum RAM.
- Keep the reviewer instance separate from local/CI tests. Resetting it invalidates its pairings and removes uploads. Its idle retention is 24 hours; an active terminal keeps it alive. Idle expiry clears owned data and exits so the sample service restarts into a fresh sandbox; explicit stop does not restart it. Provide a fresh invitation when needed.

The private reviewer URL is `https://YOUR-HOST:8443/review`. Supply username `reviewer`, its password, and clear walkthrough instructions privately in App Store Connect. The page issues ordinary single-use ten-minute invitations; it contains no permanent app bearer token. Reviewers can tap the link on the same iPhone or scan its QR from another screen. Describe the scripted workload and the real networking honestly; the fixture does not establish that Xirp/Herdr integrations work.

The sample unit has not been installed by the repository tooling. Validate its syntax with the target Linux version's `systemd-analyze verify`, confirm PTY allocation under its restrictions, test public HTTPS/UDP from outside the VM, and run the app smoke scenarios before supplying reviewer access. Do not claim deployment readiness based only on a localhost test.
