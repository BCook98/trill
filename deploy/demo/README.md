# Isolated reviewer deployment

These templates run the scripted demo, not a real provider or coding agent. Build `sidecar/cmd/trillctl` as `trill`, use a separate unprivileged service user and private state volume, and provision trusted TLS for the chosen hostname. Expose only the HTTPS listener; terminal streams share it. Preview stays on loopback and administration uses a private Unix socket.

The service has no general shell execution path. Limit resource usage, deny new outbound access and metadata/private-network access, and allow established HTTPS replies and loopback fixture traffic. Manage certificates and updates through a separate operator path.

The password-protected `/review` page issues normal single-use, ten-minute pairing invitations. Share its URL and reviewer credentials privately. Never commit a password, invitation or device token. The sandbox is shared: use sample content, document retention, and reserve a maintenance window for global fault checks.

The [Fly template](../fly/README.md) explicitly trusts Fly’s HTTPS edge and private listener topology. Local [smoke checks](../../docs/DEMO_SMOKE.md) need no cloud host. No deployment is implied by editing these templates.
