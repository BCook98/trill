# Trill demo on Fly

This deployment uses app `trill-demo` in org `trill`: one always-on
`shared-cpu-1x` Machine with 256 MB RAM in `iad`, one 1 GB volume, and the
reserved dedicated IPv4 `137.66.44.233`. It has two Mosh leases. The existing
volume is named `demo_data`, with scheduled snapshots disabled.
Configure the reviewer password through the private `DEMO_REVIEW_PASSWORD_B64` secret.
The deployment and load checks passed on 25 October 2026; evidence is summarized below.

Only TCP 443 and UDP 60300–60301 are public. TLS terminates at Fly; the fixed
startup uses `--fly-edge-tls`, private HTTP `0.0.0.0:8080`, and
`https://trill-demo.fly.dev`. Pairing uses managed TLS and an empty fingerprint.
Port 80 is not configured. The TCP health check needs no Host or credential;
it establishes listener availability, not API, pairing, preview or Mosh health.

Fly requires [dedicated IPv4, `fly-global-services`, and identical internal and
external UDP ports](https://fly.io/docs/networking/udp-and-tcp/). The parent
resolves the special bind address at startup. HTTP and UDP autostop/autostart
are disabled. Keep the one Machine running even while HTTPS is idle so a live
Mosh connection is not stopped. Nothing in this config adds Machines; always
pass `--ha=false` when deploying and verify that exactly one exists afterward.

## Build and deploy

Run from the repository root. Build and qualify changes locally before updating
the shared reviewer sandbox. Reserve a maintenance window for deployment and
fault-injection checks.

```sh
docker build --platform linux/amd64 \
  --file deploy/fly/Dockerfile --tag trill-demo:qualification .
```

The Dockerfile builds `sidecar/cmd/trillctl` with Go 1.26.10 and CGO disabled.
It installs Debian Bookworm's upstream `mosh=1.4.0-1+b1`; the
[Debian package index](https://packages.debian.org/bookworm/mosh) confirms that
amd64 version. Bookworm is the deliberately selected image suite, not an alias
for Debian's moving stable release. The build retains `/usr/share/doc/mosh/copyright`
and records installed package versions at `/usr/local/share/trill/dpkg-versions.txt`.
Image tags and other package revisions can move; retain the resulting image digest
and package manifest with qualification evidence.

`Dockerfile.dockerignore` admits only sidecar Go sources, module metadata,
embedded sample media/provenance and the image bootstrap. It excludes repository
metadata, local state, credentials, app builds and unrelated project files.
Docker uses the Dockerfile-specific ignore file. Fly also selects it explicitly
with `build.ignorefile`; there is no broad repository-root COPY.

After image qualification, deploy with the local build:

```sh
fly deploy . --app trill-demo --config deploy/fly/fly.toml \
  --local-only --ha=false --strategy immediate
fly status --app trill-demo
fly machines list --app trill-demo
fly volumes list --app trill-demo
fly services list --app trill-demo
```

That deploy command rebuilds locally. To publish an already qualified image,
tag and push that exact image to the Fly registry, then use `fly deploy --image`
with its digest and `--ha=false` instead. Do not create another volume/IP, add
HA, enable blue/green deployment, or enable a remote paid builder. Immediate
updates interrupt the single Machine and existing leases. There is no replica
or automatic volume extension.

## Privilege and network boundaries

Root runs only the fixed entrypoint before the app. It verifies Fly/app identity
and the mounted volume, makes the dedicated `/data` mount UID/GID 10001 and mode 0700,
and leaves `/data/state` creation to the unprivileged app so its ownership marker
is valid. Existing state must already be UID/GID 10001, mode 0700, and not a
symlink. Startup never recursively changes ownership or silently repairs state.
The runtime's application-owned paths are the dedicated `/data` volume and `/run/trill`;
image code and system directories stay root-owned and unwritable by UID 10001.

Fly writes the base64-decoded secret into `/run/trill/review-password`. Startup
rejects symlinks/multiple links, sets UID/GID 10001 and mode 0600, and removes
`DEMO_REVIEW_PASSWORD_B64` from the environment before exec. Do not print the
secret, pass it as a build argument, bake it into an image, or copy it into the
volume. Rotation uses a newly generated private credential and staged Fly secret,
followed by a deliberate redeploy. See [Fly secret files](https://fly.io/docs/apps/secrets/).

Before dropping privileges, startup submits one native `nft -f -` batch. It
adds, deletes, and recreates only the owned `inet trill_demo` table in that
transaction. It never flushes the VM's ruleset or edits another table. See the
[nftables atomic replacement reference](https://wiki.nftables.org/wiki-nftables/index.php/Atomic_rule_replacement).
This replaces the earlier iptables approach because the live Fly kernel rejected
its `xt_owner` extension. The image installs `nftables`; startup uses native
`meta skuid` matching, not that compatibility extension.

The `output` base chain uses priority -10 and default accept, with only
`meta skuid 10001 jump service`. Other socket UIDs bypass this owned policy;
other VM tables still apply. In `service`, established/related replies and
loopback are accepted first. IPv4 private, link-local/metadata, multicast and
reserved destinations are rejected before allowing new UDP from the exact
resolved `fly-global-services` IPv4 and source ports 60300–60301. The final
`counter reject` blocks everything else. The inet table also covers IPv6, whose
new traffic has no UDP or TCP exception. Root resolves the source IP with
`getent` before installing rules; malformed or missing IPv4 blocks startup.
The [official nftables quick reference](https://wiki.nftables.org/wiki-nftables/index.php/Quick_reference-nftables_in_10_minutes)
describes socket UID, connection-state, address and source-port predicates.

The atomic transaction must succeed or the app is not started. `setpriv` then
switches to UID/GID 10001, clears supplementary groups and capabilities, and
enables no-new-privileges. The fixed bootstrap shell is never a terminal
interpreter. Firewall execution belongs only inside the owned Fly VM, after
image validation. Recheck native nft kernel support, CAP_NET_ADMIN and actual packet verdicts
when changing the image bootstrap or VM platform.

## Cost and qualification

Budget **USD 4.34 per 30 days plus egress** at the current lowest-region prices:
$2.19 compute + $2.00 dedicated IPv4 + $0.15 for the 1 GB volume. Current
[Fly pricing](https://fly.io/pricing/) lists North America/Europe egress at
$0.02/GB. This is a baseline estimate, not a spending cap or measured invoice;
no paid extras, HA, autoscaling, scheduled snapshots or volume growth are enabled.
The 24-hour retention setting belongs to the demo runtime; it does not remove
or stop the billed Machine, IPv4 or volume.

The 256 MB size passed the workload described below. `GOMEMLIMIT=160MiB` gives the parent
Go process a soft memory target; it does not cap total RSS or guarantee capacity
for uploads, terminal children and Mosh. Record memory under two real leases,
photo decoding, reconnect and preview/WebSocket traffic before reviewer use.
The single local volume has no replica or scheduled backup; host failure can
make the sandbox unavailable or lose its state.

Local checks cover shell syntax/ShellCheck, TOML parsing, Fly's local config
parser, exact ports and sizing, and native nft batch generation with fake tools.
Run `python3 deploy/fly/test_startup.py` for the offline firewall regression checks.
These verify the single owned-table transaction, output hook/UID scope, ordered
rule predicates, changed bind IPs, failure before readiness, and negative IPv6,
private/metadata, wrong-source and out-of-range-port probes. Unknown transaction
commands or service rules fail the predicate model. The model is deliberately
limited to this policy; it is not an nft parser and cannot prove atomicity,
kernel behavior or the effect of other VM tables.
They do not install firewall rules or prove Fly kernel support, edge TLS/header
trust, real UDP routing, resource ceilings, native UI behavior or provider
compatibility. Deployment qualification must check those independently, including
new outbound TCP/metadata/6PN rejection as UID 10001 while HTTPS responses,
loopback previews, Mosh updates and Fly operator access continue to work.

## Qualified deployment (25 October 2026)

`trill-demo` in the `trill` organisation runs one `shared-cpu-1x` 256 MB Machine
in `iad`, with one encrypted 1 GB volume and dedicated IPv4 `137.66.44.233`.
The private reviewer page is `https://trill-demo.fly.dev/review`; credentials
are supplied separately, never in this repository.

The deployed Linux image digest is
`sha256:2f5ce9287ebb0e29f4d112362e3ee920b7cb237c755a146a43fd5dd17b3cd160`.
Its image index is
`sha256:b5393947e250a673f27b68a60becff65adf66cbe6b5e4cf0401d5dc474a8869d`.
Go runtime source is commit `9c029c8`; the native firewall replacement is in
`f5fd287`. The image records installed Debian package versions, including
Mosh `1.4.0-1+b1`.

Public trusted HTTPS, single-use pairing/expiry, create/rename/archive/resume,
real Mosh input/resize/reconnect with distinct lease IDs, files/uploads,
authenticated browser forwarding and injected-error recovery passed from an
external Mac. The smoke run revoked its device, released its leases and cleared
its faults. Anonymous access to `/review` returns 401.

A separate load check held both real Mosh slots open while uploading a generated
4000×2000, 16-bit RGBA PNG. It passed without restarting the Machine. The parent
and four Mosh/terminal processes totalled about 116 MiB RSS after the upload;
Linux reported about 67 MiB available. This is a measured workload, not an
unbounded-load guarantee. Demo uploads reject dimensions above 8 MP before
pixel decoding; ordinary Trill photos are resized to a 2048-pixel longest edge.

To repeat the transport checks during a reserved maintenance window:

```sh
python3 scripts/demo-fly-smoke.py --app trill-demo --allow-demo-mutations \
  --output "$PWD/.local/new-fly-smoke-report.json"
```

Without `--allow-demo-mutations`, the command performs read-only deployment and
HTTPS checks. It never stops the hosted server. Fault tests affect the whole
shared sandbox, so don't run them during a review. Each report identifies the
Machine and image and excludes tokens, invitations and Mosh keys. Native app,
physical-device and actual provider coverage remain separate.

### Reviewer form fix (25 October 2026)

Source `7046477` changes only the reviewer page's referrer policy to `same-origin`.
The previous `no-referrer` policy caused WebKit form submissions to send
`Origin: null`, which the server correctly rejected. Missing, null and foreign
origins remain rejected; other routes keep `no-referrer`.

The replacement image index is
`sha256:a01a380458661b81b3ae59118fac0875712fa864d8cd71ff0ec51353a580e88d`,
with Linux image
`sha256:466106cc4471253a5f067dfb795623f9f67248368c2d232142b59bab6060c1e9`.

WebKit reproduced the old form's 403 with `Origin: null`. After deployment,
the same browser form returned 200 with the expected origin and rendered both
the pairing link and a decoded QR. The focused Go package tests and vet passed.

### Preview session routing fix (25 October 2026)

Native IDB testing found that a newly created worktree's `localhost:18080`
preview showed the first fixture's heading. Source `c474a08` binds demo pages,
assets and WebSocket updates to the session stored in the authenticated preview
grant. Client headers and query strings cannot override it; ordinary companion
proxy behavior is unchanged. Regression tests, focused race tests, vet and an
independent review passed. The native app then rendered a fresh terminal-entered
heading from its own hosted worktree.

Deployed image index:
`sha256:90cf4d5d6bd3a36d099c0ef1dc9d99e353297a2eb2ffdc0a03a12e9a7d110afb`.
Linux image:
`sha256:110c259cd4cfce6d907b7f71e2ca963567c8b3a46d5764315fc6b94b602a6af9`.
The same single Machine, volume, IPv4 and memory sizing remain in use.
