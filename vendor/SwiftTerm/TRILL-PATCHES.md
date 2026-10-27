# Trill SwiftTerm patch

Upstream: https://github.com/migueldeicaza/SwiftTerm
Revision: c3436be32e3dc26d5e2181b1e451d91e21e50d53 (MIT; see LICENSE).

Tracked upstream source at that revision (no Git submodule contents). The touch activation change is in `Sources/SwiftTerm/iOS/iOSTerminalView.swift`: opt-in `activatesLinksOnTap` resolves explicit OSC 8 and implicit links using the terminal's locked, scroll/wrap/bidi-aware cell lookup before forwarding a single tap or a public `forwardTap` call. It does not require pointer hover. Existing double/triple tap selection recognizers retain precedence; other taps and mouse reporting retain upstream behavior. The default is false. Trill enables it and validates/routes only HTTP(S) links in its application delegate. Terminal output never automatically opens URLs.

Remove this patch when upstream provides equivalent touch activation. Upstream tests are retained; Trill tests cover routing and activation through the real UIKit gesture selector.

## Shared Apple view warning

`Sources/SwiftTerm/Apple/AppleTerminalView.swift` checks an explicitly optional cell-dimension snapshot before remeasuring the grid. This preserves the macOS initialization guard and avoids comparing the nonoptional iOS property directly with `nil`, consistent with the other shared view helpers.

## Repository hygiene

Removed generated Python bytecode under `Tools/BidiHarness/Scripts/__pycache__` from the vendored snapshot. Source scripts and upstream license notices are retained.

## Hardware key repeat

`Sources/SwiftTerm/iOS/iOSTerminalView.swift` schedules key-repeat timers in the common run-loop modes so held keys continue repeating during UI tracking. Trill also overrides `hasText` in its terminal subclass because the remote shell can have editable text when UIKit’s input shadow is empty; this keeps soft-keyboard Backspace enabled for repeated deletion.

## Remote history scrolling

The optional iOS `remoteScrollHandler` delegates bounded whole-line finger/pointer scrolling, including the hit cell, to a host-rendered terminal surface. Trill enables it only for the structured Herdr controller, which owns scrollback. Nil retains the upstream mouse/alternate-scroll/local-history routing. Connection teardown clears the callback. Double/triple tap text selection retains its existing precedence.
