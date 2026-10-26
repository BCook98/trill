# Publication checks

This is a bounded publication review, not a penetration test or a guarantee that all secrets have been found. Repeat it when preparing a release or changing public assets.

## Source cleanup, 26 October 2026

The publication cleanup removes proprietary-provider implementation observations from API documentation and source comments, website inspiration notes, personal design-preference wording, unnecessary account identifiers, and raw prototype run reports. Supported provider integration contracts, reproducible probes, first-party design decisions, and third-party attribution remain. Personal simulator exclusions belong in ignored local configuration; capture and smoke tools retain their ownership checks.

This is a working-tree cleanup, not a history purge. Earlier versions of the provider investigation remain in the current branch's ancestry as well as older pull-request refs. The existing GitHub repository has been renamed `trill-priv` and remains private. Publish a reviewed snapshot into a new repository without copying `.git`, old refs, or ignored `.local` and `.research` directories. Do not merge the private history back into the public repository. Local state includes actual credentials and private research that are outside the publishable source tree.

## Git history

On 25 October 2026, Gitleaks **v8.30.1** (pinned Go module) scanned the fetched local refs/tags with its default rules and `--all --full-history`. Before the history rewrite described below, the repeated candidate scan through `0c170d0` covered 96 reachable commits, 95 commit diffs, and approximately 9.64 MB of text. The checkout was not shallow. Five detections were reviewed rather than suppressed:

| Location | Detections | Disposition |
| --- | --- | --- |
| `sidecar/preview_test.go` WebSocket handshake fixture | 3 historical instances | RFC example WebSocket nonce, not an authentication credential. The surrounding bearer values are test-server parameters. |
| `vendor/swift-mosh/Tests/SwiftMoshTests/PeerResponseCountTests.swift` | 1 | Deterministic base64 test key representing bytes 0 through 15, used with in-memory endpoints. |
| `vendor/swift-mosh/Tests/SwiftMoshTests/SessionStartupTests.swift` | 1 | Same deterministic fixture key, used with injected test endpoints. |

No live credential was confirmed in these findings. Gitleaks exited nonzero because it found those patterns; this was not a zero-finding scan. There are no broad vendor/test allowlists hiding results. The Go-installed binary may report “version is set by build process”; `go version -m` records the actual module version.

Reproduce from the repository root:

```sh
git fetch origin --tags
mkdir -p .local/publication-audit/tools
GOBIN="$PWD/.local/publication-audit/tools" go install github.com/zricethezav/gitleaks/v8@v8.30.1
go version -m .local/publication-audit/tools/gitleaks
.local/publication-audit/tools/gitleaks git --redact \
  --log-opts='--all --full-history' --report-format json \
  --report-path .local/publication-audit/history.json .
```

Keep reports private and redacted. Investigate every new detection; never publish raw secret matches in issues. This scans available reachable history, not deleted remote refs, inaccessible forks, arbitrary unreachable objects, credentials on local disks, or image/video pixels. Removing a file from the current tree does not remove it from history. On 25 October 2026, `main` was replaced with the single root commit `5a4fc50`, preserving the exact reviewed file tree. The merged remote feature branch was also deleted. A verified private Git bundle preserves the earlier refs for recovery. Existing clones, local worktrees, GitHub pull-request refs and cached commit pages may still retain that earlier history; this rewrite does not erase those copies. If a real secret is found, revoke/rotate it first and coordinate any history remediation.

### Post-reset verification, 25 October 2026

A clean remote clone at `44121dd` contained exactly two commits: root `5a4fc50` and the README update. `git fsck --full` passed. Gitleaks v8.30.1 scanned both commits (8.44 MB) with its default rules and `--all --full-history`. It reported three detections: `sidecar/preview_test.go:591`, `vendor/swift-mosh/Tests/SwiftMoshTests/PeerResponseCountTests.swift:116`, and `vendor/swift-mosh/Tests/SwiftMoshTests/SessionStartupTests.swift:159`. Independent context review confirmed the same fixtures described above, with no new credential found. This was not a zero-finding scan. The pre-reset five detections and archived commit IDs above remain historical evidence.

## Published media

The current homepage uses four Duo clips and six stills from the real isolated Field Notes/Codex lab. Their source/build/run provenance is in [`website/assets/demos/manifest.json`](../website/assets/demos/manifest.json). Review covered the native stills, chronological frame sheets, hinge boundary and full unseeked playback of light/dark sequences. All 2,345 exported frames also received supplementary OCR for recognized pairing/credential patterns, with no matches. OCR is not proof against arbitrary secrets.

The retained footage shows the public demo project, demo-local paths and localhost URLs, plus actual agent edits and output. No live invitation, QR code, bearer credential or unexpected personal session was found. Posters match decoded frame zero and video hashes match the reviewed published assets. Terminal pixels were not fabricated or retouched.

The old `docs/screenshots` images were visually reviewed and removed: they showed stale navigation and, in terminal views, a personal Mac hostname. Unused older iPhone exports and the unused marketing terminal still were removed from the current public tree. The README uses current reviewed Duo assets. Copies remain in the private recovery bundle and may remain in older clones or GitHub pull-request refs. App icons remain original artwork; third-party vendored artwork retains its upstream notices.

For repeat checks, inspect every changed public still, watch each final clip in full, inspect text at native resolution, and compare live assets with local hashes after deployment. Keep captures isolated; never open a personal terminal to obtain marketing evidence. See [demo capture](DEMOS.md) and the [marketing upkeep skill](../.agents/skills/trill-marketing-upkeep/SKILL.md).

## Clean-clone verification

On 25 October 2026, an independent `git clone --no-local` was tested without copying ignored state, installed companion binaries, package checkouts, or private configuration. These commit IDs refer to the history preserved in the private recovery bundle. The initial candidate was `5021cd4`; test-harness corrections were pulled from `0c170d0` with a clean tracked working tree.

| Check | Result |
| --- | --- |
| Go 1.26.10 | `go test ./...`, `go vet ./...`, and `go test -race ./...` passed. |
| Python tooling | 122 tests passed across lab, capture, export, toolchain and release suites; real synthetic-media export cases ran. |
| Locked website install | `npm ci`, syntax checks and downloaded Playwright Chromium/WebKit suites passed. |
| Locked project generation | Repository-local Bundler/gems installed; generated app/test targets and shared version configuration verified, with no signing team. |
| Native simulator build | `scripts/build-ios.sh` passed with the explicitly selected Xcode 27.1 SDK. |
| Native tests | 164 passed, zero failed, two optional TLS probes skipped on a newly created iPhone 17 Pro / iOS 27.0 simulator; the owned simulator was removed afterwards. |
| Skills and docs | Release and marketing skill schemas validated; local documentation links resolved. |

Clean-clone testing exposed hidden lazy-image screenshot waits and executable test bundles built with signing disabled. The browser harness now requests images explicitly with a timeout; native instructions retain normal ad-hoc simulator signing. Independent review also corrected Cloud hook placement beside `ios/Trill.xcodeproj` and confirmed the documented trust boundaries.

These checks do not certify a fresh Mac/provider installation, iOS 18 runtime behavior, physical-device roaming, signed distribution, or App Store Connect configuration. The normal installer was not run over the owner's installation. Provider login, firewall/network policy, trusted-device pairing and real-device release checks remain environment-dependent steps in the setup/release guides.

## Working-tree review on 25 October 2026

A subsequent review covered the working tree based on `44121dd`, including the uncommitted demo runtime, native, privacy, release, and transport changes. This was not another clean-clone check. Review focused on authentication and certificate pinning, filesystem confinement, upload bounds, preview capabilities, subprocess boundaries, saved-computer changes, build configuration, contributor instructions, and dependency notices.

Corrections made during this review:

- Ignore local `.env` files and Apple signing exports while retaining example environment files.
- Remove the personal `DEVELOPMENT_TEAM` from the shared app configurations, matching the generator and documented contributor setup.
- Correct the development guide's stale Go 1.24 minimum. Install the vulnerability scanner inside the Go module so it is built with the module-selected compiler; an existing scanner built with Go 1.25 failed to load this Go 1.26 module.
- Make the main-actor WebKit test double's protocol conformance explicit with runtime isolation checking, resolving its Swift concurrency warning.
- Document the vendored transport's datagram, fragment-assembly, and decompression limits in the patch inventory and third-party overview.

Verification against this working tree:

| Check | Result |
| --- | --- |
| Go 1.26.10 | `go test -race ./...` and `go vet ./...` passed across all nine packages. |
| Reachable Go vulnerabilities | Pinned `govulncheck` v1.4.0 rebuilt with Go 1.26.10: no vulnerabilities found. |
| Python | 296 tooling tests and six Fly startup tests passed, including synthetic media export cases. |
| Website | Syntax checks and the Chromium/WebKit browser suite passed using the installed locked dependencies and isolated browser profiles. `npm audit` reported zero vulnerabilities. Dependency installation was not repeated. |
| Vendored Mosh | 108 tests executed, zero failures, four explicit real-server integration skips. |
| iOS | SDK 27.1 simulator build passed. On a fresh iPhone Duo / iOS 27.1 simulator, 167 unit tests and two UI tests completed with zero failures and two opt-in TLS skips. The corrected test-double conformance compiled without its previous concurrency warning. |
| Supporting checks | Local links in root/docs guides, shell/Ruby syntax, release version preflight, and `git diff --check` passed. |

Gitleaks v8.30.1 scanned a temporary copy of all Git-tracked and nonignored untracked files: approximately 13.45 MB, four detections. Two were the RFC WebSocket example nonce in `sidecar/preview_test.go` and `sidecar/demo_test.go`; two were the deterministic bytes-0-through-15 Mosh test key described above. A separate `git --all --full-history` scan covered 154 commits available through local refs, approximately 18.91 MB, and eight historical instances of those fixtures. Both scans exited nonzero for these reviewed matches; neither was a zero-finding scan. No live credential was confirmed. Reports remain in ignored local storage. No remote refs were fetched, and this review did not repeat the image/video pixel review.

The first native run on a fresh iPhone 17 Pro / iOS 27.0 simulator failed to launch the test host and was interrupted. The successful retry explicitly booted a fresh Duo simulator and disabled parallel testing. The first failure was not diagnosed further; this run does not establish iOS 27.0 test coverage. Both runs owned their simulators and included cleanup. Native results are saved locally in `.build/review-duo.xcresult`; other review logs and reports remain under ignored `.build` and `.local` directories.

The project has an MIT license, dependency notices, installation and removal instructions, contribution/security guides, locked tooling, and broad regression coverage. GitHub checks deliberately run only on `main`; pull requests and forks receive no automatic checks under the current policy. Maintainers must obtain validation before merging contributions. Native hardware behavior, iOS 18 execution, fresh provider installation, signed distribution, and real-server integration remain outside this run. Large stateful files such as `WorkspaceView.swift`, `TerminalConnection.swift`, and `service.go` merit gradual extraction when their behavior changes; this review did not undertake an unrelated structural rewrite.

## Marketing media review on 26 October 2026

Rebuilt native source `78c1cec` with Xcode 27.1 and recorded new light/dark Duo utility runs in the owned terminal lab. Replaced all four active videos and six poster/preview images after review. Native source was clean; the checkout contained other existing changes. Build hashes, runtime, source runs, measured cuts and crop are recorded in `website/assets/demos/manifest.json`; the detailed capture record is in `docs/DEMOS.md`.

All exports fully decoded, use faststart and contain no audio; each player poster exactly matched decoded frame zero. Both themes completed uninterrupted browser playback, with chronological visual review of the workflow and fold. Desktop/mobile light/dark screenshots and the Chromium/WebKit suite passed. Local Vision OCR checked all 2,272 exported frames with no recognized pairing/credential matches; this supplements visual review rather than proving arbitrary content secret-free. The live agent's warning indicator remains visible.

Focused checks passed: 95 capture runner tests, 9 lab tests, 16 exporter tests, website syntax and skill validation. Updated the exact-build Duo map and skill reference for the Sessions/Agents layout and title-free terminal. The demo helper processes started for capture were stopped; their isolated tmux session was preserved. Personal simulators, sessions and companion processes were not operated on. This review covers local capture and website validation; publication is verified separately. Physical Safari/VoiceOver and native hardware remain outside this review.
