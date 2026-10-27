# Demo scope and coverage

The isolated demo shares production HTTPS authentication, pairing/revocation, terminal leases/WebSocket routing, files, photos, video and preview handlers. A synthetic Herdr catalog and restricted literal editor supply deterministic sample workloads. The controller implementation does not launch a shell.

| Check | Scripted demo | Separate evidence needed |
| --- | --- | --- |
| TLS, pairing, invitation expiry and revocation | Real handlers and credentials | Physical networks and certificate renewal |
| Terminal input, resize, scroll and reconnect | Real WSS path and bounded synthetic controller | Native Herdr controller and physical gestures |
| Files, photos and previews | Real confined handlers on generated files | Personal-project workflows only with explicit authorization |
| Terminal creation and rename | Scripted model through normal app/API | Native Herdr mutation/readback |
| Agent detection and actual coding | Not simulated as real work | Dedicated Herdr lab and explicitly started real agent |
| Spaces and Git worktrees | Catalog fixtures | Native provenance/trust checks and isolated repository |

No demo result should be described as a physical-device, production-provider or TestFlight qualification unless that exact path was exercised. Use [the real Herdr probe](../prototypes/herdr/README.md) for native terminal behavior and [demo capture](DEMOS.md) for actual app footage.
