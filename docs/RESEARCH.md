# Research and design decisions

Trill is independent of Spotify. This document records implementation choices and their provenance; current behavior is defined in [Architecture](ARCHITECTURE.md), [Providers](PROVIDERS.md), and [Security](../SECURITY.md).

## Provider boundary

Keep the desktop provider's project/session model: titles, agent labels, branch context, capabilities, and explicit actions. Use the installed supported CLI as the compatibility boundary. Resolve authoritative terminal targets from current metadata rather than manufacturing tmux names from IDs. Do not parse transcripts or infer agent progress from terminal output.

Herdr supplies authoritative spaces, terminal identities, worktree provenance and native mutations. The [capability matrix](PROVIDERS.md) defines the supported actions. Mutation requests are never automatically retried after an uncertain outcome.

## Terminal and transport provenance

[SwiftTerm](https://github.com/migueldeicaza/SwiftTerm) supplies native rendering and UIKit input. [Local patches](../vendor/SwiftTerm/TRILL-PATCHES.md) route intentional link taps and cell-aware scroll gestures. Herdr’s structured controller preserves terminal bytes, including labeled OSC 8 links, through an authenticated HTTPS WebSocket. No output opens a link automatically.

The disposable loopback prototype measured roughly 19–21 ms median shell round trips with the structured stream. This is local evidence, not a physical-device or roaming guarantee. The [probe](../prototypes/herdr/README.md) verifies stream behavior without accessing personal terminals.

## Native interaction decisions

Use native split navigation, toolbars, inspectors, sheets, and alerts. Sessions is the main destination, with Projects opened from its list; saved computers have one active connection context at a time. Keep terminal and workspace state independent of transient layout changes.

Use Dynamic Type, semantic colors, text plus symbols for status, accessible modifier states, and reachable terminal keys. Terminal font size is independent of application text because its fixed cell grid has different constraints. Duo layout uses local geometry and active reserved regions rather than fixed device dimensions; see [DUO.md](DUO.md).

The [UX acceptance matrix](UX_REVIEW.md) records the consequences of these choices. Apple's [accessibility guidance](https://developer.apple.com/design/human-interface-guidelines/accessibility), [alerts](https://developer.apple.com/design/human-interface-guidelines/alerts), and [context menus](https://developer.apple.com/design/human-interface-guidelines/context-menus) are upstream design references, not certification of Trill.

## Evidence and remaining work

Use [VALIDATION.md](VALIDATION.md) for repeatable checks and device release gates. Revocable per-device pairing, saved computers, and managed certificate renewal are implemented; process-eviction terminal restoration remains limited. Test provider-version compatibility, energy use, physical networking, accessibility, and third-party transport security before making release claims.

Website decisions and media provenance are in [WEBSITE-DESIGN.md](WEBSITE-DESIGN.md). Scan findings and publication review results belong in [PUBLICATION_AUDIT.md](PUBLICATION_AUDIT.md), with their actual scope and limitations.
