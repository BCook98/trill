# Trill Mac companion

The Go companion exposes a small authenticated HTTPS API for Herdr. The managed CLI is `trill`; its private daemon is `trilld`. All terminal traffic uses the same HTTPS origin as metadata, files and previews.

```sh
./scripts/setup-companion.sh
trill control --enable
trill pair
trill status
trill doctor
```

Herdr must already be installed and its selected servers running. `trill providers --herdr-sessions default,work` selects existing named servers. `--herdr-path` and `--herdr-config-home` pin custom installations. Pairings survive configuration changes.

For a foreground listener, supply TLS, a private pairing store and a reachable address:

```sh
trill serve --listen 192.0.2.10:7443 \
  --cert /absolute/path/tls.pem --key /absolute/path/tls.pem \
  --pairing-store /absolute/path/devices.json --allow-control
```

`--insecure-loopback` allows HTTP only for isolated development on loopback. Production defaults to read-only; control is explicit. The public API has no arbitrary command endpoint. A terminal lease starts only the native structured Herdr controller and releases it on disconnect without stopping its terminal.

```sh
go -C sidecar test ./...
go -C sidecar vet ./...
go -C sidecar build ./cmd/trillctl ./cmd/trilld
```

Run commands above from the repository root. See [setup](../docs/SETUP.md), [API](../docs/API.md), [Herdr semantics](../docs/PROVIDERS.md), and the [isolated demo](../docs/DEMO_RUNTIME.md).
