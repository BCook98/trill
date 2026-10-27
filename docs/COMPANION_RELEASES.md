# Companion distribution and maintenance

The Mac companion is Go software. Build it with Go, then use Apple's command-line signing/notarization tools. `ios/Trill.xcodeproj` builds the iPhone app; it does not build the companion. Companion versions live in `sidecar/COMPANION_VERSION`. This coordinated release uses `2026.10.25` for both iOS and the companion; companion tags use the separate `companion-vVERSION` namespace.

The release machinery is implemented, but signed downloads are **not enabled** until the public signing team and Apple credentials below are configured and an actual release passes clean-Mac validation. Continue using [source installation](SETUP.md#install-the-mac-companion) meanwhile.

## Commands

```sh
trill version
trill doctor
trill doctor --json
trill update --check
trill update
trill update --version 2026.10.25
```

`version` prints companion version, source commit, OS and architecture as JSON. Source builds identify as `dev`. `trilld --version` reports the daemon's identity without starting it.

`doctor` is read-only and returns exit code 1 for failed checks, 0 when there are no failures (warnings alone do not fail). It checks private configuration permissions, executable availability, signatures/notarization for configured distributions, both owned LaunchAgents, TLS identity/expiry, local HTTPS, running daemon versus CLI build, pending update recovery, and selected provider/Mosh/tmux dependencies on the saved service PATH. `--state-dir` selects an existing configuration. It never creates a pairing credential, repairs services, executes provider commands, or changes settings. Phone reachability, Mosh UDP connectivity and provider authentication still need a phone-side check.

`update --check` reads official stable companion release metadata over HTTPS without changing files or services. It ignores iOS tags and draft/prerelease releases. Normal updates reject older versions and development/source builds. A check is advisory: payload signatures are verified during installation, not by the metadata-only check.

Updates require internet access to fetch assets and verify Apple's notarization ticket. SHA-256 verifies archive integrity; the compiled-in Apple team and fixed code identifiers authenticate the executables. Downloaded metadata cannot supply a replacement signing team. Every executable must pass both Developer ID verification and Apple's online notarization check before any payload runs. Signed embedded version/commit/architecture must match the manifest. ZIP extraction rejects links, paths, duplicates and unexpected tools.

## Installation layout and update recovery

Signed releases install as the logged-in user, without sudo, Go or Xcode:

```text
~/.local/share/trill/companion/
  .trill-distribution
  bin/{trill,trillctl,trilld} -> ../current/{trill,trillctl,trilld}
  current -> releases/VERSION-COMMIT-UNIQUE
  releases/...
~/.local/bin/trill -> ~/.local/share/trill/companion/bin/trill
```

Setup records the stable `bin` paths in configuration and LaunchAgents, so releases can switch without rewriting configuration. Existing configuration, TLS identity, device registrations, provider choices and control settings are preserved. Prior releases are retained for rollback; automatic pruning and background updates are not implemented.

The updater stages the complete release, takes an installation lock, and serializes service changes with the existing configuration lock. A durable journal records the prior release and which of the two jobs were loaded. It unloads only verified owned jobs, atomically switches `current`, restores exactly those jobs, and verifies the running daemon's version/commit over pinned or trusted HTTPS. Previously stopped jobs stay stopped. Phone transports disconnect; provider sessions remain alive.

Failure restores the previous pointer and loaded jobs. If the process or Mac stops midway, run `trill update` with the same `--state-dir` to recover before any network download; rerun it to retry the update. `doctor` reports pending recovery. If service ownership or the journal cannot be verified, recovery stops with an explicit error and leaves the journal in place.

The first installer and updater refuse to adopt a source/custom installation or overwrite its public command/configuration. Existing source users should keep using `scripts/setup-companion.sh`. Automatic source-to-signed migration, state schema migrations and downgrades are outside this first implementation; do not delete pairing state to force a conversion.

After the first signed release exists, download and inspect its `install-companion.sh`, then run it with the matching version, for example:

```sh
bash install-companion.sh 2026.10.25
export PATH="$HOME/.local/bin:$PATH"
trill setup
trill doctor
```

The standalone installer contains the expected public team ID. It downloads only from the official repository, extracts the named CLI into a private temporary directory, verifies its Developer ID and notarization before execution, then lets the CLI verify and install the whole release. It does not run guided setup automatically.

## Enable signed releases

1. Commit the **public** ten-character Apple Developer team ID in `sidecar/distribution/signing-team.txt`. This is a trust anchor, not a secret. Changing teams later requires a deliberate trust migration for already installed clients.
2. Create a GitHub environment named `companion-release`, restrict it to companion release tags, and configure the following values there. Keep private keys and certificate exports out of the repository.

| Environment value | Type | Purpose |
| --- | --- | --- |
| `COMPANION_SIGNING_IDENTITY` | Variable | Exact `Developer ID Application: …` Keychain identity |
| `COMPANION_SIGNING_CERTIFICATE_BASE64` | Secret | Base64 PKCS#12 export including the signing private key |
| `COMPANION_SIGNING_CERTIFICATE_PASSWORD` | Secret | PKCS#12 password |
| `COMPANION_NOTARY_KEY_P8` | Secret | App Store Connect API private key authorized for notarization |
| `COMPANION_NOTARY_KEY_ID` | Secret | API key ID |
| `COMPANION_NOTARY_ISSUER_ID` | Secret | API issuer ID |

3. Validate the first release manually before enabling general distribution. From a clean tested commit, increase `COMPANION_VERSION` as needed and create/push its exact `companion-vVERSION` tag. Never move a released tag or replace published assets. The separate [workflow](../.github/workflows/companion-release.yml) runs on those tags, or manually when a companion tag is selected. It cannot publish from a branch, a fork, or an iOS tag. No release is triggered by this feature branch alone.

The workflow tests Go, imports the certificate into a temporary Keychain, builds both architectures, signs with timestamp and hardened runtime, notarizes both archives, and confirms the tickets are available. Only then does it produce a publishable manifest and installer. It creates a draft GitHub release containing the full set, then publishes it without replacing the repository's default latest release. Existing releases are not overwritten. A failed publish may leave a draft for inspection; inspect it before retrying. Signing files and the temporary Keychain are removed on exit.

For a local signed build, use the same builder with an existing Keychain identity and either `COMPANION_NOTARY_PROFILE` or the three key-file variables (`COMPANION_NOTARY_KEY_PATH`, `COMPANION_NOTARY_KEY_ID`, `COMPANION_NOTARY_ISSUER_ID`):

```sh
python3 scripts/build-companion-release.py --output .build/companion-signed
```

For credential-free packaging verification:

```sh
python3 scripts/build-companion-release.py --unsigned --output .build/companion-unsigned
```

Use a new output directory each time. Unsigned mode produces development binaries and `UNSIGNED-NOT-FOR-DISTRIBUTION.json`; it deliberately produces neither `manifest.json` nor a standalone installer. Those fixtures cannot be installed through the signed updater.

## Release validation still required

Local tests cover untrusted archives, pinned trust configuration, version binding, rollback, stopped/loaded service combinations and interrupted-update recovery using fixtures. They do not establish real Apple acceptance or Gatekeeper behavior.

The credential-free packaging check produced arm64 and x86_64 Mach-O binaries; their `LC_BUILD_VERSION` records macOS 12.0 as the minimum. This is an observed build setting, not a tested support guarantee.

Before the first public tag, verify actual notarization and browser-downloaded quarantined artifacts on Apple Silicon and Intel Macs without Go/Xcode. Exercise fresh setup, an update with a paired phone, a failed update, and interrupted recovery. Establish and document the minimum supported macOS version from the built binaries and real-machine testing. Keep subsequent releases compatible with the existing state schema until a migration design is implemented.

ZIP files and standalone command-line binaries cannot carry stapled tickets; initial installation and verification require Apple's online service. If offline first installation is needed, add a signed installer package with a stapled ticket. Apple documents [custom notarization](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow) and [Developer ID distribution](https://developer.apple.com/developer-id/).
