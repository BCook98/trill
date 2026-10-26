# Duo adaptation, 24 October 2026

## Native navigation

Build with the 27.1 SDK. The earlier 27.0-linked app could resize but could not use the strip alongside the status area. The current build lets NavigationSplitView, NavigationStack, and TabView share the system side rail. It returns to horizontal bars in poses where iOS chooses that layout; Trill does not hardcode a right-side stack. [Apple: prepare for Duo](https://developer.apple.com/iphone-duo/prepare/).

The terminal uses one system ToolbarOverflowMenu. Keyboard has high visibility priority, and the terminal screen prefers toolbar actions over tabs when space is limited. Sheets supply symbols with accessible titles for Cancel, Start, and Done. Terminal escape/control/arrow keys remain in a horizontal keyboard accessory area. The session filter stays with the list. [Apple: Raise the bar](https://developer.apple.com/videos/play/tech-talks/111462/).

## Fold and camera geometry

The sidebar specifies an ideal width without a hard maximum. Native containers manage navigation and presentation. The terminal also needs an uninterrupted rectangle for its fixed-cell grid: TerminalViewportRegion reads active division and occlusion regions, using the reported frames (which already include margins) in local view coordinates. It chooses the largest clear rectangle; equal areas prefer the upper region for tabletop reading, then the right region for book-to-cover continuity. Inactive folds consume no space. The terminal and key strip share one layout. In a horizontal fold, output stays above the division while Write, workspace, session actions, and terminal keys occupy the lower display. Keyboard frame notifications are converted through the current window; when Duo reserves the entire lower display, the compact control row sits just below the fold. Its 50-point height and 44-point targets remain stable, with large content viewers for enlarged labels. Input notices open a scrollable sheet rather than resizing the row. Neither changes the underlying TerminalConnection or terminal UIView. [Apple: adaptive layouts](https://developer.apple.com/videos/play/tech-talks/111463/).

No orientation, screen-size, device-model, hinge-angle, or idiom branching is used. The app is still a session list plus a terminal, so it keeps NavigationSplitView rather than adding a second navigation container inside an ArrangementView.

## Continuity and accessibility

Pairing and new-session drafts initialize once per presentation. A layout-driven reappearance cannot reload saved values over a draft. Submission errors receive VoiceOver focus. At accessibility text sizes the session filter becomes a menu, while names and branches can wrap and metadata stacks vertically. Search, selection, terminal state, and session ownership do not depend on display pose.

Apple’s exported app-resizability skill was used to audit app-owned Swift code and build settings: launch screen present, all four orientations declared, no full-screen opt-out, scene-based lifecycle, and no global screen/orientation/idiom or cached-safe-area layout assumptions. Third-party terminal code remains pinned, not rewritten.

## Toolchain and remaining validation

The previously downloaded Xcode_27.1_beta.xip was expanded under ignored `.local/toolchains/`. Build scripts select it for this project only; `/Applications/Xcode.app` and global xcode-select remain unchanged. For distribution, use a current release Xcode and repeat physical-device, accessibility, camera-activation, and keyboard checks. Simulator screenshots demonstrate the actual native rail, not a custom approximation.

## Input and browser refinement

The native draft editor fills its proposed space and scrolls internally as text grows; adding lines does not resize the editor. Dictation remains the system keyboard’s dictation. The workspace selector lives in the navigation bar, and browser navigation occupies one address row; Forward and preview details live in its menu. The layout preserves the authenticated preview transport and existing workspace state.
