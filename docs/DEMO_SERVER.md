# Demo server and app smoke-test scope

Status: implementation scope, 25 October 2026. The local server and transport/API harness are implemented; see [running the demo](DEMO_RUNTIME.md). The Fly.io reviewer sandbox is deployed and its public HTTPS/Mosh checks pass; see [reviewer hosting](../deploy/fly/README.md) for the image, measured memory use and repeatable checks. The coverage table is the acceptance target, not a claim that every native/manual check has passed.

## Purpose

Give reviewers and developers a small, repeatable environment for exercising Trill from the native UI through its real HTTPS and Mosh connections. Provider data and terminal workloads are synthetic. This is an app end-to-end fixture, not evidence that Xirp or Herdr integration works.

The environment must need no Xirp, Herdr, tmux, coding agent, AI account, database service, or arbitrary shell. Keep it inexpensive enough to leave one reviewer environment running throughout review. Do not promise zero risk or a particular hosting price before measuring resource use and selecting a host.

## Architecture

```text
Unmodified Trill app
  ├─ HTTPS → existing companion handlers/authentication
  │            └─ demo provider → isolated state + sample files
  │            └─ existing preview proxy → Go fixture website
  └─ Mosh UDP → real mosh-server → Go demo terminal child
                                      └─ private IPC → demo state
```

Ship one Go executable with demo subcommands, plus the upstream `mosh-server` runtime dependency. Mosh's PTY child is an internal mode of that same executable. It runs a bounded command interpreter, never a shell. Packaging must pin Mosh and preserve its license notices. This is not a standalone Go-only Mosh implementation.

Reuse production pairing, bearer authentication, capability fields, HTTP validation/errors, file/media serving, preview grants, attachment validation, and Mosh lease ownership/cleanup. Add a narrow provider boundary where the current `Service` branches on Xirp/Herdr. Keep public API schemas unchanged unless a specific compatibility need is demonstrated. Do not maintain a second hand-written copy of the HTTP API or fake successful transport responses.

The current test `Runner` seam alone is insufficient: runtime provider operations, file roots, persistent terminal state, and controller ownership need an explicit demo implementation. Retain the production Mosh runner. The demo attachment argv is fixed to the resolved executable and an opaque, server-owned session ID; no client-supplied executable, arguments, or command string can reach process spawning.

The parent owns session state. A new terminal child renders the existing screen/model and subscribes to updates through private, bounded IPC. Losing a Mosh child must not reset the project, rerun the previous command, or erase the transcript. A simulated running task advances in the parent while disconnected. API lifecycle actions, terminal output, files, and previews all read the same state.

Start with one sandbox per process, separate state directory, pairing store, hostname/ports, and UDP allocation. Local parallel tests use separate processes. Two processes provide the app's two-computer scenario. Do not introduce a shared multi-tenant API service in the first implementation. The reviewer sandbox is reserved and never reset by CI.

## User-visible behavior

The paired computer, sample projects, terminal greeting, and review instructions identify the environment as a demo. No hidden reviewer detection or special iOS networking path. Normal consent, Keychain storage, certificate verification, and provider capability handling still apply.

Offer three explicit fixture profiles:

| Profile | Purpose |
| --- | --- |
| Xirp capabilities | Create coding-style sessions, project-folder/worktree choices, rename, archive, stop, reopen and resume against synthetic state. |
| Herdr capabilities | Terminal creation and rename; unsupported Xirp actions stay unavailable. Busy-controller scenarios match the API contract. |
| Mixed capabilities | Both groups, including one unavailable provider while the other remains usable. |

These names describe capability fixtures, not running providers. A synthetic worktree is a separate fixture directory, not a Git worktree. A coding-style session is a scripted demonstration, never a simulated Codex or Claude identity. Original real-provider demos remain distinct from these captures.

The starter project contains UTF-8 source, nested folders, an image, a short licensed/generated video, static HTML/SVG with relative assets, and an interactive preview with multiple routes, query parameters, and WebSocket updates. Include current, attention, active, archived, stopped, failed, and empty-project states.

Suggested terminal commands: `help`, `ls`, `cat README.md`, `demo heading <text>`, `demo task`, `demo photo <uploaded-path>`, and `clear`. Parsing is purpose-built: no shell expansion, pipes, redirection, subprocesses, arbitrary filesystem reads, or network fetches. Unknown input gives a helpful error. Text written to HTML is escaped; terminal control sequences cannot be injected through user values.

The terminal must handle UTF-8, cursor editing, Return, Escape, Tab, arrows, Ctrl-C, bracketed paste, resize/redraw, and bounded full-screen output. Expose a diagnostic mode that visibly reports received key sequences and terminal dimensions, without executing them. It can verify one-shot Shift and Shift+Return behavior without pretending to be a general shell.

## End-to-end coverage and acceptance

Drive the ordinary app UI with Facebook IDB through `mise`, extending the existing runner where useful. Use dedicated simulators and explicit physical-device targets. Scenario scripts must wait for observed states rather than fixed sleeps. Use synthetic media only. Never operate personal sessions or wipe a user's existing pairings.

| Flow | Required assertions |
| --- | --- |
| Pairing | QR on a physical device and pasted link on simulator; real TLS/pinning, invitation redemption, persisted device credential, expiry, reuse rejection, wrong pin and revoked credential. Reset affects only owned fixture state. |
| Computers | Pair A and B, switch catalogs, keep drafts/permissions scoped correctly, disconnect the previous transport, forget one without affecting the other, restore saved pairing after relaunch. |
| Sessions/projects | Search and filters, status colour/labels, context menus, projects with no live sessions, archived items absent from current sessions and discoverable in projects. |
| Lifecycle | Create terminal; create scripted coding-style session in project folder and synthetic worktree; rename, archive, reopen, stop and resume. Verify UI plus resulting state. Herdr profile hides unsupported actions. |
| Consent/control | Reject and approve terminal/photo access; remember and reset each independently; server read-only mode still blocks mutations and terminal control. |
| Terminal input | Direct keyboard, Write draft, Insert without automatic Return, deliberate Return, multiline/bracketed paste, Ctrl/Shift/special keys, Unicode, literal punctuation, and draft isolation. Never retry uncertain insertion automatically. |
| Layout | iPhone 17 Pro and Duo cover/unfolded/half-folded portrait, light/dark, keyboard visible/hidden; resizing reaches the terminal. Pane toggles, presentation and dismissal preserve the intended session and browser state. |
| Recovery | Short background/resume, lost UDP traffic, owned Mosh child exit, new lease, API outage/restart, process termination and explicit reopen. Input remains gated during recovery; commands are not replayed; parent-owned session changes survive. |
| Leases | Same-device replacement, other-device busy rejection, normal release, expiry, capacity exhaustion, cancellation during bootstrap, and cleanup with no leaked ports/processes. |
| Files | Nested navigation, source/image/video display, video seeking and temporary-file cleanup, unsupported/oversized/missing files, path escape rejection and attachment browsing. |
| Photos | Native picker → consent → real JPEG upload → quoted draft path → Insert/Return → fixture acknowledgment and Photos listing. Assert metadata stripping, limits, cancellation, and no duplicate approval prompt after remembered consent. |
| Static preview | Real authenticated file route, relative assets, script/network restrictions, HTML/SVG context menu and source-preview action. |
| Live preview | Real proxy/grant/cookie/WebSocket path, navigation/back/forward/reload, path/query preservation, hidden-pane restoration, expired grant renewal, unavailable upstream and recovery. |
| Links | Localhost terminal link opens the proxy preview; an external HTTPS link offers/uses the intended external handling without leaking companion credentials. |
| Accessibility/system | VoiceOver labels/order, Dynamic Type and keyboard access; real dictation and hardware keyboard on suitable physical devices. Unsupported automation is reported as manual/blocked, never inferred from typed text. |

A single successful journey should pair, create a session, type a demo command, observe a real fixture file change, inspect it, open its preview, upload a sample photo, reconnect with state preserved, archive/reopen, disconnect, and forget. Add focused scenarios for the other rows. Use both capability profiles: no one provider profile supports every action.

A passing test needs visible native evidence and the expected server-side state/event. API calls may seed fixtures and inspect results, but cannot substitute for the UI action being tested. Record the app executable hash/build provenance, OS/device, fixture/protocol version, source revision, scenario seed, timings, assertion results, and artifact paths. Redact credentials, invitation links, Mosh keys, arbitrary typed text and photos from ordinary logs. Store recordings privately unless explicitly reviewed for publication.

## Deterministic failures

Provide a separate local-only control socket for selecting seeds, resetting owned state, querying assertions, and arming one-shot faults. It must never be routed through the public API or preview proxy. Hosting operators can reach it through their private administrative channel.

Faults include expired invitation/grant, rejected credentials, read-only mode, unavailable provider, busy terminal, exhausted Mosh capacity, delayed bootstrap, lost mutation response after successful creation, API unavailability, owned Mosh child termination, preview failure, and upload rejection. Mutation uncertainty must preserve the actual created object so the app's refresh-before-retry behavior can be tested.

Use real UDP packet interruption in an isolated test network for transport-loss tests; an injected HTTP error does not prove Mosh recovery. Do not change the host's global firewall or networking. Accelerated expiry uses an injected test clock or scoped fixture setting without changing production security defaults. Faults remain off in the reviewer environment unless deliberately enabled by its operator.

## Pairing and reviewer access

Production invitations remain single-use and expire after ten minutes. A QR pasted into review notes cannot serve as a permanent credential.

Provide a small HTTPS reviewer page protected by a dedicated, revocable credential. It issues a fresh normal invitation and displays its QR and pairing link on demand. Keep the page usable from the same phone so the link can open Trill. Credential lifetime spans review; invitation lifetime does not change. No public anonymous signup, shared master API token, or credentials compiled into the app. Reviewer details go only into App Store Connect, never the repo.

Use a publicly trusted HTTPS certificate and a host with reachable Mosh UDP ports. Reviewers should not need Tailscale, a custom CA, or local developer tools. Keep the environment available throughout review and provide a contact/recovery path. A network that blocks UDP is still a real limitation, not something the demo should conceal.

Describe simulated provider behavior and real transport behavior in Notes for Review. This fixture is useful reviewer access, not a guarantee that Apple will accept it as sufficient for every feature. Keep real-provider evidence available and resolve any additional reviewer-access requirement with Apple. See [App Review preparation](APP_REVIEW.md) and [Apple's review guidelines](https://developer.apple.com/app-store/review/guidelines/#performance).

## Resource and abuse boundaries

Use one small VM with no autoscaling, paid agent calls, or per-request service dependencies. Fly bills machine time, storage, a dedicated IPv4 and outbound traffic separately; the base estimate is not a hard spending cap. Measure memory and traffic before raising concurrency. Start with one reserved reviewer sandbox and at most two active terminal leases; this is a sizing target, not a measured resource claim. Limit session count, transcript/input size, upload storage, request rate, pending work, and total connections. Reuse existing per-file limits; give the environment a separate aggregate disk quota.

The Go service and terminal children run unprivileged, with a read-only base filesystem and writable owned fixture storage. No personal mounts, cloud IAM credentials, metadata access, Docker socket, package installation, or general outbound network access. Preview targets are restricted to that sandbox's fixture website. This additional allowlist must run through the normal authorization/proxy implementation and must not expose the administrative socket or another sandbox's ports. Loopback alone is not tenant isolation.

Uploaded photos are untrusted and remain private to their sandbox. They are never executed or used as templates. Delete them on explicit reset and after a documented inactivity retention period; show that retention in reviewer instructions. Active review sessions must not be silently reset by a timer. Device revocation closes owned leases and preview grants using production mechanisms. Cleanup targets recorded process identities and owned directories only.

## Separate integration coverage

Keep three reports with independent outcomes:

| Suite | What it establishes |
| --- | --- |
| Demo app E2E | Native UI, consent, API contract, real HTTPS/Mosh, files/uploads/previews, and recovery against a controlled backend. |
| Real-provider integration | Actual Xirp CLI/tmux and Herdr protocol/terminal behavior, catalog mapping, native create/rename/lifecycle, real Git worktrees, controller conflicts, and reconnect without takeover or lost shell state. |
| Physical-device release checks | Camera pairing, dictation, hardware keyboard, background suspension/eviction, cellular/Wi-Fi transitions, IPv6/NAT64, accessibility, sustained output, battery and real device layouts. |

Build provider integration on disposable named Herdr servers and private tmux sockets. The existing [Herdr probe](../prototypes/herdr/README.md) and [isolated lab](DEVELOPMENT.md#isolated-integration-lab-manual) are starting points, not proof of a complete suite. Plain shell checks need no Codex. Actual agent startup/resume, when required for Xirp coverage, is a separate explicitly enabled local integration test; never install agents or their credentials on the hosted demo.

Demo success must never waive provider or physical-device release gates. Reports distinguish pass, fail, skipped and blocked. Simulator tests against the same source do not claim to test the uploaded TestFlight binary.

## Delivery sequence

1. **Provider seam and contract:** isolate the minimum provider operations, preserve existing Xirp/Herdr behavior, introduce deterministic fixture state/capabilities, and run production Go tests/vet plus provider regressions. Document any API changes before implementation.
2. **Real transport:** fixed demo child argv, private state IPC, terminal parser/rendering, real pairing/TLS and Mosh, lease/reconnect persistence, ownership and bounded cleanup. Prove input round-trip and resize with a real client.
3. **Complete app surfaces:** lifecycle scenarios, sample project/media, real attachment path, static/live preview and WebSockets, two-computer fixture, and local-only fault controls. Prove isolation and invalid-input handling.
4. **Repeatable smoke runner:** IDB scenarios for 17 Pro and Duo, light/dark, state-based assertions, evidence manifest, clean-run reset and owned cleanup. Keep manual physical checks explicit. Do not enable tests on untrusted PRs; use local/manual or existing trusted-main policy.
5. **Reviewer deployment:** pinned runtime package, measured resource ceiling, private invitation page, public TLS/UDP, restart/expiry/retention checks, reviewer walkthrough and honest metadata. Deployment and App Review submission are separate from this scope document.
6. **Integration gates:** strengthen real-provider harnesses and publish a coverage matrix beside each release's demo E2E and physical-device reports.

Demo command family (see the runtime guide for flags):

```text
trill demo serve --state-dir <owned-directory> --profile mixed
trill demo pair --state-dir <owned-directory>
trill demo status --state-dir <owned-directory>
trill demo reset --state-dir <owned-directory>
trill demo stop --state-dir <owned-directory>
```

Use the existing `trill` command family. Resolve TLS/listen/UDP configuration, reviewer-page credential provisioning, retention values, and runner flags during implementation; do not add a separate user-facing daemon command. Reset and stop require a matching ownership marker and cannot target the installed personal companion.

The first usable milestone is local pairing → real Mosh → typed command → file change → preview → reconnect with preserved state. The full scoped deliverable adds all coverage rows, fault cases, operational limits, and separate integration reporting. No iOS demo-only transport or fake session renderer is part of the plan.
