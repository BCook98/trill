# Research and design decisions

Trill is independent of Spotify. This document records implementation choices and their provenance; current behavior is defined in [Architecture](ARCHITECTURE.md), [Providers](PROVIDERS.md), and [Security](../SECURITY.md).

## Provider boundary

Keep the desktop provider's project/session model: titles, agent labels, branch context, capabilities, and explicit actions. Use the installed supported CLI as the compatibility boundary. Resolve authoritative terminal targets from current metadata rather than manufacturing tmux names from IDs. Do not parse transcripts or infer agent progress from terminal output.

[Xirp](https://xirp.spotify.com/) supplies coding-session and tmux operations. Herdr supplies independent direct terminals. Provider actions differ; the [capability matrix](PROVIDERS.md) is the canonical description. Creation uses a correlation identifier and metadata verification; an uncertain outcome requires inspection rather than automatic mutation retry.

## Terminal and transport provenance

[SwiftTerm](https://github.com/migueldeicaza/SwiftTerm) supplies the native VT/xterm emulator and UIKit terminal view. The vendored revision and opt-in touch-link change are recorded in [TRILL-PATCHES.md](../vendor/SwiftTerm/TRILL-PATCHES.md). Trill validates and routes deliberately tapped HTTP(S) links; terminal output does not automatically open pages.

[swift-mosh](https://github.com/wiedymi/swift-mosh) supplies UDP terminal transport. Its pinned revision and local timing, peer-liveness, and startup-cancellation changes are recorded in [TRILL-PATCHES.md](../vendor/swift-mosh/TRILL-PATCHES.md). [Third-party notices](THIRD_PARTY.md) retain upstream attribution.

[Mosh](https://mosh.org/) synchronizes terminal state over encrypted UDP. Trill uses authenticated HTTPS for bootstrap instead of placing SSH credentials on the phone; terminal traffic then travels directly to mosh-server. Xirp uses tmux for retained remote sessions; Herdr uses its direct-terminal client. Mosh is not unlimited remote scrollback. Physical roaming, loss, suspension, and sustained-output behavior require separate validation.

## Native interaction decisions

Use native split navigation, toolbars, inspectors, sheets, and alerts. Sessions is the main destination, with Projects opened from its list; saved computers have one active connection context at a time. Keep terminal and workspace state independent of transient layout changes.

Use Dynamic Type, semantic colors, text plus symbols for status, accessible modifier states, and reachable terminal keys. Terminal font size is independent of application text because its fixed cell grid has different constraints. Duo layout uses local geometry and active reserved regions rather than fixed device dimensions; see [DUO.md](DUO.md).

The [UX acceptance matrix](UX_REVIEW.md) records the consequences of these choices. Apple's [accessibility guidance](https://developer.apple.com/design/human-interface-guidelines/accessibility), [alerts](https://developer.apple.com/design/human-interface-guidelines/alerts), and [context menus](https://developer.apple.com/design/human-interface-guidelines/context-menus) are upstream design references, not certification of Trill.

## Evidence and remaining work

Use [VALIDATION.md](VALIDATION.md) for repeatable checks and device release gates. Revocable per-device pairing, saved computers, and managed certificate renewal are implemented; process-eviction terminal restoration remains limited. Test provider-version compatibility, energy use, physical networking, accessibility, and third-party transport security before making release claims.

Website decisions and media provenance are in [WEBSITE-DESIGN.md](WEBSITE-DESIGN.md). Scan findings and publication review results belong in [PUBLICATION_AUDIT.md](PUBLICATION_AUDIT.md), with their actual scope and limitations.
