# Trill website

The static website has no runtime dependencies. Local preview and browser checks use the pinned development dependencies below. Set the Vercel root directory to `website`; no install or build command is required.

- Home: https://trill.bcook.io
- Demo: https://trill.bcook.io/#demo
- Privacy policy: https://trill.bcook.io/privacy
- Support: https://trill.bcook.io/support
- Contact: trill@bcook.io

The homepage uses the automatic Duo demo as its product visual, embedded in the workspace section. There is no separate hero screenshot. Video playback starts when the stage enters view; it pauses offscreen. The site presents Trill as remote control for Xirp and Herdr on the user’s own Mac. It introduces both providers, tmux and direct terminals over mosh, file/page/video previews, native writing and photos, and saved computers. Early-access links open email; no App Store availability, pricing, or signup is claimed. There is no account, form service, analytics, advertising, or browser storage. System fonts and assets are local.

The Duo recording lives on the homepage at `/#demo`. Old `/demo` links redirect to that section. It starts automatically, unfolds over 1.4 seconds, then continues the unfolded recording and holds its last frame. There are no device/theme selectors, timeline, hinge slider, or chapter controls. The site, screenshots, and footage follow the browser’s light/dark appearance; changing appearance preserves the current part of the workflow. The frame is a thin neutral bezel.

Click or tap the stage to pause/resume; the same action is keyboard accessible and its label appears on hover/focus. Browser autoplay blocking exposes a Play prompt. Reduced motion starts with a still preview and explicit Play, and skips animated folding. Offscreen/hidden playback pauses and resumes on return unless manually paused. No-JavaScript visitors get direct recording links.

Current design decisions, upstream provenance, and claim boundaries are in [`docs/WEBSITE-DESIGN.md`](../docs/WEBSITE-DESIGN.md). Shared `styles.css` owns `.wrap`, `.nav`, `.nav-links`, `.brand`, `.footer`, `.footer-links`, `.copyright`, `.document`, `.contact-panel`, `.section-index`, and `.button.primary`; retained pages need no new markup. Theme colors follow browser appearance (`#f8fafc` / `#101216`).

Keep `privacy/index.html` consistent with the offline policy in `ios/Trill/PrivacyPolicy.txt`. Hosting request processing is disclosed generically so the policy remains accurate across hosting providers.

Preview from the repository root: `npm --prefix website ci`, then `npm --prefix website run preview` (directory URLs also work with a trailing slash). This uses the locked local http-server executable and binds only to 127.0.0.1:8780. Use a server with HTTP byte-range support for video seeking; Python’s simple HTTP server does not provide it. Vercel provides byte ranges, extension-free routes, and the custom 404.

## Deployment

Vercel project: `bcook/trill`. Live fallback: https://trill-xi.vercel.app

`trill.bcook.io` is live on this project. The GitHub integration publishes pushes to `main`. Keep the Vercel Root Directory set to `website` so public output contains only website files. `.vercelignore` excludes environment files. Local `.vercel/` identity is ignored by Git; use `vercel link --project trill --scope bcook` on another checkout for deployment diagnostics.


## Recorded demo wrapper

The screen content is captured from the native app connected to a real, isolated Codex/tmux session. `site.js` only controls recordings and frame presentation; it never fabricates terminal output. The previous illustrative Shell/Agent transcript has been removed.

`assets/demos/manifest.json` records source runs, trim points, crop geometry, codec, dimensions, durations and provenance. The four silent H.264 MP4 files use faststart; WebP posters match their decoded first frames. The Duo media totals about 3.7 MB across four videos and six images. Video loading begins when the demo is visible and starts playback, and all media is served from this site. There are no third-party player libraries or model assets.

Duo exports omit the recorded hinge blackout and crop the unfolded display out of the recorder's portrait canvas. The CSS hinge bridges these two real clips using the unfolded clip's first frame. The final frame stays visible; the demo does not loop automatically. A text description covers the silent workflow; the no-JavaScript fallback links directly to the recordings.

Prior art reviewed: [Duo Fold Studio](https://duo-fold-studio.vercel.app/) for interactive hinge exploration; [CSS 3D Lab's foldable phone](https://css3dlab.edgarasneverdauskas.com/models/foldphone/) for lightweight two-half device geometry; [Ecom Image Kit](https://www.ecomimagekit.com/tools/iphone-duo-animation) for separating screen media from device motion. Trill's frame and controller are original code; no third-party models, wallpaper, screenshots, or source snippets are included.

## Pinned development tools

Use Node.js 22+ (CI uses Node.js 24 LTS) and npm with lockfile v3 support. `package.json` pins **Playwright 1.64.1** and **http-server 14.1.1** exactly; `package-lock.json` fixes their transitive dependencies and integrity hashes. These are development tools; deployed HTML/CSS/JS and media need no package install or build. Vercel's static upload excludes the dependency manifests, local modules, and test output.

Versions were verified against the [Playwright npm package](https://www.npmjs.com/package/playwright) and [http-server npm package](https://www.npmjs.com/package/http-server) on 25 October 2026. Playwright downloads browser builds matched to its version. Its [release notes](https://playwright.dev/docs/release-notes#version-157) describe the switch to Chrome for Testing builds; the test uses the downloaded `chromium` channel rather than a personal system Chrome. See the [browser installation guide](https://playwright.dev/docs/browsers) for OS support and codec limitations. macOS is used for CI browser checks because the suite exercises H.264 playback in WebKit.

Run from the repository root:

```sh
npm --prefix website ci
npm --prefix website run check
# Browser binaries stay in this checkout, not a shared/global browser cache.
export PLAYWRIGHT_BROWSERS_PATH="$PWD/.local/playwright-browsers"
npm --prefix website run browsers:install
npm --prefix website run preview
```

In another terminal at the same repository root:

```sh
export PLAYWRIGHT_BROWSERS_PATH="$PWD/.local/playwright-browsers"
TRILL_DEMO_ARTIFACTS="$PWD/.research/website-demo" npm --prefix website test
```

Stop the preview with Ctrl-C in its original terminal. `npm test` expects that preview server to be running; it does not start it. These scripts use POSIX shell syntax (macOS/Linux). On Linux, Playwright's documented OS libraries must also be available for WebKit and video decoding; no global Chrome installation is needed. The browser install script downloads only the full Chromium/Chrome for Testing build and WebKit, without the unused headless shell. Both browser tests create disposable profiles and never attach to a user's browser session.

Do not use unversioned `npx --yes` downloads or an external `NODE_PATH`. The npm test script supplies the repository-local module path for `tests/website_demo.cjs`, which lives outside `website/`. To update tooling deliberately, check stable versions on npm, change exact versions in `package.json`, regenerate the lock with `npm --prefix website install --package-lock-only --ignore-scripts`, then rerun `npm ci`, browser installation, and the suite. Review the lockfile diff together with the version change.

`TRILL_DEMO_URL` selects another preview URL; `TRILL_DEMO_ARTIFACTS` selects the screenshots directory (default `.research/website-demo`). The suite covers homepage CTA and no-JavaScript behavior, in-page demo links and offscreen initial playback, legacy demo redirects, byte-range serving, document overflow with FAQs expanded, Duo autoplay, pause, automatic fold, replay, system theme continuity, reduced motion, seven responsive widths, offscreen pause/resume, browser autoplay blocking, and failed-media retry. It uses isolated browser profiles and never opens a terminal session.

## Homepage image provenance

The homepage uses player posters and a no-script/unavailable-player unfolded preview, with light/dark variants selected by `<picture>`. It has no standalone product screenshot. The embedded player also uses real frame posters. Capture provenance is in `assets/demos/manifest.json`. Exports only trim, crop and scale actual recorded pixels.

## Keeping marketing current

Use [$trill-marketing-upkeep](../.agents/skills/trill-marketing-upkeep/SKILL.md) after major app changes. It covers rerecording both Duo appearances, regenerating posters and screenshots, updating website claims, and repairing capture scripts and the skill when the native UI changes.

The October 25 refresh rebuilt the native app from `4f3cdd0` and recorded new Duo utility runs `20261024T200953Z-a861aff1` (light) and `20261024T201106Z-7b1c8eaf` (dark). Both show Sessions → native keyboard/request → real Codex edit → file source → preview. Public provenance includes the app executable hash, source/build revision, runtime, runner/map hashes and measured cuts. The exporter is `scripts/demo-export.py`; capture and review details are in `docs/DEMOS.md`.
