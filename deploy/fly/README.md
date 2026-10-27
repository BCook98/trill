# Fly reviewer sandbox

The `trill-demo` configuration runs one isolated scripted demo with a private volume. Public HTTPS port 443 forwards to private HTTP 8080 at Fly’s TLS edge. The terminal WebSocket uses that same origin. No datagram service or dedicated terminal IP is required. The internal TCP check establishes listener availability only; use the authenticated smoke suite for API/terminal behavior.

The fixed image bootstrap verifies its Fly identity and mounted private state, installs only the owned `inet trill_demo` nftables table atomically, and drops all privileges to UID/GID 10001 before exec. Service egress permits established replies and loopback, rejects private networks and all other new traffic, and leaves other UIDs’ operator traffic untouched. Failure to apply the transaction prevents startup. Offline predicate tests do not substitute for checking a real kernel after deployment.

The runtime image includes CA roots, nftables, locales and privilege-drop tooling. It does not install a terminal transport executable or provider. Keep the generated package manifest and image digest for release evidence. Supply the reviewer password through Fly secrets; do not place it in source or logs.

This template keeps one Machine running with a 256 MiB limit and a private 1 GiB volume. Confirm real current capacity and costs before operating it. A capacity limit of two terminal leases is configured; qualify memory and upload load on the exact deployed stream-only image before relying on old measurements.

Use [the smoke tooling](../../docs/DEMO_SMOKE.md) for read-only preflight, or an explicitly authorized exclusive maintenance window for mutation/fault checks. The tooling never deploys or stops the service. Editing this configuration does not update the existing hosted demo. Current native app/reviewer media must be qualified separately.
