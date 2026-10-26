# Local device pairing

`NewStore("")` selects `~/.config/trill/devices.json`. Setup can instead pass
`filepath.Join(stateDir, "devices.json")`; pass that same absolute path to
`trilld --pairing-store`. The daemon does not create or read a legacy master
credential in this mode unless `--token-file` is also explicitly supplied.
Session control stays disabled until `--allow-control` is supplied.

The local setup CLI's explicit `pair` action calls `CreateInvitation` with a
positive TTL of at most ten minutes. Each invitation authorizes exactly one
device. Show the computer details to the phone user for review before they tap
Pair. There is no remote invitation-creation or pending-approval endpoint.

The QR producer uses raw JSON with these fields:

```json
{"version":1,"address":"https://host:7443","name":"Mac name","fingerprint":"64hex or empty for CA trusted","code":"invitationcode","expiresAt":"RFC3339"}
```

An optional paste/open-URL form is `trill://pair?data=<base64url JSON>`. The
invitation is 32 cryptographically random bytes encoded as 64 lowercase hex
characters. It is sensitive and must not be logged. QR encoding and the local
pair command belong to setup; this package provides their persistent backend.

The phone sends unauthenticated `POST /v1/pair` with JSON
`{"code":"<invitation>","name":"<user label>"}`. Success returns only
`{"deviceId":"...","token":"..."}`. Invalid fields return 400; an unknown,
expired, or consumed invitation returns 403. Names must be nonblank UTF-8, at
most 200 bytes, without control characters. The bearer is returned once and
never persisted in clear text. A lost response requires a fresh invitation.

State and the stable `.lock` file must be regular, single-link, user-owned 0600
files. The parent must be user-owned without group/other write access; unsafe
ancestors, linked files, malformed state, and oversized state fail closed.
The store bounds JSON to 1 MiB, 1,024 device records, and 128 live invitations.
Expired invitations are pruned when creating a new one. Revoked device records
remain for local auditing and count toward the limit. Lock acquisition is
bounded to three seconds. Mutations use an exclusive cross-process flock and
fsynced atomic replacement; readers use a shared lock. The `.lock` file must
not be removed while the daemon or CLI is using the store. The security
boundary excludes malicious processes running as the same user.

`Revoke` is idempotent and takes effect on the next API authentication without
a credential cache. The HTTP handler polls active paired leases every 100 ms
to close a revoked device's idle mosh processes, plus filesystem and process
shutdown time. It never stops tmux or another device's leases. Unreadable state
also closes paired leases. Device callers can delete only their own leases;
explicit legacy master-token callers retain their existing lease permissions.
`HTTP.Close` drains paired leases and the watcher; `Service.Close` drains all
leases and also terminates the watcher.

TLS startup validates the configured certificate/key pair. Each subsequent
certificate callback checks file identity, modification time, and size. Changed
files reload as a matching, currently valid pair. A missing, mismatched,
invalid, expired, or not-yet-valid replacement retains the previous pair only
while that leaf is still valid; the next callback retries. Once no valid pair
is available the handshake fails. Existing TLS connections remain established.
