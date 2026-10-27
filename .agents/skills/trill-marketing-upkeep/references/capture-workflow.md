# Capture workflow and asset map

These are entry points, not frozen assumptions. Read the current files and CLI help before using them. Repair this reference when a refresh establishes a changed workflow.

## Sources and ownership

- `scripts/demo.py`: prepare/capture/run, simulator ownership, UI map validation, manifests and recording.
- `scripts/demo-lab.py`: dedicated companion, disposable fixture and isolated Herdr server/configuration root.
- `scripts/demo/screen_text.swift`: native screen-text helper.
- `demos/profiles/{iphone-17-pro,iphone-duo}.json`: device/runtime profiles.
- `demos/profiles/maps/{iphone-17-pro,iphone-duo}.json`: exact-build UI mappings.
- `demos/scenarios/utility.json`: real request → file change → source → preview story. `terminal` and `workspace` are narrower scenarios.
- `demos/fixtures/field-notes`: public disposable site used for real agent edits.
- `docs/DEMOS.md`: build, setup, lab, capture and export instructions.
- `docs/DEMO_CREATION.md`: constraints on demonstrating real new-session creation.

Default private state is `.local/demo-capture/lab`; runner ownership/lock state is in the sibling `lab-runner` directory. Raw run artifacts are under `.local/demo-capture/artifacts`. Use the registry, not remembered UDIDs, ports, PIDs or session names. Never publish an invite or copy personal provider state into the fixture.

## Commands

From the Trill repository, inspect the current interfaces:

```sh
python3 -B scripts/demo.py --help
python3 -B scripts/demo.py list
python3 -B scripts/demo-lab.py --help
mise exec -- idb --help
```

Build the simulator app with the repository build options, into a dedicated directory; set `TRILL_DEMO_APP` to that exact newly built `.app` path. For example:

```sh
# Set DEVELOPER_DIR to your Xcode 27.1+ installation if xcode-select points elsewhere.
xcodebuild \
  -project ios/Trill.xcodeproj -scheme Trill \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath .build/ios-marketing-refresh \
  -clonedSourcePackagesDirPath .build/packages -skipPackagePluginValidation build
```

Do not add an explicit `-sdk iphonesimulator` override: in the observed Xcode toolchain it made the SwiftTerm host build generator resolve to a nonexistent product. The repository destination-based invocation succeeded. The runner does not build it. Use the selected Xcode with a scoped `DEVELOPER_DIR`, never global `xcode-select` changes.

For the homepage story, first verify the owned lab contains an idle real agent and inspect/refresh its mappings:

```sh
python3 -B scripts/demo.py run --profile iphone-duo --appearance both \
  --scenario utility --app "$TRILL_DEMO_APP" \
  --ui-map-dir demos/profiles/maps --allow-terminal-input --dry-run
```

Remove `--dry-run` only for the actual isolated capture after those checks. `--allow-terminal-input` permits the utility scenario's real lab input; it is not authorization to operate another session. Do not call a dry-run a recording. Use `--profile all` only when both supported devices are in scope. Use `--state-dir` and `--output` when an alternate owned lab or artifact directory is needed.

The existing lab commands include `start`, `invite`, and `stop`; inspect help for the real-agent setup subcommand and current flags. Do not stop a shared or personal service. Permission/setup steps belong outside the final recording.

Focused checks when changing capture logic:

```sh
python3 -B -m unittest discover -s tests/demo_runner -v
python3 -B -m unittest discover -s tests/demo_lab -v
```

## UI drift and mutation recovery

The October 26 published media is historical Codex/tmux footage, with its original provenance retained. The current app uses Herdr HTTPS streams and opens a selected live session directly after consent. Wait for the exact enabled `remote-terminal` identifier; do not tap the removed `open-terminal` screen. Keep pixel identity and credential checks. The migrated lab uses its returned Herdr pane/terminal IDs and its own configuration root; never substitute a personal server or fabricated terminal label. Current layouts need a fresh native review before repinning an old capture map’s executable hash. Dismiss first-use keyboard tutorials before recording.

A polish-release review does not itself claim new recordings. Keep published captures and their dates intact when the demonstrated journey is unchanged; record any stale or unqualified media in the release handoff. A requested or major UI refresh still requires complete new light/dark footage and validation before replacement. New terminal/space creation now uses a Mac folder browser; terminal menus include confirmed closure, and panel visibility is sticky. Review those controls explicitly if a capture scenario uses them.

The Duo profile explicitly sets `orientation: PORTRAIT`; hinge angle alone does not clear a retained landscape orientation. Inspect every changed step on each new build before repinning maps. Fresh pairing review also needs the same owned-app terminate/launch as capture: retained detail navigation can otherwise survive pairing.

Duo cover/unfolded geometry can differ between idb's description and the native accessibility coordinate system. Existing maps use an explicit `ax-coresimulator` fallback with expected identifiers, runtime and executable hash. Reinspect UI, point/pixel dimensions and posture instead of substituting unchecked taps. The utility scenario uses `ensure-workspace`: it reads the exact enabled native workspace control and opens it only if closed, because unfolding may reveal an already-open Files pane. An unknown state fails rather than toggles blindly. Keep one runner rather than creating a second ad hoc capture pipeline.

The current utility path separates typing, Insert, and Return. Its text whitelist, length/chunk limits, agent-idle gate and changed-heading check are deliberate. `wait-heading` observes a real file hash and stable heading; it does not prove every agent task is finished. If submission status is uncertain, inspect the owned terminal and file state before retrying. Do not reset a real agent conversation to manufacture successful history.

Native HTML-file preview disables scripts. Use an actual running web-server preview when demonstrating JavaScript interactions, and verify the server responds before recording. A still HTML render is not evidence that a generated app works.

## Public output

`website/index.html` owns the embedded `#demo` player. There is no standalone hero screenshot. `website/site.js` reads `website/assets/demos/manifest.json`; `website/demo.css` owns frame geometry. `/demo` redirects to `/#demo`.

Active assets under `website/assets/demos`:

- `duo-{light,dark}-cover.mp4` and matching `.webp`: folded recording and first-frame poster.
- `duo-{light,dark}-open.mp4` and matching `.webp`: unfolded recording and first-frame poster.
- `duo-{light,dark}-preview.webp`: current unfolded preview still for reduced motion and fallback imagery.
- `manifest.json`: consumers, source runs, trims/crops, dimensions, durations, sizes and encoding/provenance.

Unused older `iphone-*` exports were removed during publication cleanup. Inventory consumers before adding or refreshing them. Brand SVGs and app icons are not capture screenshots.

Export silent H.264/yuv420p MP4 with faststart and compatible dimensions. Use ffprobe to verify actual metadata. Derive the trim/crop from the **new** file's MP4 presentation timeline; do not reuse old recorder portrait-canvas offsets or assume capture wall time equals video time. Exclude the recorded hinge blackout; the website bridges cover/open footage with its illustrative CSS fold. Extract posters from decoded final exports, and scale current native preview screenshots without editing terminal pixels. Synchronize manifest durations, source provenance and file sizes after encoding.

Use the repository exporter to create a new private staging directory:

```sh
python3 -B scripts/demo-export.py \
  --light-manifest .local/demo-capture/artifacts/LIGHT_RUN/iphone-duo/light/manifest.json \
  --dark-manifest .local/demo-capture/artifacts/DARK_RUN/iphone-duo/dark/manifest.json \
  --cuts .local/demo-capture/review/cuts.json \
  --output .local/demo-capture/export-NEW_RUN
python3 -B -m unittest discover -s tests/demo_export -v
```

The cuts JSON has `light` and `dark` objects, each with `coverIn`, `coverOut`, `openIn`, `openOut` in MP4 seconds and `crop: {x, y, width, height}` for the unfolded display. Inspect `--help` and derive these values from the new footage. In the October 25 capture, the display was x=0/y=525/w=1398/h=984: `crop=...:exact=1` preserves its odd y offset. Dark-mode crop detection can incorrectly exclude the black OS rail, so compare actual display geometry and light footage rather than trusting its detected width.

The exporter requires ffmpeg/ffprobe with libx264, and either ffmpeg libwebp or standalone `cwebp` (the current host uses cwebp). It validates complete manifests, contained artifacts and their SHA256 checksums, provenance fields, and actual cuts/geometry, then creates silent clips, decoded-first-frame posters, native preview stills, and `export.json`. It refuses an existing output directory. Its success does **not** approve visual quality or publish files. Inspect output, fully decode videos, compare posters to frame zero, verify faststart and actual metadata, and review entire playback before promoting assets and updating the public manifest. Preserve build/runtime/run provenance separately from the export revision. The current Duo map is reviewed for `utility`; legacy URL-entry targets need a new native review before using `workspace`. A mapped URL-entry scenario missing its reviewed controls is rejected before device interaction.

## Website verification and publication

Use a local server that supports byte ranges; Python's simple HTTP server is unsuitable for seeking checks:

```sh
npm --prefix website ci
export PLAYWRIGHT_BROWSERS_PATH="$PWD/.local/playwright-browsers"
npm --prefix website run browsers:install
npm --prefix website run preview
```

Run `npm --prefix website test` from another repository-root terminal with the same `PLAYWRIGHT_BROWSERS_PATH`. The locked npm scripts supply local Playwright and matched Chromium/WebKit builds; do not rely on an external `NODE_PATH` or unversioned npx downloads. `TRILL_DEMO_URL` selects the preview origin; `TRILL_DEMO_ARTIFACTS` selects the screenshot output directory. The suite checks homepage embedding, legacy redirect, actual media playback/fold, theme continuity, pause/replay, reduced motion, blocked autoplay, failed-media retry, no-script fallback, responsive layouts and support/privacy pages. Inspect the resulting images as well as test output.

Vercel project `bcook/trill` serves `website/` from the public GitHub repository `BCook98/trill`; pushes to its `main` publish at `https://trill.bcook.io`. Verify the remote and branch ancestry before pushing. The older `BCook98/trill-priv` repository has private history: transfer approved file changes into a clean public checkout, never merge or push that ancestry into the public repository. Keep only public artifacts under `website`. Check the deployed homepage/redirect when publishing is authorized, and report deployment status accurately.
