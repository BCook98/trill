# Validation

Run checks against a recorded source revision and save commands, toolchain versions, results, skipped probes, and artifact locations with that run. Old test counts and simulator captures do not certify the current build. Publication scan findings, fixture triage, and media review scope are recorded separately in [PUBLICATION_AUDIT.md](PUBLICATION_AUDIT.md).

## Automated checks

From the repository root, run the companion suite in its Go module:

```sh
(cd sidecar && go test -race ./... && go vet ./...)
(cd sidecar && go install golang.org/x/vuln/cmd/govulncheck@v1.9.0 && govulncheck ./...)
```

The default suite covers authentication, pairing, revocation, lease ownership, provider boundaries, setup/control changes, uploads, file confinement, static preview, browser grants, and video limits. Real transport/provider probes are separate opt-in checks; inspect their prerequisites before enabling them.

Use a currently patched compiler, at least the minimum in `sidecar/go.mod`.
The vulnerability check needs network access to the official Go database and
must run with the compiler used to build the binaries. A failed database fetch
is an incomplete scan. Rebuild and restart installed companions after a compiler
security update; changing the module minimum does not patch running binaries.
Install the scanner from inside `sidecar` as shown so Go selects the module's
toolchain. A scanner previously built with an older Go release may fail to load
this module even when `go test` automatically selects the correct compiler.

Follow [DEVELOPMENT.md](DEVELOPMENT.md) for locked dependencies, all Python suites, project generation, and a native-test command that creates its own isolated simulator. Capture tooling has deterministic regression suites:

```sh
python3 -m unittest discover -s tests/demo_lab -p 'test_*.py'
python3 -m unittest discover -s tests/demo_runner -p 'test_*.py'
python3 -m unittest discover -s tests/demo_export -p 'test_*.py'
```

For native checks, select the project-supported Xcode toolchain and a dedicated installed simulator. Use its actual UDID:

```sh
./scripts/build-ios.sh
xcodebuild -project ios/Trill.xcodeproj -scheme Trill \
  -destination 'platform=iOS Simulator,id=YOUR_DEDICATED_SIMULATOR_UDID' \
  -derivedDataPath .build/ios -clonedSourcePackagesDirPath .build/packages \
  -skipPackagePluginValidation -only-testing:TrillTests test
```

The build script honors `DEVELOPER_DIR` or the active `xcode-select` installation; use the same toolchain for the subsequent test command. Native tests cover models, saved-computer storage, switching, input/recovery, preview routing, and file/media lifetime. TLS probes require explicit test endpoints; report skips instead of treating them as exercised. Compilation does not validate physical-device behavior or a signed release.

For the static website, follow [website/README.md](../website/README.md) for a byte-range-capable local server and the isolated Chrome/WebKit suite. Review responsive layouts, both appearances, fallback media, keyboard controls, reduced motion, pause/resume, and transfer recovery.

## Isolated integration checks

Use [DEMO_CREATION.md](DEMO_CREATION.md) for account boundaries and [DEMOS.md](DEMOS.md) for current lab/capture commands. Never use an existing personal session to test input or lifecycle operations.

| Area | Required evidence |
| --- | --- |
| Pairing and trust | Single-use/expired invitations; wrong fingerprint/hostname; revoked device denied; credentials absent from logs/captures. |
| Global control | Read-only metadata works; terminal, mutation, file, upload, and preview routes reject access until control is enabled. |
| Revocation | New API requests denied; paired Mosh leases and HTTP/WebSocket grants close; document in-flight request limits from SECURITY.md. |
| Real terminal | Real output/input and resize on a disposable target; disconnect preserves the underlying session; no input replay after reconnect. |
| Recovery | Idle accepted peer responses resume retained Mosh; stalled resume falls back; background/disconnect cancels pending startup. |
| Providers | Correct authoritative target; supported actions only; Herdr controller ownership and same-device replacement. |
| Files and uploads | Traversal/special-file rejection, bounded reads, static symlink/hidden-path rejection, upload quota/cancellation, correct computer/session. |
| Video | Authenticated bounded download, progress/cancellation, private local playback, cleanup, unsupported media and oversized-file errors. |
| Workspace and prompts | Execute the [UX acceptance matrix](UX_REVIEW.md#acceptance-matrix). |

Browser grants reach a chosen Mac loopback port, not a proven session-owned server. Use only disposable development servers for these checks. Local test helpers, simulator ownership, and cleanup must be explicit; do not restart a normal companion as part of a documentation check.

## Physical-device release checks

Record device/OS/build, network, and outcome for:

- Wi-Fi/cellular handover, packet loss, long suspension/lock, foreground recovery, and process eviction.
- Duo poses and active camera regions; keyboard, hardware keys, largest Dynamic Type, and VoiceOver.
- Dictation/IME ordering and long drafts; photo cancellation and late completion during computer/network changes.
- Camera pairing, certificate renewal/re-pairing, video playback/audio routing, and slow cellular downloads.
- Sustained terminal output, energy use, and interoperability with supported provider/Mosh versions.

Process-eviction terminal restoration remains a limitation. Third-party transport review and adversarial testing remain release work. This checklist does not assert a completed security audit, a clean history scan, or zero vulnerabilities; see [SECURITY.md](../SECURITY.md) and the scoped [publication audit](PUBLICATION_AUDIT.md).
