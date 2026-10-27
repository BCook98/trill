# Trill API v1

All endpoints use HTTPS and bearer authentication except `POST /v1/pair`. The companion defaults to read-only. Terminal attachment, mutations, files, media, uploads and preview grants require control enabled on the Mac.

## Catalog and native operations

| Endpoint | Contract |
| --- | --- |
| `GET /v1/health` | `{name,version,readOnly,backend:"herdr",terminalLab,sessionManagement:false}` |
| `GET /v1/projects` | `{projects,providers}`; Herdr spaces and linked worktrees |
| `GET /v1/sessions` | `{sessions,providers}`; current Herdr terminal metadata |
| `GET /v1/changes` | Bounded long poll returning `{changed}` after a native event or timeout |
| `GET /v1/folders?path=` | Browse folder names on the Mac; defaults to the home directory |
| `DELETE /v1/sessions/{id}` | Close the current native pane and its running program, confirmed by readback |
| `PATCH /v1/sessions/{id}` | `{name}`; rename the current native pane label |
| `POST /v1/sessions` | `{projectId,kind:"terminal",useWorktree:false,directory?}`; create a terminal tab in an optional existing Mac folder |
| `POST /v1/sessions` | `{projectId,kind:"worktree",useWorktree:true,branch,name?}`; native linked worktree |
| `POST /v1/spaces` | `{providerId,name,directory}`; space in an existing absolute directory |

Per-item capabilities include `terminalStream`, `rename`, `closeTerminal`, `createTerminal`, `worktree`, and `files`. Provider records carry `id`, `name`, `available`, optional `message`, and `createSpace`. The client does not infer support when capabilities are absent. Unavailable named servers do not hide healthy ones; all unavailable is an error.

Session identities bind the pinned configuration home, named server and stable terminal ID. `done` is a live idle terminal. Native mutations are issued once then checked by metadata readback. HTTP 409 with an unconfirmed-outcome message requires inspection before a manual retry. No generic Herdr API or remote command proxy is exposed.

Folder browsing requires control and returns directory names and paths only, using bounded listings. Paths must be canonical absolute paths. Demo and lab browsing/creation stay confined to their owned fixture roots. The optional terminal `directory` is validated before any mutation and passed as one literal `--cwd` argument. Space defaults remain in use when omitted. Closure resolves the stable terminal’s current pane, sends one native close and confirms that exact terminal disappeared; an uncertain result never triggers an automatic retry.

## Terminal stream

`POST /v1/sessions/{id}/terminal` accepts `{cols,rows}` and returns only `{leaseId}`. Dimensions are bounded to 20–500 columns and 5–200 rows. An occupied direct controller returns 409; no takeover is attempted.

Connect a WebSocket to `GET /v1/leases/{leaseId}/terminal` on the same authenticated HTTPS origin (`wss`). Each lease accepts one connection. Only its authenticated device may connect or release it. `DELETE /v1/leases/{leaseId}` releases the controller; the Herdr terminal and its program survive. Revocation, expiry, socket closure and companion shutdown use the same cleanup path. An unclaimed production stream is released after 15 seconds.

Server text messages are native JSON `terminal.frame` records with `seq`, `width`, `height`, `full`, and base64 `bytes`. Frames are capped at 4 MiB and retained in a bounded queue. OSC 8 links reach SwiftTerm intact.

Client text messages use a fixed allowlist:

```json
{"type":"terminal.input","bytes":"BASE64"}
{"type":"terminal.resize","cols":90,"rows":28}
{"type":"terminal.scroll","direction":"up","lines":3,"column":7,"row":5}
{"type":"terminal.release"}
```

Input chunks are at most 8192 decoded bytes. Scroll lines are 1–200; zero-based column/row preserve hit location for mouse-aware apps. Unknown fields and command types are rejected. Heartbeats detect idle connection failure. There are no separate terminal ports or connection keys. Removed transport/lifecycle routes are unregistered.

The same paired device may replace its own tracked controller for the same terminal. Other paired devices and external controllers receive a busy error. Authentication comes from the credential, never request fields. Input is never replayed during recovery.

Errors use non-2xx JSON `{error}` and exclude raw command output and secrets. TLS is required outside explicitly enabled development loopback. Legacy shared-token mode is available only when explicitly configured; managed pairing uses per-device credentials.

## Device pairing

Managed setup passes `--pairing-store /absolute/path/devices.json`. Without an explicit `--token-file`, this disables the legacy shared master token. The local `trillctl pair` command authorizes one device using a 256-bit, single-use invitation with a maximum ten-minute lifetime.

`POST /v1/pair` is the only unauthenticated endpoint. Its JSON body is `{code,name}`; success returns `{deviceId,token}`. A malformed request returns 400, an expired/consumed/unknown code returns 403, and unavailable private state returns 503. Permanent device tokens are returned once and stored only as hashes on the Mac. Lost enrollment responses require a new invitation. The phone keeps an issued credential in a separate pending Keychain entry until connection verification succeeds, preserving the previous computer during recovery.

The invitation payload is `{version:1,address,name,fingerprint,code,expiresAt}` with an RFC3339 expiry. `fingerprint` is empty for a system-trusted certificate, or the SHA-256 leaf certificate fingerprint for LAN pairing. QR data uses raw JSON; paste links use `trill://pair?data=<base64url JSON>`. Importing a code never sends a request until the user reviews the computer and confirms pairing.

A device can delete only its own terminal leases. Local revocation denies subsequent API calls and closes that device’s owned terminal controllers; Herdr terminals and other devices’ leases survive. See [pairing storage and lifecycle](../sidecar/pairing/README.md).

## Files and page previews

File and preview endpoints require authenticated control access. `GET /v1/sessions/{id}/files?path=` and `/v1/projects/{id}/files?path=` list the authoritative root or a canonical relative directory. Response: `{path,entries:[{name,path,kind,size}],truncated}`; kind is `directory`, `file`, or `unsupported`. Matching `/file?path=relative/name` routes return bounded raw UTF-8 text (HTML as plain source) or JPEG/PNG/GIF/WebP. No arbitrary roots, traversal, or symlink escapes are allowed. `GET /v1/sessions/{id}/attachments` and `/attachment?path=...` read the session’s uploaded images. Existing archived worktrees and attachments remain readable without resuming.

`GET /v1/sessions/{id}/video?path=relative/name` and `GET /v1/projects/{id}/video?path=relative/name` download videos for the native player. They require bearer authentication and enabled control, use the same authoritative session worktree/project roots and canonical `path` validation as `/file`, and prohibit symlink escapes. No caller-supplied roots or additional query parameters are accepted. Archived worktrees remain readable without resuming or launching an agent.

Video downloads accept `.mp4`, `.m4v`, and `.mov` case-insensitively and validate ISO BMFF file-type brands or a legacy MOV movie header; an extension alone is insufficient. Successful responses are full-file HTTP 200 with `Content-Type: video/mp4` or `video/quicktime` based on the container header, exact `Content-Length`, `Cache-Control: no-store`, and `X-Content-Type-Options: nosniff`. Range requests currently receive the full file. Video responses have a bounded five-minute write deadline, replacing the ordinary API write timeout. The limit is **100 MiB (104857600 bytes)** inclusive; larger opened files return HTTP 413 with an explicit download-limit error. Invalid headers/types return 415. The server streams from the opened descriptor using bounded memory and at most its observed size; growth is excluded and a truncated/interrupted transfer fails rather than completing a shorter download. Native clients download to a private cache and must reject incomplete transfers before playback. Container checks do not guarantee that the native player supports the file's codecs. Existing `/file` limits (2 MiB text, 10 MiB images), static-file serving, and image attachments are unchanged.

`POST /v1/previews` accepts `{sessionId,port}` and returns `{id,token,expiresAt}`. It authorizes one localhost HTTP port (1024 to 65535, excluding the companion listener) for 30 minutes. `DELETE /v1/previews/{id}` releases an owned grant. A valid `trill_preview` cookie selects the proxy before API routing, allowing development routes such as `/v1` and root-relative assets. An invalid cookie never falls through to the API. The proxy removes companion credentials before forwarding. Native API requests continue to use bearer authentication separately from WebKit.
