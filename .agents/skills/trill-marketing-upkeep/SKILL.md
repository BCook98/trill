---
name: trill-marketing-upkeep
description: Refresh Trill marketing videos, screenshots, and website content after major app changes; repair the real-device capture workflow and keep this skill current. Use for Trill media refreshes and release reviews involving marketing assets, not unrelated website work.
---

# Trill marketing upkeep

Keep the marketing site faithful to the current native app. A requested media refresh means actual new recordings and images, not revised dates or a source review alone. This skill runs when invoked or selected for relevant work; it does not schedule background monitoring.

Read [references/capture-workflow.md](references/capture-workflow.md) for the current runner, artifact map, commands, and known capture pitfalls. Resolve repository paths against the Trill checkout rather than assuming the process runs there. The canonical skill lives in `.agents/skills/trill-marketing-upkeep`; the installed skill may be a symlink to it.

## Establish what needs refreshing

Inspect the working tree and intended app revision, `docs/DEMOS.md`, `docs/DEMO_CREATION.md`, `docs/WEBSITE-DESIGN.md`, `website/README.md`, and the public media manifest. Trace actual image/video consumers in HTML, CSS, and JavaScript. Compare current navigation, input, panes, connection states, and capabilities with the published media and copy.

For a major UI refresh, rerecord the affected Duo sequence in **both light and dark appearance** and regenerate its posters and preview screenshots. The public site presents Duo only, automatically matching browser appearance. Include iPhone 17 Pro when requested or when it has active consumers; do not replace unrelated brand artwork. If the user requests a narrower copy-only change, honor that scope.

Build the intended native app into a dedicated build directory. Record the executable hash, checkout revision and dirty state; a checkout commit alone does not establish the provenance of a prebuilt app. Use the user's selected Xcode and mise-managed idb without changing global tool configuration.

## Record real, isolated work

Use the existing runner and owned demo lab. Demonstrate an actual terminal/tmux session and actual agent work. Never manufacture Claude/Codex transcripts, successful creation screens, backend metadata, or terminal pixels.

Protect personal work: interact only with verified runner-owned simulators, the isolated lab's tmux socket and disposable project. Never attach to, type into, steal, archive, stop, or kill personal sessions; never restart the user's companion or globally kill idb/tmux to fix a recording. Serialize captures and respect the runner lock and ownership records. Private invites, credentials, logs, and raw captures stay out of public assets.

Start in Sessions, open the real demo session, show the keyboard, type a useful request, explicitly insert and submit it, then show the real changed file and preview. Resolve agent consent before recording. Read `docs/DEMO_CREATION.md` before including session creation: the terminal lab cannot create sessions, and a separate database or environment override is not proof of isolation from personal Xirp.

When the app or runner drifts, repair the existing scenario, navigation, accessibility mapping, or helper before final capture. Inspect the new native UI and exact build/runtime geometry; **never merely replace a map's app hash to bypass validation**. Run relevant runner tests and a smoke capture, then rerecord the affected final sequence. After an uncertain submit, inspect the real lab state; do not blindly replay input or pretend a timeout succeeded. Stop dependent actions if ownership or isolation cannot be established, and report the specific blocker while completing independent work.

## Update the public assets and content

Use only complete, inspected captures. Derive cuts and crop coordinates from the new recording's presentation timestamps and actual display geometry; old manifest values are not reusable assumptions. Export silent compatible video and regenerate first-frame posters plus the current Sessions posters and unfolded-preview images. Keep old public assets intact until replacement footage passes review.

Update `website/assets/demos/manifest.json`, all affected consumers, alt text, and the accessible workflow description together. Preserve traceable run IDs, build/device/runtime provenance, source paths, trim/crop details, dimensions, duration and byte counts. Watch each final clip end to end and inspect posters at their displayed size; sampled frames alone cannot establish a coherent or private recording. No credentials, personal projects, pairing details, or accidental dialogs may enter published media.

The homepage has no standalone hero screenshot; keep video as its main product visual. Still images belong to player posters, reduced-motion and error/no-script fallbacks. The homepage `#demo` is the primary player; `/demo` is a compatibility redirect. Preserve automatic browser theme matching, Duo unfolding, subtle pause/replay, reduced-motion stills, offscreen pausing, and no-JavaScript/error fallbacks. The fold frame is illustrative presentation around real footage, not captured device motion.

Update feature claims and setup/support copy when behavior changes. Position Trill as remote control for Xirp and Herdr on the user's own computer. Keep privacy/support routes and App Store review information intact; keep `website/privacy/index.html` consistent with `ios/Trill/PrivacyPolicy.txt`. Do not invent App Store availability, Apple approval, provider features, or privacy guarantees.

## Verify, maintain this skill, and deliver

Run focused checks for modified capture code, then the website browser suite against a byte-range-capable local server. Inspect desktop/mobile screenshots in both appearances and verify the real videos, fold transition, theme continuity, reduced motion, fallbacks, and links. Failed or partial recordings do not qualify as refreshed assets.

**Every refresh must also check this skill for drift.** If actual commands, scenario steps, map validation, ownership checks, asset consumers, export geometry, or verification procedures differ from these instructions, fix the runner/docs and update this canonical skill or its reference in the same change. Document what worked, not a workaround that bypassed a guard. Keep one canonical copy; do not let an installed duplicate diverge. Validate the skill after edits using the available skill-creator validator. Do not weaken session protection or expand publication authority as part of self-maintenance.

Commit and publish according to the user's existing authorization. A push to Trill's GitHub `main` deploys the site through Vercel; do not treat publishing as a local-only operation. If publication is not authorized, finish the reviewable local work first. Report refreshed assets, real capture/build provenance, validation, runner/skill repairs, any remaining stale media or blockers, and whether publication actually completed. If you only created or edited this skill, say so; do not report a media refresh.
