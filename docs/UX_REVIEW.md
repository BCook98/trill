# Native UX decisions and acceptance checks

This document records durable interaction decisions and checks against the current implementation. It is not a runtime review report or evidence that hardware validation is complete.

## Navigation and continuity

Sessions is the main destination; Projects opens from the session list. Use native split navigation, inspectors, toolbars, sheets, and alerts. Keep the terminal connection and workspace state outside transient layout containers. Opening the workspace can make room by hiding the session sidebar; closing it must preserve the user's sidebar choice.

Scope selection, drafts, and workspace state to computer and session identity. Changing computers closes old transports and previews. Cover/unfold transitions, keyboard changes, and inspector dismissal should preserve the selected session and unsent draft. Process-eviction restoration is a separate limitation.

## Actions and feedback

Archive, Stop, and Rename confirmations belong to a persistent visible presentation, including the Projects sheet. Snapshot the intended computer/session and display name, then revalidate identity, permissions, busy state, and capability at execution. Cancel or explain an invalid target; never silently operate on a replacement.

Use explicit Cancel and consequence text. Archive hides a session without stopping a running process; Stop ends the remote session; Disconnect closes Trill's transport. Forget removes local pairing data and drafts; server-side revocation is separate. Action failures must remain visible with a terminal open and must survive unrelated metadata polling.

Binary alerts avoid a disappearing menu's popover anchor. This is a Trill design choice informed by Apple's [alerts](https://developer.apple.com/design/human-interface-guidelines/alerts) and [context menus](https://developer.apple.com/design/human-interface-guidelines/context-menus) guidance. Keep visible alternatives to long-press shortcuts.

## Input and workspace

Keep Write, Insert, explicit Return, photo actions, and terminal keys reachable with the software keyboard and large text. Long drafts scroll inside the editor. Make scrolling keys discoverable and expose modifier selection to accessibility. Do not shrink touch targets to fit extra keys.

Files use native Back and a reachable Close action. Source-to-browser transitions preserve the intended file. Static preview and live localhost preview have different trust boundaries; see [SECURITY.md](../SECURITY.md). A browser failure should retain the requested address and offer an actionable retry.

Use native reserved-region geometry for Duo rather than hardcoded device dimensions. Layout changes must not replace the terminal view or send terminal input.

## Acceptance matrix

Run mutations only on disposable sessions.

| Check | Expected result |
| --- | --- |
| Sessions/Projects row and detail overflow → Archive/Stop → Cancel | One named prompt; no mutation, disconnect, or selection loss. |
| Confirm Archive, Stop, and Rename on isolated targets | Exactly one intended operation; visible success or persistent failure. |
| Prompt during polling, capability changes, or computer switching | Stable wording; target revalidated or prompt cancelled. |
| Forget active/inactive computers; discard enrollment | Correct named local target; remote revocation explained separately. |
| Files → source → static preview; Back/Close/toggle repeatedly | Correct file and folder; terminal and sidebar state preserved. |
| Open localhost page; close/reopen workspace; switch ports/computers | Intended continuity; obsolete grants released when state is closed/replaced. |
| Write a long unsent draft with keyboard and largest text | Editor scrolls; Insert/Close/photo controls remain reachable. |
| Scroll keys; toggle modifiers; hardware Escape/Return | Keys discoverable; selected states announced; no unintended submission. |
| Cover → unfolded → book/propped; camera regions | Session, draft, and terminal retained; controls avoid reserved regions. |
| VoiceOver, dictation/IME, hardware keyboard | Correct focus, draft ordering, and explicit submission behavior. |

Simulator captures establish layout evidence only. Physical posture, touch reach, camera occlusion, dictation, VoiceOver, networking, and remote effects require recorded device checks. See [VALIDATION.md](VALIDATION.md) for reproducible verification and [DEMO_CREATION.md](DEMO_CREATION.md) for capture isolation.
