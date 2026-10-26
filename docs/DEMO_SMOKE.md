# Native app smoke harness

`scripts/demo-smoke.py` drives the ordinary Trill app through Facebook IDB, using the synthetic Go backend described in [DEMO_SERVER.md](DEMO_SERVER.md). It does not require Xirp, Herdr, tmux, Codex, or an AI account. The real pairing, TLS, authentication, Mosh, file serving and preview paths remain in use.

## Qualification status (25 October 2026)

Integrated `87386db` passed 302 Python tooling tests with zero skips, including
148 smoke tests and 6 deployment tests. The 10 new focused AX regressions and
runner change `9c78f838` were independently approved. The website npm check also
passed; that is not publication evidence. These checks test tooling, not the
native app. Native evidence below uses synthetic backends and simulator source builds;
it does not establish physical TestFlight behavior or full provider compatibility.

| Evidence | Result and scope |
| --- | --- |
| Updated app | Release source `6461a2bc736192aab16365c20695ff0ebfd11b4e`, version `2026.10.25`, local build `1`; includes revised policy, short disclosures and separate v2 consent keys |
| Consent | Four independent native consent tests passed, zero skips; attended in-place `967a057` → `6461a2b` upgrade retained pairing/v1 state, showed separate renewed v2 disclosures, remembered terminal approval and left photo approval false after cancellation; no new-v2 photo approval/upload |
| Duo light/dark, iOS 27.1 | Attended core TLS/Mosh, deliberate input/backend acknowledgment, file/source/static/live previews, reported 65-second dwell/fresh input and cover/unfolded/book/tabletop input observed; native geometry and screenshots verify the actual layouts |
| Hosted 17 Pro baseline, iOS 27.0 | `967a057` passed real public TLS/Mosh, exact photo-content comparison, static/live previews, reported 65-second dwell/fresh input and archive/Projects reopen; API-seeded creation, older policy/consent |
| Native creation, 17 Pro/iOS 27.0 | Attended `967a057` run passed all three forms plus archive/Projects reopen against an isolated local demo; exact rename fidelity failed then and later passed with the scoped driver |
| Earlier local qualifications | Happy journey, capacity/busy/dropped-lease recovery, photo upload/acknowledgment and creation observed; separate action checks passed archive/exclusion, Projects reopen and two-computer pairing/switch/relaunch/forget-only-B |
| Audited unattended 17 Pro | App `6461a2b`, runner `df73ca2`, light/iOS 27.0: 11 happy/lifecycle assertions passed, including post-relaunch input and exact rename; all 35 artifacts hash-matched, cleanup/backend stop passed. Background-state proof excluded |
| Unattended Duo | `6fc` passed pairing/Mosh/input/live/source/static checks, then blocked on post-foreground AX; no fresh input/poses. Fresh `9c78f83` blocked at the first TLS invitation/catalog assertion after about 41 seconds, with no later assertions/poses. Both cases cleaned up; fresh backend stopped. Full unattended Duo remains a gap |
| Reviewed AX guards | Runner/test-only `9c78f8382656ddcbebe38bdd7e236327f644e0ae` adds meaningful roots, guarded absence, explicit VoiceOver and saved SpringBoard/Trill identity; independently approved and integrated in `87386db`, fresh native run blocked at pairing/catalog, native qualification unestablished, no app changes |

Earlier failures remain in their private manifests. An attended recovery does
not turn the original automated run into a pass. Each live invocation returns
`0` for pass, `1` for failed observations/uncertain input, or `2` for blocked
prerequisites/selectors. Inspect assertions and provenance, not just exit codes.
For signed-build and privacy-publication gates, see the
[external review packet](TESTFLIGHT_REVIEW.md#remaining-external-beta-gates).

## Updated consent and Duo evidence

The updated app was installed in place over `967a057`, preserving the existing
pairing and remembered v1 permissions. Terminal and photo actions each required
the new v2 disclosure. Terminal approval was remembered; cancelling photo left it
false. New-v2 photo approval/upload was deliberately not exercised. Four native
consent regressions passed with zero skips. The original old-photo import timeout
and below-fold permission-selector block remain recorded; attended readback and
navigation completed the check without replaying the committed import. This is
source-build migration evidence, not an upgrade test of uploaded build 9.

Duo light and dark runs used app source `6461a2b`, iOS 27.1 and an isolated local
backend from `f54ecc5`. Both completed attended core journeys, reported 65-second
dwell followed by fresh input, and independently acknowledged input after hinge 0°, 180°
and 90° transitions. Hinge 90° in portrait produced a vertical book split;
additional native landscape-left orientation established the requested tall
horizontal tabletop split. Keyboard/action placement was checked in cover,
unfolded and tabletop configurations using screenshots plus native AX geometry.
In both tabletop appearances the terminal occupied y=116.33–455.67 and Return
y=498.67–542.67; visible keys were below the controls. The broad native input-view
frame alone was not accepted as proof of keyboard overlap or correct placement.

These were attended passes with original automation blocks preserved. The light
attended manifest retains earlier tool failures; the dark attended manifest
passed. Artifact hashes across all four original/attended manifests matched with
zero mismatches. Photos, preview-expiry renewal, multi-computer and lifecycle
fault groups were not repeated in this Duo core qualification. This is not full
accessibility certification or a clean full unattended Duo pass.

## Unattended native qualification

The frozen `6fc` Duo run passed normal pairing, real Mosh, deliberate input and
live/source/static preview assertions. After foregrounding it blocked waiting for usable
native accessibility: the screenshot showed the Mosh terminal, but AX exposed
only an unnamed, zero-size application. No new input or pose transitions followed.
This is an automation/observation block; the screenshot does not establish a
fresh-input recovery pass or identify a product defect. Cleanup passed in full.
The attended light/dark observations and pose evidence above remain separate.
The fresh `9c78f838` Duo run then blocked at its first normal TLS invitation/
catalog assertion: expected native AX state was unavailable after about 41 seconds.
It ran no later assertions, terminal input or pose checks. Ownership verification,
app termination and simulator shutdown passed; `backendStopped=true` was recorded.
These are two separate blocked cases, not one combined happy-path pass. The
reviewed AX guards and unattended poses remain without native qualification.

The iPhone 17 Pro light run (`df73ca2daec1c18b37e5ef86dcd748ffff7b99ea`, app
`6461a2b`, iOS 27.0) completed unattended happy/lifecycle PASS: 11 assertions,
including fresh post-relaunch input, one terminal creation, exact rename,
archive/exclusion and Projects reopen. Independent audit accepted the lifecycle
paths and matched all 35 referenced artifact hashes with zero mismatches.
Cleanup, owned lease release and backend stop passed. Reopened session detail
was observed; no connection/input into that newly reopened terminal is claimed.

The old manifest calls its 65.003-second timer confirmed dwell, but no positive
background-identity proof was retained. Accept that field only as elapsed dwell
after HOME, not confirmed background/suspension or transport-loss recovery.
Post-relaunch fresh input and the audited lifecycle passes remain valid.
The reviewed guards below do not retroactively qualify the old measurement.

## Hosted native check (25 October 2026)

An attended iPhone 17 Pro/iOS 27.0 run used source `967a057`, version
`2026.10.25 (1)`, against `https://trill-demo.fly.dev`. It passed public Mosh input,
file source/static preview, sample-photo upload/acknowledgment, archive/Projects
reopen, and a reported 65.009-second dwell followed by fresh input. The
uploaded JPEG was fetched through authenticated API reads, decoded and compared
with the generated checkerboard; the native draft matched its path.

The run found a demo live-preview session-selection bug. Fix `c474a08` binds
upstream requests to the authenticated preview grant's session; fresh input and
scoped browser capture passed after deployment. Creation was API-seeded for
isolation. This attended baseline predates the policy/consent update and does
not qualify native creation, post-expiry recovery, video playback or TestFlight.

## Native creation follow-up (25 October 2026)

An attended iPhone 17 Pro/iOS 27.0 run used `967a057` against a fresh local demo.
All three native forms created exactly one session: Terminal, Coding agent in
Project folder, and Coding agent in New worktree. Independent backend receipts
verified project, type, exact goal and separate worktree workspace. No API
creation was used. This proves synthetic native form contracts, not actual
agent startup or Git worktree creation. Archive excluded the new terminal from
Sessions; Projects reopen restored the same ID.

Exact rename fidelity failed: input requested `native terminal renamed`, the
field displayed `Native renamed`, and one Save stored `Native`. The cause
remains unresolved; keyboard composition or automation may be involved, and no
app defect was established. Earlier successful rename observations do not erase
this failure. The later unattended `df73` lifecycle run passed exact rename
with the scoped driver; this qualifies that path without resolving the earlier
composition/automation discrepancy or establishing an app defect.
The creation goal field also needed attended focus because the submit inset
occluded its center; attended success does not qualify unattended goal entry.
Owned registrations, sessions, backend and simulator were cleaned up. Neither
personal sessions nor the shared Fly host were used.

## Automation guards and retained failures

The revised runner has mock regression coverage. Fresh unattended qualification
is listed above; attended evidence does not qualify unobserved automation paths.

Background review found a frozen-runner defect: unnamed `AXApplication` or empty
AX could be accepted as proof that Trill left the foreground. Absence is not
positive identification of another foreground app. Where positive application
identity was not retained, recorded elapsed waits and later acknowledged input
establish those observations only, not independently confirmed background dwell.
The existing attended facts remain recorded. Revision `9c78f838` changes only
the runner and tests. It was independently approved and integrated in `87386db`.
Its fresh Duo run blocked at pairing/catalog before qualifying later recovery or
pose checks. The implemented guards are:

- Native reads require one enabled, nonempty named `AXApplication` root with
  finite, nonzero geometry. Unnamed, empty or ambiguous roots block.
- Absence checks additionally require an identified enabled Trill control or
  editor inside that root. Root-only, container-only and loading-text snapshots
  cannot prove that a row or dialog disappeared.
- The fallback explicitly requests `--client-type voiceOver` rather than relying
  on IDB's default client.
- Background recovery records named, usable SpringBoard proof before starting
  the dwell timer, then named Trill proof before fresh input. Identity receipts
  and application frames accompany the timing fields; a missing proof blocks.

The 148 passing smoke tests include 10 new focused AX regressions. Mock results
alone do not qualify real native identity reads or recovery.

- **Search and editor identity:** pairing searches for the backend-selected
  session. Duo's Search side-rail button may need activation before a field
  exists; one fresh, unambiguous catalog context is required. An unlabeled field
  must be unique and contain the native Search icon. Replacement uses Select
  All and exact readback. Rename fields are confined to the exact named sheet
  and verified frame; underlying Search cannot satisfy focus or readback.
- **Tutorial and HID failures:** only the positively identified slide-to-type
  tutorial with one Continue button may be dismissed, once. The Duo light run
  also hit an invalidated `dtuhidd` connection after restart; dismissing the
  tutorial did not fix it. Attended recovery replaced only the identity-verified
  owned companion. The runner does not retry uncertain input or alter unrelated
  companions.
- **Exact preview observations:** original Duo dark OCR read `0` as `O` and
  correctly blocked despite a visibly correct page. A fresh letter-only heading
  passed attended checks. Generated browser headings now use 12 random letters
  excluding `i/l/o` and digits, retaining at least 48 bits of entropy. OCR still
  requires exact current-region text; no fuzzy substitution is allowed. A moved
  pane discards its crop and repeats reads/capture only, never the input.
- **One-shot actions:** context-menu holds require fresh session-name/parent-row
  identity and geometry. Rename requires exact scoped draft immediately before
  Save and exact backend equality afterward. Missing, changed or ambiguous
  state blocks; uncertain input, Save, creation and other mutations are not
  automatically retried or normalized into success.

## Prerequisites

- macOS with Xcode, its command-line tools, and the requested simulator runtimes installed. Select Xcode using `DEVELOPER_DIR` if it is not the active installation.
- Python 3.12 or newer; the harness uses only the standard library, plus macOS `sips`/ImageIO to decode the actual uploaded JPEG and the local Vision helper for OCR.
- Facebook IDB available through `mise exec -- idb`, with the installed patched companion's `ax-coresimulator`, XCTest client, exact identity guards, screenshot cropping and Duo hinge support. Upstream IDB alone may lack these additions. `mise exec -- idb ui tap --help` and `ui describe-all --help` describe the required flags.
- An explicitly built simulator `Trill.app`, its exact source revision, and the newly built `trill` demo executable. The harness does not build the app or infer binary provenance from a dirty working tree. See [Development](DEVELOPMENT.md) for builds.
- `mosh-server` on PATH for the backend. The harness never substitutes a fake Mosh transport.

Default profiles match the current marketing fixture device types: iPhone 17 Pro on iOS 27.0 and iPhone Duo on iOS 27.1. `--runtime` overrides one explicitly selected profile. This is simulator coverage, independent of the minimum iOS version supported by Trill.

## Start an isolated run

Choose a **new** absolute directory below an existing private development directory. Canonicalize `/tmp` to `/private/tmp` on macOS when starting the backend; the CLI verifies the canonical owner path.

```sh
python3 scripts/demo-smoke.py init --state-dir "$PWD/.local/demo-smoke"
```

`init` refuses an existing directory. It creates a mode-0700 harness root, random ownership identity and simulator registry. It prints the canonical root and the reserved `backend-a` and `backend-b` paths; it does not start anything. Use the printed paths below.

Start a demo backend in another terminal, with unused TCP and UDP ports. Keep it in the foreground:

```sh
/absolute/path/to/trill demo serve \
  --state-dir /canonical/path/to/demo-smoke/backend-a \
  --listen 127.0.0.1:19443 \
  --address https://localhost:19443 \
  --mosh-host 127.0.0.1 \
  --udp-ports 60300:60310 \
  --profile mixed \
  --preview-listen 127.0.0.1:18080
```

The backend creates its own certificate and private fixture state. Never substitute your installed companion or reviewer environment. The harness accepts only a running `trill-demo-v1` backend with loopback HTTPS and a matching process/certificate/address identity throughout the run.

Run from a checkout containing the approved AX guards in `9c78f838` (integrated
in `87386db`) or a reviewed successor. Runner revision, app-build source revision
and backend executable are separate provenance: record all three. The app used
for the updated evidence is `6461a2bc736192aab16365c20695ff0ebfd11b4e`, version
`2026.10.25 (1)`; pass the actual revision for the supplied app, not the current
runner checkout's revision. See the manifest hashes when comparing reruns.

Run the smallest journey first, retaining the 65-second requested dwell:

```sh
python3 scripts/demo-smoke.py run \
  --state-dir /canonical/path/to/demo-smoke \
  --trill /absolute/path/to/trill \
  --backend-state /canonical/path/to/demo-smoke/backend-a \
  --app /absolute/path/to/Build/Products/Release-iphonesimulator/Trill.app \
  --app-source-revision FULL_40_CHARACTER_BUILD_SOURCE_REVISION \
  --profile iphone-17-pro --appearance light --scenarios happy \
  --background-seconds 65
```

The runner creates a **new** simulator before installing the specified app. It cannot adopt a supplied UDID, modify an existing app installation or operate on physical devices. It clears inherited IDB routing overrides and targets only the newly verified simulator. The simulator registry binds every target to the owner UUID, device type, runtime and random name. It rejects any identity present before creation.

Both native runners also read `.local/protected-simulators.json` in the runner's checkout: an optional, git-ignored JSON list of private simulator UUIDs. For example, `["11111111-1111-4111-8111-111111111111"]` protects that synthetic example identity; replace it with your personal simulator IDs locally. Configured IDs are rejected even with otherwise valid ownership metadata. Malformed or unreadable configuration blocks simulator operations. Fresh checkouts may omit the file; strict ownership and new-simulator requirements still apply. Keep personal simulator IDs out of tracked files.

`--profile all --appearance both` requests 17 Pro and Duo in light and dark.
Duo begins in cover, then exercises the remaining poses after a passing journey:

| Pose | Native IDB rotation | Hinge angle |
| --- | --- | --- |
| Cover | `PORTRAIT` | 0° |
| Unfolded | `PORTRAIT` | 180° |
| Book | `PORTRAIT` | 90° |
| Tabletop | `LANDSCAPE_LEFT` | 90° |

The runner requires an exact rotation enum and numeric hinge read, then sends a
new unique terminal command after each post-journey transition. A wrong or
unavailable read blocks input at that pose; no pose mutation is retried. Successful
reads enter the manifest alongside `visual_layout: "not asserted"`.

The native orientation enum does not describe the rendered app's aspect ratio.
The attended light/dark [geometry evidence](#updated-consent-and-duo-evidence)
establishes book versus tabletop and visible keyboard/action placement. Those
observations do not qualify unexecuted unattended poses; the older
`half-folded-portrait` manifest name described book, not tabletop.

Missing runtime, rotation/hinge controls or selectors block the case. Revised
unattended pose checks and the complete appearance matrix remain unqualified.
Control readback and acknowledged input establish functional behavior only;
angle alone cannot establish keyboard placement, HIG or accessibility compliance.

The requested background dwell defaults to 65 seconds. `--background-seconds 0` is useful when developing the driver but cannot establish long suspension recovery. An elapsed timer alone is not background proof; retain positive application-identity evidence before qualifying the dwell. The runner never changes global networking/firewall rules.

## Scenarios and current boundaries

| Scenario | Implemented native actions and assertions | Remaining qualification |
| --- | --- | --- |
| `happy` | Fresh normal invitation deep link, Pair, native catalog, Open terminal/Allow once, Mosh status, verified Write draft, Insert without execution, Return once, changed fixture heading, live preview, `web/index.html` source/static preview, background/resume and fresh acknowledged command | Attended updated-app Duo light/dark and hosted 17 Pro core/browser observations remain recorded. Fresh Duo automation passed through live/source/static preview, then blocked on post-foreground AX before fresh input/poses. Fresh 17 Pro light happy/lifecycle passed, including input after the reported dwell; background identity proof remains excluded. Fresh `9c78f83` blocked at its first pairing/catalog assertion; native guard/pose qualification remains open. |
| `lifecycle` | Create terminal once, backend creation receipt, rename confirmation, archive confirmation, absence from current sessions, find in Projects, reopen with backend receipt | All three native creation forms plus archive/Projects reopen passed attended on `967a057`; the same run failed exact rename fidelity. Fresh 17 Pro light `df73` passed guarded lifecycle including exact rename, archive/exclusion and Projects reopen. Coding-style/worktree creation is not yet qualified unattended, and stop/resume needs separate evidence. |
| `two-computers` | Pair a second normal invitation, inspect address, select the address-scoped saved-computer UUID row, relaunch and verify active address, forget only B and retain A | Passed on 17 Pro light/v3 in a separate actions qualification. Requires independent `backend-b`; context-menu points still require qualification on each additional layout. Draft/consent isolation and disconnect timing need separate assertions. |
| `photo` | Generate a synthetic PNG, import it only to the owned simulator, open Write and native photo consent/picker | Dense checkerboard comparison verifies all 36 tiles, the fresh native Photo element and uploaded JPEG before accepting the exact draft path; attended hosted 17 Pro checks passed. The in-place upgrade separately verified terminal renewal/photo cancellation and retained v1 state. New-v2 photo approval/upload and repeated native picker flows remain separate gaps; other layouts are unqualified. |
| Faults | `capacity`/`busy`: observe actual native error then automatic/manual recovery and fresh input. `drop-leases`: kill only owned backend leases, background/resume through the native recovery path, then require fresh input. `preview-expired`: expire grants, produce a unique new heading through the real terminal, and reload the retained browser pane; the new heading must render. | Capacity, busy and dropped-lease recovery passed on 17 Pro light/v3. Grant-expiry qualification is pending. Provider/create/API/preview/upload failures still need native assertions and report blocked. |

List scenarios after `happy`, for example `--scenarios happy lifecycle`. The first incomplete/uncertain scenario stops further UI actions; dependent cases are recorded blocked. Session mutations, input, Insert and Return are never retried automatically. Polls repeat reads only. The runner verifies the native draft exactly and requires a new backend heading after deliberate Return.

For two computers, start a second foreground process using the printed `backend-b` path, distinct HTTPS/preview ports, UDP range and certificate, then add `--second-backend-state /canonical/path/to/demo-smoke/backend-b`. Both processes belong to the same harness root; an arbitrary existing demo directory is rejected.

## Backend evidence contract

The outer `trill demo status --state-dir ABS --json` identity is stable:

```json
{"kind":"trill-demo-v1","running":true,"address":"https://localhost:19443","profile":"mixed","fingerprint":"certificate SHA-256","previewPort":18080,"pid":12345,"state":{}}
```

The `StateView`/`Backend.adapt` boundary keeps the runner independent of raw terminal transcripts. It consumes `state.sessions[]` identifiers, names, project/provider, status/archive state, and optional attachability/heading fields. With the current minimal status, known initial fixture IDs derive their independent heading receipt from **only** the owned `fixtures/<project>/web/index.html` output. The adapter does not read pairing stores, private keys or persisted terminal transcripts. Created/worktree session directories require a safe directory/heading receipt before those commands can be tested. Missing evidence is blocked.

Fixture projects currently have the names `Demo · Xirp` and `Demo · Herdr`; the fallback project adapter uses those names and checks them against the actual UI before use. A future safe `state.projects[]` takes precedence. Photo assertions inspect only the owned backend’s session-hashed attachment directory, requiring exactly one new private JPEG, its digest/size, and the matching native draft. Picker coordinates are derived anew from the generated checkerboard pixels, bound to the live simulator’s AX scale and the observed `PXGGridLayout-Info` Photo element; no historical coordinates are used.

`trill demo pair --state-dir ABS --url` must emit one ordinary ten-minute invitation URL. The runner opens it in memory, uses normal native Pair, and never writes the link/payload to disk or a command log. No manual token injection, Keychain seeding or test-only app transport is used.

## Artifacts and outcome rules

Each private `run-<uuid>/manifest.json` is updated after assertions and finalized after cleanup. It records:

- Exact app path, executable SHA-256, bundle/version/build and operator-supplied source revision; runner and backend executable hashes.
- Simulator UDID/name/profile, runtime/build, appearance, backend identity and requested scenario outcomes.
- Every assertion's timing, pass/fail/blocked outcome and evidence references. Successful native assertions also receive an allowlisted independent backend receipt.
- Sanitized AX receipts, private screenshots and hashes. Browser OCR runs locally and captures only a unique Page preview region. Fresh AX reads verify that the region remains present with unchanged geometry before/after capture and OCR; a stale frame or terminal echo cannot satisfy a browser assertion. Source checks require HTML source context.
- Reported dwell, separate roundtrip/fresh-input timing, cleanup status and retained foreground/background identity evidence where available. Neither the frozen missing-identity guard nor an elapsed timer establishes confirmed background duration. Earlier manifests used `background_seconds_observed` for the whole roundtrip; that legacy field is not exact background time. A blocked/failed assertion never becomes a pass because a screenshot or API call succeeded.

Tool logs contain command category, elapsed time and exit status only, never arguments/stdout/stderr. AX values are omitted. Pairing screens and detected credentials suppress screenshots entirely. Build hashes and certificate fingerprints are deliberate provenance, not bearer credentials. All fixture text entered by this harness is generated public sample text. Artifacts stay private; do not publish recordings/screenshots without review.

Unavailable AX/OCR, missing safe state, or unsupported automation is **blocked**. An observed mismatch, changed backend identity during input, or uncertain mutation cannot be inferred successful. No mocked unit result is written as a live app manifest. This harness does not run the marketing capture runner or launch its real coding agent fixture.

## Cleanup

Finalization does not navigate native menus or claim native Disconnect coverage. After verifying the simulator's ownership, it attempts app termination and simulator shutdown independently, including after a failed scenario. Termination failure cannot skip shutdown. The manifest records each action in `cleanup_steps`; any failure blocks an otherwise passing run, while an earlier failed scenario and its reason stay intact. If ownership cannot be verified, both simulator mutations are withheld. The simulator and artifacts remain available for inspection. Use the explicit cleanup command to delete only fully matching registry entries:

```sh
python3 scripts/demo-smoke.py cleanup --state-dir /canonical/path/to/demo-smoke
```

All registry entries are validated before any deletion. A missing/mismatched target blocks cleanup; inspect the recorded identities rather than deleting similarly named devices. The harness does not stop/reset the backend, revoke its devices, or delete its state. After a case it invokes `drop-leases` on only these identity-verified, exclusive demo processes to release hidden terminal tabs too; this keeps later simulator cases independent. Stop each foreground demo using its own CLI when finished. An interrupted cleanup may leave a backend terminal lease until normal expiry; never steal another controller to clear a smoke fixture.

## Checks and release gaps

```sh
python3 -B -m unittest discover -s tests/demo_smoke -v
python3 -B scripts/demo-smoke.py --help
```

These mock-only tests cover ownership, private state, protected/existing simulator rejection, local synthetic backend identity, app provenance, credential redaction, exact draft verification, no uncertain-input retries, independent backend acknowledgments and honest blocked outcomes. They do not boot a simulator, contact a backend or establish a live E2E pass.

Keep separate reports for:

1. This synthetic-backend native E2E journey.
2. Disposable **real Xirp/tmux and Herdr integration**: actual provider catalog/lifecycle, worktrees, controller ownership and reconnect behavior.
3. Physical-device release checks, explicitly deferred to the release owner: exact TestFlight binary, camera QR, dictation, hardware keyboard, genuine suspension/eviction, Wi-Fi/cellular/VPN roaming, IPv6/NAT64, accessibility, sustained output and battery behavior.

Camera-photo metadata sanitization, video playback/cleanup, negative pairing/revocation, detailed WebSocket/navigation checks, input modifiers, lease expiry/cancellation, the remaining fault cases and UI polish need their own observed assertions. A synthetic PNG upload cannot prove GPS/EXIF removal from a camera photo. Dropped-lease coverage uses a real owned-child exit plus background/resume; it does not establish automatic detection of a dead foreground peer or real packet-loss recovery. Neither demo success nor simulator source-build evidence waives those gates. Keep CI local/manual or trusted-main only; do not enable device tests on untrusted PRs.
