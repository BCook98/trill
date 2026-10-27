# Scripted demo runtime

The demo uses real HTTPS, pairing, authenticated terminal WebSockets, file/upload handlers and browser previews. Its spaces, sessions and command results are scripted. It starts no Herdr server, shell, coding agent or AI provider; it cannot prove real Herdr integration.

```sh
./scripts/build-demo.sh
.build/demo/trill demo serve --state-dir /tmp/NEW-PRIVATE-DEMO \
  --listen 127.0.0.1:19443 --preview-listen 127.0.0.1:18080
```

Use a new directory; the runtime creates its private ownership marker. The foreground process prints safe status. In another terminal:

```sh
.build/demo/trill demo status --state-dir /tmp/NEW-PRIVATE-DEMO --json
.build/demo/trill demo pair --state-dir /tmp/NEW-PRIVATE-DEMO
.build/demo/trill demo stop --state-dir /tmp/NEW-PRIVATE-DEMO
```

Pair through the normal phone UI. Select a live **Demo · Herdr** session; allow terminal access. Type `help` to see literal demo commands. Input is edited and bracketed paste remains literal until deliberate Return. There is no shell execution. New session creates a sample terminal tab; rename uses the same native app form. History/lifecycle actions are absent.

Local certificates are generated privately and pinned through the invitation. A public listener needs supplied TLS and an explicit reachable HTTPS address. The preview server is loopback-only; administrative commands use a private Unix socket. The API and terminal stream use one HTTPS port.

The optional `/review` page requires `--review-password-file` and supplied trusted TLS, or the explicit Fly edge TLS contract. It issues normal expiring invitations, not a permanent bearer credential. [Deployment templates](../deploy/demo/README.md) are isolated review services, never production companions.

Faults are private admin operations: `busy`, `capacity`, `api-unavailable`, `provider-unavailable`, `create-unconfirmed`, `upload-rejected`, `drop-leases`, `preview-unavailable`, `preview-expired`, `invitation-expired`, and `clear`. Check the CLI/status for supported names. Faults and resets affect the shared sandbox: reserve an exclusive maintenance window before remote mutation tests.

State persists across normal restarts. An inactivity reset after the configured retention interval is global to the sandbox; active use can extend it. Reset is explicit with `demo reset --yes` on a stopped owned instance. Do not use personal code, credentials, photos or agent accounts in a public demo.

See [API smoke checks](DEMO_SMOKE.md) and [coverage](DEMO_SERVER.md).
