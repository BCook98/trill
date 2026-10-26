# Development

Run commands from the repository root unless a command changes directory. This guide covers contributor checks; user installation is in [SETUP.md](SETUP.md).

## Toolchains and dependencies

| Area | Requirement | Dependency source |
| --- | --- | --- |
| Companion | Go 1.26.10 module minimum; CI pins Go 1.26.10 on Linux and macOS and checks reachable vulnerabilities | `sidecar/go.mod` and `sidecar/go.sum` |
| Python tools | Python 3.12+ recommended; CI checks 3.12 and 3.14 | Standard library only; no pip install required |
| Website checks | Node.js 22+; Node.js 24 LTS is used in CI, npm with lockfile v3 support | `website/package.json` and `website/package-lock.json` |
| Project generation | Ruby 3.3+ recommended, Bundler 4.0.11, xcodeproj 1.28.1 | Root `Gemfile` and `Gemfile.lock` |
| Native build/test | macOS, Xcode with iOS **SDK 27.1 or later**, compatible simulator runtime | Select with `DEVELOPER_DIR` or `xcode-select` |

CI fixes the Go patch at 1.26.10 to make runs repeatable; `sidecar/go.mod` also requires at least Go 1.26.10. With the default `GOTOOLCHAIN=auto`, an older Go launcher may download that toolchain; with automatic downloads disabled, install a compatible compiler first. The CI pin and module minimum must be updated deliberately as supported Go patches change.

The app has an iOS 18 deployment target, but its Duo API references require SDK 27.1+ at compile time. An older Xcode cannot compile it merely because those calls have runtime availability guards. The checked-in Swift packages are under `vendor/`; there is no submodule initialization step. Those packages require Swift tools 6.0 or newer, supplied by the required Xcode.

`DEVELOPER_DIR` is an optional per-process override, for example `export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer`. If absent, the native build and demo helpers use the existing `xcode-select` selection. Neither helper changes the global selection or assumes a `.local/toolchains` installation. Inspect the selection before native work:

```sh
xcode-select -p
xcrun --find xcodebuild
xcrun --sdk iphonesimulator --show-sdk-version
xcodebuild -version
```

GitHub Actions runs Go, Python tooling, and website browser checks on `main` in [checks.yml](../.github/workflows/checks.yml). Native tests run separately in Xcode Cloud, pinned to Xcode 27.1 with iPhone Duo on iOS 27.1 and iPhone 17 Pro on iOS 27.0. Cloud rejected the iPhone 17 Pro/iOS 27.1 pairing even though its API advertised it; verify actual completed runs before claiming device coverage. Pull requests do not trigger either service. The local native test command below remains available for contributors.

## Clean-clone checks

```sh
(cd sidecar && go test ./... && go vet ./...)
python3 -m compileall -q scripts tests
# Top-level discovery does not recurse into these non-package directories.
# Run each separately; this also includes tests/release and future suites.
for suite in tests/*/; do
  python3 -m unittest discover -s "$suite" -p 'test_*.py' -v || exit 1
done
sh -n scripts/build-ios.sh
npm --prefix website ci
npm --prefix website run check
```

Python suites currently include `tests/demo_lab`, `tests/demo_runner`, `tests/demo_export`, `tests/tooling`, and `tests/release`. They use temporary files and mocks; they do not start the real demo lab, send input to an agent, or operate a simulator. `demo_export` also exercises generated synthetic video when FFmpeg/ffprobe and lossless WebP support (FFmpeg libwebp or `cwebp`) are available; those cases explicitly skip when the tools are absent. Python CI installs those media tools so the export cases run. No extra Python packages are needed.

The Go real mosh test explicitly skips unless `TRILL_REAL_MOSH_TEST=1` is set and `tmux` and `mosh-server` are installed. If requested, run it separately with `cd sidecar && TRILL_REAL_MOSH_TEST=1 go test -run '^TestRealMoshOnIsolatedTmuxSocket$' -v .`. It owns a temporary tmux socket and does not attach to the default server. Routine checks leave this opt-in unset.

See [website/README.md](../website/README.md) for the local browser download, byte-range preview, and full browser test commands. These downloads stay in the checkout, and use isolated browser profiles rather than personal Chrome or Safari data.

## Regenerate and build the iOS project

The generated Xcode project is checked in. Regeneration is only needed when changing project inputs or the generator. The root lockfile fixes xcodeproj and its transitive gems. If the required Bundler is not already available, install it in this checkout, then install the locked gems locally:

```sh
export GEM_HOME="$PWD/.local/gems"
export PATH="$GEM_HOME/bin:$PATH"
gem install --no-document bundler -v 4.0.11
BUNDLE_PATH="$PWD/.local/bundle" bundle _4.0.11_ install
BUNDLE_PATH="$PWD/.local/bundle" bundle _4.0.11_ exec ruby scripts/generate-project.rb
sh scripts/build-ios.sh
```

This does not install global gems. Review generated project changes before committing. `scripts/build-ios.sh` checks the selected simulator SDK version first and builds into `.build/ios`, using `.build/packages` for package checkouts. Signing/release configuration belongs to the native project and release tooling; a simulator check does not require personal device signing. See [RELEASING.md](RELEASING.md) for version preparation, release checks, and the Xcode Cloud hook.

## Run native tests explicitly

The shared `Trill` scheme includes `TrillTests` (unit tests) and `TrillUITests` (UI tests). Use a newly created test simulator so onboarding tests cannot reset a personal simulator. First list the installed types and runtimes:

```sh
xcrun simctl list devicetypes
xcrun simctl list runtimes
```

The following example requires the iPhone 17 Pro device type and iOS 27.0 runtime. Choose another compatible installed pair if these are unavailable; keep the SDK requirement at 27.1+. Run this block in a subshell so cleanup applies only to the simulator it creates:

```sh
(
  set -eu
  TEST_SIMULATOR="$(xcrun simctl create Trill-Tests \
    com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro \
    com.apple.CoreSimulator.SimRuntime.iOS-27-0)"
  trap 'xcrun simctl shutdown "$TEST_SIMULATOR" >/dev/null 2>&1 || true; xcrun simctl delete "$TEST_SIMULATOR"' EXIT
  xcodebuild -project ios/Trill.xcodeproj -scheme Trill \
    -destination "platform=iOS Simulator,id=$TEST_SIMULATOR" \
    -derivedDataPath .build/ios-tests \
    -clonedSourcePackagesDirPath .build/packages \
    -resultBundlePath ".build/TrillTests-$(date +%Y%m%dT%H%M%S).xcresult" \
    -only-testing:TrillTests -only-testing:TrillUITests \
    -skipPackagePluginValidation \
    test
)
```

Xcode boots the selected simulator for testing and applies ad-hoc simulator signing; no personal team is needed. Do not disable code signing for executable test bundles. Do not substitute a simulator currently used for personal work or capture. The two opt-in TLS tests skip unless `TRILL_PROBE_URL` is configured; leave it unset for ordinary tests. A real TLS probe also needs the intended certificate pin in `TRILL_PROBE_PIN`. Do not use production credentials to make these tests pass.

## App E2E demo fixture

[Run the demo server](DEMO_RUNTIME.md) for real HTTPS/Mosh with scripted session workloads, or read its [scope and coverage matrix](DEMO_SERVER.md). The fixture needs no agent or provider installation. Real Xirp/Herdr integration and physical-device validation remain separate test suites.

## Isolated integration lab (manual)

The lab is separate from unit tests and the installed companion. It requires macOS, Go, Python, `tmux`, `mosh-server`, `openssl`, and `/bin/zsh` on PATH/as provided by macOS. It builds private binaries, creates a private tmux socket and synthetic Field Notes workspace, generates a loopback TLS certificate, and starts only its own companion and preview helpers.

Use a fresh, short absolute path. Do not pre-create the final state directory: the lab creates its ownership marker and mode 0700 itself. Ports must be distinct, available, unprivileged, and never 7443. For example, run these commands only when you intend to start integration services:

```sh
LAB_STATE="/tmp/trill-lab-$UID-$(date +%s)"
python3 scripts/demo-lab.py start --state-dir "$LAB_STATE" --api-port 19443 --preview-port 18080
python3 scripts/demo-lab.py status --state-dir "$LAB_STATE"
# When finished, stop only helpers owned by this state directory:
python3 scripts/demo-lab.py stop --state-dir "$LAB_STATE"
```

Keep `LAB_STATE` in the same shell for cleanup. `stop` stops the owned helper processes and preserves the lab's tmux session/workspace; inspect that private socket before any later explicit cleanup. Never run an unqualified `tmux kill-server`, stop a user service, reuse a default tmux socket, or send input to personal sessions. Lab pairing output and credentials are private; do not include them in a pull request.

Starting a coding agent (`demo-lab.py codex`) and simulator capture (`demo.py`) are separate explicit operations. They are not needed for the commands above. Capture additionally requires the installed profile runtime, a prebuilt app, and the reviewed IDB/mise capture environment described in [DEMOS.md](DEMOS.md); the CI setup does not install those tools or claim to validate live recordings.
