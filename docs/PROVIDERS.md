# Xirp and Herdr

Trill can show Xirp sessions and Herdr terminals together on a paired computer. Each session stays in the application that owns it. Xirp terminals attach through tmux; Herdr terminals attach through Herdr's direct-terminal client. Both use Trill's existing mosh transport, HTTPS pairing, and device credentials.

## Configure a computer

Xirp is the default. For a new installation, select providers during setup:

```sh
./scripts/setup-companion.sh --backends xirp,herdr
```

For an existing installation, change providers without re-pairing:

```sh
trill providers --backends xirp,herdr
trill providers
```

Select Herdr alone or return to Xirp:

```sh
trill providers --backends herdr
trill providers --backends xirp
```

These commands change only Trill's configuration. If the owned companion is loaded, it restarts and releases its mosh connections; the underlying sessions remain running. Device pairings and the control permission are preserved. Certificate renewal is not restarted. Interrupted changes recover through `trill start` using the exact previous configuration from private state, with executable/network identities still checked.

Herdr must already be installed and its selected servers running. Trill never installs, starts, updates, restarts, or takes over a Herdr server. The default selected Herdr server is `default`. To include named servers, list them explicitly:

```sh
herdr session list --json
trill providers --backends xirp,herdr --herdr-sessions default,work
```

`--herdr-path /absolute/path/herdr` chooses another binary. Otherwise the companion resolves `herdr` from its managed PATH, including the user's `~/.local/bin` and standard Homebrew directories. Up to sixteen Herdr server names may be configured. `--herdr-path` and `--herdr-sessions` require a Herdr backend. Switching to Xirp clears the Herdr selection.

Managed setup also saves the effective Herdr configuration location from the invoking shell's `XDG_CONFIG_HOME`, or `~/.config` when unset. This is the base containing `herdr/`, not the `herdr` directory itself. To select another location explicitly:

```sh
trill providers --backends xirp,herdr --herdr-config-home /absolute/path/config-home
```

The companion pins this location for metadata, native actions, ownership checks, and mosh attachment, even when launchd has a different environment. The same location is part of session identity.

Foreground deployments use the same flags:

```sh
trill serve --backends xirp,herdr --herdr-sessions default,work \
  --listen 192.0.2.10:7443 --cert /absolute/path/tls.pem --key /absolute/path/tls.pem \
  --pairing-store /absolute/path/devices.json --mosh-host 192.0.2.10
```

Control stays disabled unless explicitly enabled. Pairing, TLS, and terminal access retain their existing rules. `--tmux-socket` remains an isolated terminal lab and cannot be combined with provider selections. The separate `--herdr-lab-root` option is for disposable integration probes, requires one Herdr server, and rejects terminals outside its declared directory.

## Available actions

| Feature | Xirp | Herdr |
| --- | --- | --- |
| List projects/workspaces and live terminals | Yes | Yes, across selected servers |
| Open the existing terminal over mosh | Yes | Yes |
| Rename | Native session rename | Native pane-label rename |
| Create a terminal | Project folder | New tab using the workspace's default directory |
| Create a space | Existing project setup | Native workspace in an existing folder on the computer |
| Create a worktree | Existing Xirp configuration | Native Git branch and linked-worktree workspace |
| Start a coding agent | Existing Xirp configuration | Start the agent in an existing or newly created terminal |
| Stop, archive, reopen/resume | Existing Xirp behavior | Unavailable as Trill actions |
| Session files, photos, local web preview | Existing behavior | Existing behavior when authoritative cwd is available |
| Project files | Project root | Worktree checkout when Herdr supplies checkout provenance |

The Sessions browser becomes **Spaces** when Herdr is present. It preserves Herdr's workspace order, nests linked worktrees beneath their primary repository space, shows branches and the workspace focused on the Mac, and lets you collapse each space or worktree. Empty Herdr spaces remain visible. A worktree whose primary space is closed remains visible at the top level. Grouping uses Herdr's repository provenance, never matching folder names or pane directories. The separate Projects browser retains project file and archive access.

Session names prefer a pane label, a meaningful tab label, an agent title, then Herdr's stripped terminal title; native pane IDs remain the final fallback. A completed agent turn remains a live terminal: Herdr's `done` becomes idle, and `blocked` becomes needs-attention. Trill does not turn pane closure into a resumable stop or invent an archive history.

The **+** menu offers **New session**, **New space**, and **New worktree** according to the available capabilities. A Herdr session is a new terminal tab in an existing space. New space takes a name and an absolute path to an existing folder on the Mac. New worktree takes a primary repository space, branch name, and optional label; Herdr chooses its checkout location and base using its native defaults. Both open shells, preserve desktop focus, and let you start an agent in the resulting terminal. They do not start a new Herdr daemon or named persistent server session. Linked worktrees remain available for terminal sessions but cannot be sources for additional worktrees, even when their primary space is closed. The companion enforces this restriction as well as the app. Native repository trust checks remain in force; Trill never passes `--trust-repository`.

The API supplies capabilities per session, project, and provider. Provider status includes `createSpace`, so a healthy server with no spaces can still create its first one. `POST /v1/spaces` accepts `providerId`, `name`, and `directory`; the provider ID comes from the catalog. `POST /v1/sessions` additionally accepts `kind: "worktree"`, `projectId`, `useWorktree: true`, `branch`, and optional `name`. Both require control permission and use one native mutation followed by correlated metadata readback. The app blocks repeated submission after an uncertain response and asks you to check Spaces.

Git provenance and branch labels come from native worktree listings. Optional Git discovery has a two-second budget per snapshot; failures retain existing snapshot provenance and terminal access. This also handles fresh spaces whose initial snapshot has not yet discovered their repository. No direct Git command runs against personal repositories.

Older companion responses without capabilities keep their previous Xirp behavior.

If a provider or selected server is unavailable, other healthy providers remain browsable. The app shows a provider-specific warning. If every provider is unavailable, refresh fails and previously displayed metadata is marked stale.

## Attachment and identity

Herdr allows one direct controller for a terminal. Before allocating a mosh server, Trill briefly acquires and releases a controller at the requested size, without input or takeover. An occupied terminal returns HTTP 409. Trill prevents competing Herdr leases across paired devices. Reconnect from the same paired device replaces only that device’s tracked Mosh connection to the same terminal. This recovers after an app restart, a lost attach response, or a failed disconnect request, without needing the old lease ID. A concurrent release is allowed to finish before reacquiring the controller. The terminal and its running program remain alive. Legacy shared-token clients and external direct controllers are never implicitly replaced; disconnect those clients before trying again. Normal desktop Herdr TUI use can coexist with Trill's direct attachment.

Trill automatically hands a Herdr terminal back to the desktop when you leave its session detail view or background the app. It stops the phone transport and releases its Mosh lease; Herdr removes the direct-attach resize lock and restores the desktop client's geometry. The terminal and its programs keep running. Trill retains the last screen and unsent draft locally. Returning to a previously opened session requests a fresh attachment at the current phone size after the old attachment's cleanup; input is never replayed. An explicit Disconnect cancels that automatic-return intent. Browsing a session that was never opened does not attach.

The app requests a bounded iOS background-execution allowance for controller cleanup and pauses all connections independently, so one slow release does not delay another terminal's handoff. A failed release remains a visible cleanup warning; network loss can prevent the companion from confirming handoff. A returning phone still receives the normal busy-terminal error if another direct controller owns that terminal. Xirp continues to retain and resume its existing Mosh transport.

Another client can acquire ownership after preflight. Herdr remains authoritative in that race; Trill does not retry with `--takeover`. Connection failure and foreground recovery use the existing explicit connection intent and lease lifecycle. Recovery identity comes from the authenticated device credential, never from request fields. Devices sharing a copied credential are treated as the same device and must be paired separately for independent ownership.

Herdr IDs include local configuration location, server name, and terminal identity. They differ across servers even when native IDs are identical and survive a terminal moving between workspace-qualified pane IDs. Existing Xirp IDs, drafts, uploaded files, and pairings retain their identities. Mutation targets are freshly resolved. Commands use fixed executable/argument vectors, and native operations are sent once with metadata readback. If a response is unconfirmed, refresh to check the result. Do not automatically repeat the mutation.

Direct attach uses Herdr's internal protocol. Trill reads the installed CLI's schema and requires the server snapshot to match its protocol, with a minimum of protocol 22. It does not pin to one patch version. Herdr 0.9.5 is the release exercised by the live probes; compatibility failures produce provider unavailability rather than a server restart.

## Validation and limits

[The reproducible probe](../prototypes/herdr/README.md) exercises real Go bootstrap/cleanup, shared Swift MoshCore, full-screen Codex, native Herdr rename and terminal creation, mixed listings, an unavailable server, and occupied-terminal errors. The opt-in `TRILL_REAL_HERDR_TEST=1 go test -run '^TestRealHerdrSpaceAndWorktreeCreation$' -v .` command in `sidecar/` creates its own Herdr config/server and synthetic Git repository under `/tmp`, exercises space → worktree → terminal creation, verifies hierarchy/branch readback, and stops only its owned server.

Tests use disposable Herdr state and only read Xirp metadata. They never mutate an existing personal Xirp session.

Physical-device roaming, packet loss, and the full keyboard/scrolling matrix remain release testing, as they do for the existing transport. The Swift probe runs on macOS with the same MoshCore library as iOS; it does not substitute for an end-to-end physical iPhone test.

Herdr is an optional user-installed executable, not bundled in Trill. See the [upstream CLI](https://herdr.dev/docs/cli-reference/) and [socket/protocol reference](https://herdr.dev/docs/socket-api/).
