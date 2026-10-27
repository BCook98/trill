# Scripted terminal editor

The demo controller uses `StreamEditor` to decode bounded UTF-8 and bracketed paste, edit literal text, and render a bounded ANSI viewport. Only deliberate Return submits a line to the demo command allowlist. No shell, PTY child or process is started.

Input is bounded to 8 KiB per line. Unknown escape sequences and invalid controls are handled locally; editor tests cover split Unicode, paste boundaries, oversized input, cursor movement and rendering bounds. The production WebSocket handler validates transport commands and owns authentication, lease cleanup and revocation. `scripts/demo-api-smoke.py` exercises the real HTTPS path end to end.
