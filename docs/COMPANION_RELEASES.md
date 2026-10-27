# Companion distribution and maintenance

The Mac companion is Go software. Build it with Go, then use Apple's command-line signing/notarization tools. `ios/Trill.xcodeproj` builds the iPhone app; it does not build the companion. Both platforms read the marketing version from `ios/Version.xcconfig`. A single immutable `vVERSION-build.COMMIT12` tag triggers Xcode Cloud for iOS and the companion signing workflow. Small updates retain the marketing version and get a new commit-based tag; there is no separate companion tag or version file.

## Install and maintain with Homebrew

```sh
brew install --cask bcook98/tap/trill
trill setup
trill doctor
```

Install and run Herdr separately. No Go or Xcode is needed for the companion. See [setup and pairing](SETUP.md#install-the-companion).

```sh
brew update
brew upgrade --cask bcook98/tap/trill
trill stop
trill start
```

Restart with your usual `--state-dir` on both commands to load the upgraded daemon. Phone attachments disconnect; Herdr terminals and pairings remain. Before removing binaries, run `trill uninstall` with that state directory, then `brew uninstall --cask bcook98/tap/trill`. Private state is retained.

## Diagnostics and legacy standalone commands

```sh
trill version
trill doctor
trill doctor --json
trill update --check
trill update
trill update --tag v2026.10.25-build.0123456789ab
```

`version` prints companion version, source commit, OS and architecture as JSON. Source builds identify as `dev`. `trilld --version` reports the daemon's identity without starting it.

Homebrew publishing details are in [packaging/homebrew](../packaging/homebrew/README.md).
The command `brew install --cask bcook98/tap/trill` installs the
checksummed signed assets. Install and run Herdr separately. Homebrew installations
use stable prefix paths and direct `trill update` users to `brew upgrade`; the standalone
updater never rewrites Homebrew-owned binaries. Existing source/standalone
configurations require an explicit migration before Homebrew setup can use them.

`doctor` is read-only and returns exit code 1 for failed checks, 0 when there are no failures (warnings alone do not fail). It checks private configuration permissions, executable availability, signatures/notarization for configured distributions, both owned LaunchAgents, TLS identity/expiry, local HTTPS, running daemon versus CLI build, pending update recovery, and Herdr executable on the saved service PATH. `--state-dir` selects an existing configuration. It never creates a pairing credential, repairs services, executes provider commands, or changes settings. Phone HTTPS reachability and Herdr authentication still need a phone-side check.

For legacy standalone installations, `update --check` reads official stable companion release metadata over HTTPS without changing files or services. It uses the shared release tags and ignores draft/prerelease releases. Normal updates reject older versions/builds and development/source builds. Repeated releases of the same marketing version are ordered by the full main-history commit count, embedded in signed binaries and the manifest; it is independent of Xcode Cloud’s build number. A check is advisory: payload signatures are verified during installation, not by the metadata-only check.

Updates require internet access to fetch assets and verify Apple's notarization ticket. SHA-256 verifies archive integrity; the compiled-in Apple team and fixed code identifiers authenticate the executables. Downloaded metadata cannot supply a replacement signing team. Every executable must pass both Developer ID verification and Apple's online notarization check before any payload runs. Signed embedded version/commit/architecture must match the manifest. ZIP extraction rejects links, paths, duplicates and unexpected tools.

## Legacy standalone layout and update recovery

Legacy standalone signed releases use this layout, separate from Homebrew’s Caskroom and stable prefix links:

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

The first installer and updater refuse to adopt a source/custom installation or overwrite its public command/configuration. Existing source users can build binaries using the [development guide](DEVELOPMENT.md#build-the-companion-from-source). Automatic source-to-signed migration, state schema migrations and downgrades are outside this first implementation; do not delete pairing state to force a conversion.

The standalone installer contains the expected public team ID. It downloads only from the official repository, extracts the named CLI into a private temporary directory, verifies its Developer ID and notarization before execution, then lets the CLI verify and install the whole release. It does not run guided setup automatically.

## Signed release configuration

1. Commit the **public** ten-character Apple Developer team ID in `sidecar/distribution/signing-team.txt`. This is a trust anchor, not a secret. Changing teams later requires a deliberate trust migration for already installed clients.
2. Create a GitHub environment named `companion-release`, restrict it to shared `v*-build.*` release tags, and configure the following values there. Keep private keys and certificate exports out of the repository.

| Environment value | Type | Purpose |
| --- | --- | --- |
| `COMPANION_SIGNING_IDENTITY` | Variable | Exact `Developer ID Application: …` Keychain identity |
| `COMPANION_SIGNING_CERTIFICATE_BASE64` | Secret | Base64 PKCS#12 export including the signing private key |
| `COMPANION_SIGNING_CERTIFICATE_PASSWORD` | Secret | PKCS#12 password |
| `COMPANION_NOTARY_KEY_P8` | Secret | App Store Connect API private key authorized for notarization |
| `COMPANION_NOTARY_KEY_ID` | Secret | API key ID |
| `COMPANION_NOTARY_ISSUER_ID` | Secret | API issuer ID |

3. Validate the first release manually before enabling general distribution. From a clean tested main commit, use `python3 scripts/release.py plan` and publish its exact `vVERSION-build.COMMIT12` tag once for both platforms. Never move a released tag or replace published assets. The separate [workflow](../.github/workflows/companion-release.yml) runs on those tags, or manually when the same shared tag is selected. It checks the shared version, commit suffix and main ancestry. It cannot publish from a branch or a fork. No release is triggered by this feature branch alone.

The workflow requires a successful main `Checks` run for the exact tagged commit, then imports the certificate into a temporary Keychain, builds both architectures, signs with timestamp and hardened runtime, notarizes both archives, and confirms the tickets are available. Only then does it produce a publishable manifest and installer. It creates a draft GitHub release containing the full set, then publishes it under the same shared tag used by Xcode Cloud. Existing releases are not overwritten. A failed publish may leave a draft for inspection; inspect it before retrying. Signing files and the temporary Keychain are removed on exit. Validation runs once on main; a lightweight Linux gate waits for that run instead of repeating Go tests/vet. The macOS signing runner starts only after the gate succeeds. A missing, failed or cancelled check run blocks signing. Rerun the original main check run and the release job to retry a cancelled validation.

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

For release validation, verify actual notarization and browser-downloaded quarantined artifacts on Apple Silicon and Intel Macs without Go/Xcode. Exercise fresh setup, an update with a paired phone, a failed update, and interrupted recovery. Establish and document the minimum supported macOS version from the built binaries and real-machine testing. Keep subsequent releases compatible with the existing state schema until a migration design is implemented.

ZIP files and standalone command-line binaries cannot carry stapled tickets; initial installation and verification require Apple's online service. If offline first installation is needed, add a signed installer package with a stapled ticket. Apple documents [custom notarization](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow) and [Developer ID distribution](https://developer.apple.com/developer-id/).
