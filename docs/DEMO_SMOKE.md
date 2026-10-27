# Demo smoke checks

Build and exercise a fresh local scripted runtime:

```sh
./scripts/build-demo.sh
python3 scripts/demo-api-smoke.py --trill .build/demo/trill --output /tmp/NEW-REPORT.json
```

The standard-library client verifies the exact local TLS leaf before sending credentials. It tests invitation expiry/replay, terminal creation/rename, authenticated WebSocket input, deliberate Return, exact resize, distinct reconnect lease identities, confined files, photos, previews, capacity and revocation. Each run creates its own private directory and listeners and stops only its own process. Reports omit device tokens and invitations.

Hosted preflight is read-only:

```sh
python3 scripts/demo-fly-smoke.py --app trill-demo --output /tmp/NEW-PREFLIGHT.json
```

`--allow-demo-mutations` additionally authorizes owned sample terminals, a temporary registration and global fault injection. Use an exclusive sandbox maintenance window. The tool verifies the demo manifest and HTTPS origin, refuses foreign session/lease mutations, clears its armed faults and revokes its own registration. Created sample terminals/uploads follow sandbox retention. It does not deploy or stop the remote service.

For native UI checks, `scripts/demo-smoke.py --help` exposes isolated simulators and observed-control assertions. It requires a prebuilt app and an explicitly owned synthetic backend. Identity, visual nonce, insertion receipts, editor geometry and uncertain-outcome guards remain required. Automated API success does not establish UIKit gestures, physical roaming or native Herdr integration.
