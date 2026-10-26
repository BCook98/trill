# Native demo captures

`scripts/demo.py` drives the prebuilt native Trill app with `mise exec -- idb`. It creates dedicated simulators, pairs through the real native UI, grants terminal consent, and captures the private demo lab over real mosh. It never builds the app. Terminal input is disabled by default; the utility scenario requires explicit opt-in and sends a real request through the native draft, Insert, and Return controls. The lab owns the actual tmux session and Field Notes HTML/CSS/JS/SVG project. Any coding-agent UI must come from an actual running agent started separately by the operator.

## Quick start

Run from the checkout after building `Trill.app` separately, starting an owned lab with a real idle coding agent, and reviewing the exact-build UI map as described below:

```sh
python3 scripts/demo.py list
python3 scripts/demo.py run --profile iphone-duo \
  --app "$PWD/.build/ios/Build/Products/Debug-iphonesimulator/Trill.app" \
  --appearance both --scenario utility --allow-terminal-input --ui-map-dir demos/profiles/maps
```

`both` is the default. Devices and appearances run sequentially, sharing one lab. Each device gets a light and a dark website video plus original recordings and PNG stills. Duo captures its cover first, then its unfolded screen in one actual continuous recording by default. IDB retains the initial video canvas and fits the resized unfolded framebuffer into it with black padding; native PNG stills retain each pose’s full resolution. The hinge event occurs inside the actual recording, with no generated transition. `--hinge-recording segments` instead captures separate actual pose recordings and makes an explicit hard cut on a common padded canvas.

Profiles may explicitly set `orientation` to `PORTRAIT`, `PORTRAIT_UPSIDE_DOWN`, `LANDSCAPE_LEFT`, or `LANDSCAPE_RIGHT`. Duo sets `PORTRAIT`: prepare and each pose readiness step apply targeted `idb rotation PORTRAIT` before the hinge command, since hinge alone preserves a retained landscape orientation. Omitting orientation leaves rotation unchanged (including iPhone 17 Pro). Exact reviewed screenshot dimension checks still apply.

The initial profiles pin iPhone 17 Pro to iOS 27.0 and iPhone Duo to the Duo-only iOS 27.1 runtime. Missing runtimes fail without falling back to another device. Child commands honor an explicit `DEVELOPER_DIR`, otherwise the active `xcode-select` installation. Select an Xcode with the profile’s SDK/runtime; no private repository toolchain path is required. IDB's installed `set` command changes preferences; actual appearance is set and read back with targeted `xcrun simctl ui UDID appearance light|dark`.

Use `--dry-run` on `prepare`, `capture`, or `run` to validate and print the plan without commands, file writes, lab startup, or simulator operations. `--app` remains required by `prepare`/`run` in dry-run mode.

## Separate preparation and capture

```sh
python3 scripts/demo.py prepare --profile all --app /absolute/path/Trill.app
python3 scripts/demo.py capture --profile all --scenario terminal --appearance both
python3 scripts/demo.py capture --profile iphone-duo --scenario utility --appearance dark --allow-terminal-input --ui-map-dir demos/profiles/maps
```

`prepare` prints each created/reused UDID and lock path. `capture` requires a booted simulator and an unchanged prebuilt app previously recorded by `prepare`. `run` performs both operations. Each capture restarts only the owned Trill app process before pairing to clear retained navigation; it preserves the simulator, keychain, private tmux session, and agent. `--udid UUID` asserts a previously owned target; it cannot adopt an existing simulator. Use a new state directory to create another dedicated pair rather than manually editing ownership metadata.

`terminal` captures the real lab terminal only. `workspace` adds native Files and the actual local Field Notes browser preview on the final device pose. It does not type into or execute anything in the terminal. Add declarative scenarios under `demos/scenarios/`, or pass a JSON file to `--scenario`. Supported operations are `tap`, `wait`, `set-preview`, `pause`, `screenshot`, `text-input`, `wait-agent`, `wait-heading`, `wait-source`, `wait-preview`, and `ensure-workspace`; optional `pose` is a pose ID, `first`, `all`, or `last` (the default). Targets use `AXLabel` by default or explicit `AXUniqueId`. Native browser content is checked with local Vision OCR when AX does not expose WebKit text. Screenshot names are validated filenames; arbitrary shell commands are not supported.

Options include `--fps 30`, `--scale 1`, `--bitrate 8000000`, `--hold 4`, `--timeout 35`, and `--screenshots-only`. Recording startup has a configurable two-second allowance (`--record-startup`); IDB has no recording-ready event. Video requires `ffmpeg` and `ffprobe`. A private resize probe on the dedicated Duo verified a 1398×2034 initial canvas followed by the full 2853×2007 unfolded screen, letterboxed by IDB in the continuous video. This behavior is specific to the tested IDB/runtime; use segments if a different installation loses frames or crops on resize. The recorder receives SIGINT to finalize MP4, then only its own child is terminated if finalization stalls. Ctrl-C and SIGTERM discard incomplete imagery and leave a failed manifest.

## Lab and ownership

The default lab path is the absolute `REPO/.local/demo-capture/lab`. Runner metadata is in its sibling `lab-runner/`, including `devices.json` and `runner.lock`. The lab owns its own `devices.json` credential store; the runner never reads or overwrites it. The runner leaves the lab directory's creation and ownership marker to `demo-lab.py`.

The frozen lab interface is:

```sh
python3 scripts/demo-lab.py start --state-dir /absolute/lab
python3 scripts/demo-lab.py invite --state-dir /absolute/lab --output /new/private/file
python3 scripts/demo-lab.py stop --state-dir /absolute/lab
```

`start` returns public JSON with `state_dir`, `address`, `name`, `session_name`, `preview_url`, and `files_root`. The runner uses the returned session name dynamically (currently `field-notes`). `invite` must write a new 0600 file containing a fresh single-use `trill://pair` URL. That file stays in a private temporary directory and is removed immediately after opening and confirming Pair, including on failure. The native system Open prompt is handled before Pair. Terminal consent finishes before recording starts.

Every capture closes only its owned Trill app under the simulator lock, including failure and interruption, so another device can own the private tmux viewport. The lab stays running after capture, so its actual shell/agent state persists. Stop it explicitly with the lab command when finished; the lab's normal stop preserves tmux. No runner operation calls default tmux, a personal companion, simulator erase/reset/delete, global IDB kill, or simulator shutdown. Protected simulator IDs are also rejected even if metadata is altered to claim ownership.

Both native runners read optional private protections from `.local/protected-simulators.json` in the runner's checkout. Store a JSON list of simulator UUID strings there, for example `["11111111-1111-4111-8111-111111111111"]`, replacing the example with your personal simulator IDs. This file is git-ignored; keep personal IDs out of tracked files. Malformed or unreadable configuration blocks simulator operations. A fresh checkout may omit it: ownership checks still apply, and a newly created simulator cannot reuse an identity from the existing inventory. `--udid` never adopts an existing simulator.

A nonblocking runner lock serializes the lab and ownership registry for a shared state path. Per-UDID locks in the OS temporary directory (`trill-demo-UID/UUID.lock`) also serialize simulator access across worktrees. Keep the lock files; an unlocked file is harmless, and removing a live inode breaks locking. Coordinate all users of the shared demo ports with the same lab state path.

## Accessibility fallback

The default reads the CoreSimulator AX tree with the XCTest client, which exposes native toolbar identifiers omitted by the default VoiceOver tree on these seeds. Waits poll that same tree and require stable geometry across three samples before acting. This IDB version cannot select that client for marker actions, so actions deliver an explicit 0.1-second HID touch at the matched element’s verified live AX center. Zero-duration HID taps were dropped by the tested simulator seeds. Coordinates are derived from the current accessibility element, never guessed. If the installed Duo seed cannot provide AX, use `--ui-map /absolute/reviewed-map.json`. The runner never silently guesses coordinates. A map must be reviewed against the exact native app binary hash, runtime, screenshot pixel dimensions, and IDB point dimensions. It is specific to one profile. Use `--ui-map-dir DIRECTORY` with files named `iphone-17-pro.json` and `iphone-duo.json` for a sequential all-device run, or pass a single map with `--ui-map`. Obtain dimensions from `mise exec -- idb describe --udid UUID --json` on the dedicated device, and review screenshots privately before recording. The initial maps in `demos/profiles/maps/` were reviewed on both dedicated simulators against the 115-tested app binary. They pin its executable hash and refuse unknown builds. Re-review the native layout before updating a map’s hash; policy-only rebuilds do not themselves authorize different tap geometry.

Map shape (all placeholder values must be replaced with measurements; this is not an executable map):

```json
{
  "profile": "iphone-duo",
  "runtime": "com.apple.CoreSimulator.SimRuntime.iOS-27-1",
  "app_sha256": "EXACT_EXECUTABLE_SHA256_FROM_RUNNER_REGISTRY",
  "reviewed_by": "operator",
  "reviewed_at": "ISO_TIMESTAMP",
  "poses": {
    "cover": {
      "pixels": [0, 0],
      "points": [0, 0],
      "targets": {
        "Open": {"text": "Open in", "tap": [0, 0]},
        "confirm-pairing": {"text": "Pair", "tap": [0, 0]},
        "field-notes": {"text": "field-notes", "tap": [0, 0]},
        "open-terminal": {"text": "Open terminal", "tap": [0, 0]},
        "Allow once": {"text": "Allow once", "tap": [0, 0]}
      }
    },
    "unfolded": {"pixels": [0, 0], "points": [0, 0], "targets": {}}
  }
}
```

The fallback compiles the small local `scripts/demo/screen_text.swift` helper with the selected Xcode toolchain. A private persistent helper reuses the local OCR model and is stopped with the capture. Apple Vision OCR waits for the expected visible text before each action, verifies the demo identity, and rejects pairing/consent UI or possible credentials. Screenshots used for this validation are private temporary files and are deleted. Map entries can override an identifier's visible `text`; add measured entries for workspace controls when using that scenario. Appearance changes do not authorize a changed layout: re-review coordinates if needed.

## Artifacts and validation

Artifacts default to `REPO/.local/demo-capture/artifacts/RUN_ID/PROFILE/APPEARANCE/`. Each directory contains a manifest, appearance-tagged PNGs, a real continuous MP4 (or requested pose segments), and `light-PROFILE.mp4` or `dark-PROFILE.mp4` as the website source. Manifests contain simulator ID, runtime/build, scenario/profile, appearance, source checkout commit and dirty flag, runner commit/script hash, app executable hash, capture settings, timing events, artifact hashes, and verified MP4 duration/dimensions. The source checkout commit describes the checkout at capture time; a prebuilt app's exact build commit is not inferred from that checkout. Its executable hash identifies the actual app used.

The workspace scenario waits for the actual preview heading (`Make something` in the initial reviewed maps), so a matching title in the terminal cannot satisfy browser readiness. Only successful complete manifests should be consumed by website tooling. A failed capture removes all PNG/MP4 imagery from that invocation and retains a failure manifest. Raw child stdout/stderr and UI trees are never logged or copied into artifacts. Every supplied screenshot is OCR-scanned for recognized credential patterns even when AX identifies the demo. Video screening checks sampled boundary/still frames, not every video frame; it is not a proof that arbitrary terminal output is secret-free. Pairing URLs stay outside recordings and artifacts. Visible-state gates cannot prove that arbitrary external terminal content is secret-free: the fixture/operator must keep the isolated demo session and preview public. Never run this against a personal session.

Focused checks:

```sh
python3 -B -m unittest discover -s tests/demo_runner -v
python3 -B scripts/demo.py run --profile all --appearance both --app /absolute/path/Trill.app --dry-run
```


## Utility story: real request and visible file change

The operator owns live capture and agent setup. Start the real Codex agent explicitly with the parent lab's opt-in `codex` command and complete folder trust/any approvals as the operator. The runner never starts an agent or accepts trust/approval prompts. Example after the devices are prepared with the frozen app:

```sh
python3 scripts/demo.py capture --repo "$PWD" \
  --profile all --appearance both --scenario utility --allow-terminal-input \
  --ui-map-dir demos/profiles/maps --hold 1.5
```

`--allow-terminal-input` is mandatory, including for utility dry-run validation. Without it the runner rejects before commands or state writes. Custom `text-input`, Insert, and Return steps also require the flag. Utility requires the reviewed maps: `sessions-back`, `sessions-list`, and other navigation markers are map keys, not invented app accessibility identifiers. The original maps used executable hash `17cd7d1ba636f4ff6292c6ba6664b4e05246fedeb69cbd8aaee8dc8a457def3b`. The Duo map was re-reviewed against the October 25 build below; the iPhone 17 Pro map remains tied to its older build.

Current source uses a Sessions/Agents mode switch within the Sessions sidebar. `sessions-back` and `sessions-list` remain reviewed map aliases; neither is a native app identifier. Sessions-start captures reject missing navigation entries before appearance changes, app launch, or pairing. This structural check does not validate their geometry: the operator must observe the current build's return control and Sessions layout before reviewing its map. Historical capture results describe their frozen build; inspect the map executable hash and current native UI before every new app capture.

The native Write editor still exposes `terminal-draft`, disables autocapitalization and smart substitutions, and requires explicit focus. Its available height now shares space with photo tools/status, so re-review draft and keyboard coordinates on the actual build; the runner's keyboard wait and exact value readback remain required. No source-only coordinate replacement is safe.

Preflight pairs and opens the terminal, completes consent, disables the hardware keyboard only on the owned simulator (`idb set ... hardware-keyboard disable`), and returns to Sessions **before recording**. Native app quiescence is enabled by default before taps and after pose changes: `idb ui quiet --bundle-id io.bcook.trill --quiet-window-ms 400 5`, always with the owned UDID and an outer ten-second command bound. `--no-ui-quiet` is an explicit compatibility opt-out for older IDB versions. Quiet waits settle native animations; unfolded Duo controls additionally use native accessibility presses because HID geometry remains stale after a hinge change.

The recording starts with Sessions, selects the lab's returned session name, checks the visible idle `Ask Codex` prompt, opens Write, explicitly focuses the draft, waits for the visible keyboard's `123` key, and types the lowercase public request through real IDB HID text in 24-character chunks with 0.2-second gaps. Input is restricted to `[a-z0-9 .,'-]+`, whose installed IDB mappings emit no Shift events. Before editing, host `describe-point --api ax-coresimulator --json` must identify exactly `terminal-draft` with an empty `AXValue` (including the guarded explicit-null empty text-entry case). Stale drafts are rejected. Exact identity and cumulative `AXValue` readback must match after every chunk before proceeding; readback waits are bounded to five seconds and repeat only reads. A previously held Shift can still corrupt input, so a mismatch stops before Insert with no edit retry. The runner does not use AX set-value for the terminal draft because native binding updates raced with SwiftUI rendering. It shows the draft/keyboard, taps Insert, waits for the terminal, then taps Return. `lab status.agent_running` must be boolean `true` before the scenario, typing, Insert, and Return. Trust/approval text is rejected before opening the draft and checked again before Return. These are sampled visible-state/liveness checks, not an atomic guarantee against an agent changing state between checks.

Light requests the main heading **“make room for good ideas.”**; dark requests **“find a little room to think.”** Each asks to keep the other design and run `node --check app.js`. If the current public page already has that heading, the runner chooses the appearance's declared alternate instead, records the actual request/expected heading in the manifest, and requires a different content hash. It never resets the page or fabricates agent output. After uncertain typing, Insert, or Return delivery, stop and inspect the real native draft/terminal and agent state before rerunning. Input operations are not retried and no composer/terminal content is automatically cleared.

Duo sends the request on the cover (`pose: first`), then unfolds while Codex works. Only the first initial snapshot is named `sessions`; the later pose snapshot is `terminal`. iPhone executes both first- and last-pose steps in its sole pose. The runner reads only `files_root/index.html` in the parent-authenticated owned lab tree, rejecting escaped/symlink roots, symlink/nonregular files, and files larger than 1 MiB. A changed hash and matching actual `<h1>` must remain identical across at least three reads spanning 0.75 seconds; the wait is bounded at 120 seconds and its duration/outcome is logged. This proves only a stable public file update, **not** completion of the agent task or success of `node --check`.

The utility `ensure-workspace` step reads the live XCTest AX tree for exactly one enabled `session-workspace` button. An exact “Hide files and browser” label means it is already open and receives no press. An exact “Files and browser” label permits one native AX press at a verified point, guarded by that closed label, followed by read-only confirmation of the open label. Missing, disabled, duplicate, or unfamiliar controls fail closed; an uncertain press is never retried. This avoids closing a pane already visible after a fold. Root source defaults the pane to closed, so capture must inspect live state rather than infer it from that default.

The final steps show Files, open the real `index.html` source, wait for the chosen heading in the reviewed source rectangle, capture it, then tap the native `file-render-preview` globe. This opens the actual Browser pane with an authenticated static HTML preview: HTML, CSS, and local assets load through native file reads, while JavaScript is disabled. CSS light/dark appearance works; this path does not demonstrate interactive bookmark behavior or a live development server. Browser readiness OCR stays within the reviewed page-body rectangle and waits for the dynamically chosen heading. The reviewed crops include the heading below the static-preview banner on both devices. Screenshots include Sessions, terminal, draft keyboard, submitted request, file readiness, Files, source, and preview. Utility does not type a URL. The legacy `set-preview` operation retains its accessibility-plus-Backspace transport and exact readback before Go. The completed capture validation is recorded below.

`text-input` accepts 1 to 512 credential-free characters from the no-Shift ASCII whitelist `[a-z0-9 .,'-]+`, explicitly focuses its named native field, and requires `ready_target`. It supports bounded `chunk_chars` (1 to 80) and `interval_seconds` (0 to 1). It never adds a newline or Return; submission is a distinct authorized native tap. Dynamic values are `$session_name`, `$request`, and `$heading`. `wait-heading` uses the read-only public file evidence; `wait-source` and `wait-preview` use the chosen heading and the respective reviewed map rectangles.

## Capture validation, 24 October 2026

Four utility recordings completed against the frozen native app and the real isolated Codex process. Each starts at Sessions, opens Write with the software keyboard, types a verified request, submits once, waits for an actual changed file, opens its source, and renders the authenticated static HTML preview. The model's terminal output is real. The recorded heading is not reset between takes.

| Device | Appearance | Duration | Artifact run |
| --- | --- | --- | --- |
| iPhone 17 Pro | Light | 38.167 s | `20261024T023326Z-09ef1487` |
| iPhone 17 Pro | Dark | 35.465 s | `20261024T023426Z-c203ed4f` |
| iPhone Duo | Light | 43.067 s | `20261024T024859Z-c2fffe79` |
| iPhone Duo | Dark | 42.133 s | `20261024T025013Z-34c8d821` |

Find each MP4 and full-resolution PNGs under `.local/demo-capture/artifacts/RUN/PROFILE/APPEARANCE/`. All four manifests report complete and contain checksummed H.264 MP4 metadata. Native keyboard/source/page stills and sampled video frames were visually reviewed, including the actual final rendered frame of both Duo videos. Pairing and consent remain outside the public recordings. Artifacts stay local and git-ignored.

Duo records the actual cover-to-unfolded hinge change continuously. IDB keeps the initial 1398×2034 video canvas and letterboxes the unfolded portion. The accompanying unfolded PNGs retain 2853×2007 resolution. The runner does not invent an unfolding animation or synthesize terminal content.

The installed IDB companion retains cover dimensions in `describe` after the framebuffer unfolds. HID touches based on that stale geometry can miss controls. Reviewed unfolded map entries therefore use `api: ax-coresimulator` coordinate accessibility presses; controls with stable identifiers also specify `expected_id`. The complete unfolded Files → source → globe → HTML flow passed a live probe and both final recordings. Cover controls retain explicit-duration HID taps. The cover session-row point lies inside both expanded-search and collapsed-search layouts.

Readiness accounts for observed native behavior: a cursor can occlude the first letter of the idle prompt; empty active UIKit editors report explicit null values; the native address field reports its placeholder as its empty value. Exact native field identity and cumulative typed text are checked. The iPhone map has one reviewed exact-line OCR title variant. Duo can verify the exact enabled native session heading at reviewed points after pixel screening, so terminal contents cannot obscure its identity check.

All 74 focused runner tests pass. Independent Sol reviews covered input authorization, uncertainty/no-retry behavior, file evidence, scoped source/page checks, and identity handling. Later native-press and cursor cases have targeted regressions plus live device-simulator validation. The app binary remains the previously validated build with 115 native tests (two optional TLS probes skipped) and successful unsigned iPhone compilation; no native app rebuild was needed for this runner revision.

Creation is intentionally absent from these clips. A safe real Xirp creation demo needs a dedicated macOS account because same-user Xirp isolation reaches personal IPC/tmux state; see [creation isolation findings](DEMO_CREATION.md). Personal sessions and the production companion were not changed by demo capture. The private Codex session and lab remain available.


## Duo marketing refresh, 25 October 2026

A new simulator build from native source `4f3cdd01ba2c33bb87ed220af43bd33f574e2594` has executable SHA256 `1c1bd1e23ea0ad6cb4059e28ee00d5e03c74cba20d41bfc99184c814fe2f8f4d`. It was built with the repository destination-based Xcode invocation into `.build/ios-marketing-refresh`. Both captures use the owned Duo simulator on `com.apple.CoreSimulator.SimRuntime.iOS-27-1`. Capture manifests correctly report a dirty checkout for runner/map/documentation changes; native app source was unchanged after the build.

| Appearance | Raw duration | Run | Cover cut | Open cut |
| --- | --- | --- | --- | --- |
| Light | 41.833333 s | `20261024T200953Z-a861aff1` | 1.5–20.366667 s | 21.5–41.833333 s |
| Dark | 41.666667 s | `20261024T201106Z-7b1c8eaf` | 1.5–20.3 s | 21.5–41.666667 s |

The fresh native review moved the Sessions row, draft focus and unfolded file/browser map points to their actual current controls. The Duo profile restores portrait orientation before cover capture; `ensure-workspace` inspects the pane before opening it. A screenshot smoke run preceded the two final continuous recordings. The real isolated Codex agent changed the heading to “good ideas deserve a little room.” in light mode and “find a little room to think.” in dark mode, with actual `node --check app.js` output visible. No personal sessions or production companion were operated on.

Measured unfolded crop is x=0/y=525/w=1398/h=984 in the 1398×2034 canvas. Use exact crop coordinates to preserve the odd vertical offset. Exported cover clips are 742×1080, open clips 1280×900, with first-frame lossless WebP posters and separately scaled native preview screenshots. The new `scripts/demo-export.py` validates manifests and cuts and writes a fresh staging directory, never the website directly; see the [capture skill reference](../.agents/skills/trill-marketing-upkeep/references/capture-workflow.md) for its command and cuts schema. The host uses standalone cwebp because its ffmpeg lacks libwebp.

All four new public clips fully decoded; posters matched decoded frame zero byte-for-byte and all MP4s had faststart. Review included native stills, sequence contact sheets, the hinge transition, and complete unseeked browser playback of each theme. Additional Vision OCR scanned all 2,345 exported video frames with no recognized credential/pairing pattern matches; this is supplementary screening, not a guarantee for arbitrary terminal output. The real Codex warning indicator remains visible. Raw captures and review artifacts stay private under `.local/demo-capture`.

Focused checks: 87 runner tests, 9 lab tests and 16 exporter tests passed, including real ffmpeg/cwebp export integration. The current utility map intentionally omits stale legacy workspace URL-entry controls; review those controls before running the legacy workspace scenario. Older iPhone 17 Pro captures remain private; unused public exports were removed during publication cleanup.

The final homepage passed the Chrome/WebKit browser suite, including automatic playback/fold, theme continuity, pause/replay, reduced motion, no-JavaScript/error fallback, seven responsive widths, and support/privacy routes. Desktop and mobile layouts were visually reviewed in both themes. The standalone hero screenshot was removed; video is the main product visual. Post-capture review added early rejection of stale URL-entry maps before device interaction and export artifact checksum validation; these do not change captured media or its recorded runner hash.


## Duo marketing refresh, 26 October 2026

Built native source `78c1cec36e4adeb921a1683af7977c81a46e6c34` with Xcode 27.1 (27A9275) into `.build/ios-marketing-20261026`. Native app source was clean; the checkout contained existing documentation, companion and runner changes. Executable SHA256: `1cd13f57d049bca710c592028b7b1917ff126ee8fa1b2b0291e7d25bbcb08b81`. The loaded `Trill.debug.dylib` SHA256 is `1c335364e9fc5ddf2ae9fe0decb2a81709c913f97fe49757e84699a3b06b62c7`. Captures used the runner-owned Duo on iOS 27.1; the public manifest preserves runtime build and capture provenance.

The current Sessions/Agents layout moved the session row to y=244 and Open terminal to y=365 in cover point coordinates. Connected terminals no longer show a session/Mosh navigation title. The runner now waits for the exact enabled native `remote-terminal` surface, including when using a map; pixel identity and credential screening remain required. The isolated tmux status shows the real session name with `#S` in neutral colors. Removed obsolete heading identity points, reviewed source/preview geometry, and dismissed the keyboard tutorial before capture. Fresh pairing review requires the same owned-app restart as capture to avoid retained detail navigation.

Smoke run `20261026T102406Z-f4df553f` completed the real utility flow before final recording. No request was replayed after uncertain delivery. The real agent changed the page between takes; no conversation or output was fabricated or reset.

| Appearance | Source duration | Run | Cover cut | Open cut |
| --- | --- | --- | --- | --- |
| Light | 42.266667 s | `20261026T102619Z-5f79eed2` | 3–21.7 s | 23–42.266667 s |
| Dark | 42.066667 s | `20261026T102725Z-98c745f0` | 3–21.7 s | 23–42.066667 s |

New-frame measurements confirmed the unfolded display at x=0/y=525/w=1398/h=984 in the 1398×2034 recorder canvas. The first black frames occurred at 21.798333 s (light) and 21.831667 s (dark); both exports omit the blackout and fade. Four H.264 clips, four exact decoded-first-frame WebP posters and two native-preview WebPs total 4,246,700 bytes. The final light heading is “good ideas deserve a little room.”; dark is “find a little room to think.”

Validation passed: 95 runner tests, 9 lab tests, 16 exporter tests, the skill validator, complete decode and faststart checks, exact poster pixel comparisons, and the Chromium/WebKit website suite. Both themes completed unseeked playback through the illustrative fold to the final preview. Reviewed chronological playback images and desktop/mobile light/dark site captures. Local Vision OCR checked all 2,272 exported frames with no recognized credential/pairing matches; this supplements visual review and is not a general secrecy guarantee. The real Codex 2-warning indicator remains visible.

Private review/build evidence is under `.local/demo-capture/review-20261026`; the exporter staging directory is `.local/demo-capture/export-20261026`. The canonical skill reference was updated and validated. The website assets were replaced after local review. Publication is verified separately from the capture checks.
