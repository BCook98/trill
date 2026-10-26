# Website design

The website presents Trill as remote control for Xirp and Herdr on the user's own Mac. Product behavior and limitations come from [Architecture](ARCHITECTURE.md) and [Providers](PROVIDERS.md); public copy must not imply a hosted agent, reconstructed chat, or App Store availability without release evidence.

## Current composition

- Explain the product and requirements before asking visitors to play a recording. Early-access and contact actions use `trill@bcook.io`.
- Embed the automatic Duo recording in the homepage workspace section at `/#demo`. Legacy `/demo` links redirect there.
- Use system typography, semantic light/dark colors, restrained framing, and actual app imagery. Keep navigation and support links visible on narrow screens.
- Match footage to system appearance while preserving playback position. Pause offscreen and when hidden; honor manual pause. Reduced motion starts with a still and explicit Play.
- Keep pause/play keyboard accessible, show useful autoplay/transfer errors, and retain a real screenshot fallback plus direct recordings without JavaScript.
- Device geometry and hinge animation illustrate a transition between real captures. They are not evidence of physical-device continuity.

The static implementation and preview commands are in [website/README.md](../website/README.md). Shared document styles cover the support, privacy, and error pages as well as the homepage.

## Design provenance

[Vercel's Web Interface Guidelines](https://github.com/vercel-labs/web-interface-guidelines/blob/main/command.md) inform semantic controls, visible focus, meaningful media descriptions, and reduced-motion behavior. These are design references, not installed dependencies.

## Media and claims

Screen pixels must come from the native app in an authorized disposable environment. Trim, crop, and scale actual captures; do not invent terminal output, agent status, or successful operations. Keep source revision, executable hash, runtime, capture scenario, and export geometry in [the media manifest](../website/assets/demos/manifest.json). Use [DEMO_CREATION.md](DEMO_CREATION.md) for account isolation and [DEMOS.md](DEMOS.md) for capture/export procedures.

Provider capabilities differ: Xirp supports coding-session creation; Herdr supplies direct terminals. File and browser previews require companion control. Saved computers allow one active computer at a time. Describe these boundaries without implying universal provider actions or unlimited file access.

## Verification

Use the isolated browser suite described in [website/README.md](../website/README.md). Check narrow and desktop layouts, both appearances, keyboard focus, no-JavaScript fallback, reduced motion, offscreen playback, legacy redirects, byte-range transfers, and failed-media retry. Review the rendered footage and expanded support/privacy pages as well as automated results.

Physical Safari and VoiceOver checks remain separate from headless browser checks. Publication evidence and its scope belong in [PUBLICATION_AUDIT.md](PUBLICATION_AUDIT.md); this document records design decisions, not audit completion.
