# External TestFlight review pack

Prepared 25 October 2026. Hosted test evidence is dated below; privacy source
updates and publication/build status are recorded separately. Keep passwords,
pairing invitations, device credentials and Mosh keys out of public notes,
feedback and screenshots. Use protected App Store Connect account fields for
the reviewer password. This packet does not submit or distribute a build.

## Build and evidence status

| Item | Verified state |
| --- | --- |
| App / version | Trill; bundle ID `io.bcook.trill`; marketing version `2026.10.25`, local placeholder build `1` |
| Qualified simulator app | Release source `6461a2bc736192aab16365c20695ff0ebfd11b4e`, version `2026.10.25 (1)`; previous bundled policy revision and separate v2 consent keys included. It does not include the later diagnostics paragraph or collection-manifest entries. This is a local source build, not an upload |
| Native consent regression | Four native consent tests passed independently, zero skips. Attended in-place upgrade from `967a057` to `6461a2b` retained pairing and v1 approvals, required separate v2 terminal/photo disclosures; terminal approval was remembered and cancelling photo left it false. New-v2 photo approval/upload was deliberately not exercised |
| Duo attended qualification | Updated app on iOS 27.1 passed light/dark core journeys, real TLS/Mosh, exact input/backend acknowledgment, files/static/live previews, reported 65-second dwell followed by fresh input, and input after cover/unfolded/book/tabletop transitions; screenshots and native geometry establish the actual tabletop layout |
| Historical hosted baseline | `967a057aed6d9570168b58e712c6d3197b2411a5`, iPhone 17 Pro/iOS 27.0, Xcode 27.1 build 27A9269: pairing, public Mosh/input, files/static/live preview, sample-photo upload/acknowledgment, reported 65-second dwell/fresh input and archive/Projects reopen. Predates policy/consent updates; creation was API-seeded |
| Native creation | Separate attended iPhone 17 Pro/iOS 27.0 run on `967a057` created Terminal, Coding agent/Project folder, and Coding agent/New worktree through native forms against a fresh local demo; archive/Projects reopen passed. That attended run failed exact rename fidelity; the fresh scoped 17 Pro lifecycle driver later passed exact rename. See [smoke evidence](DEMO_SMOKE.md#native-creation-follow-up-25-october-2026) |
| Audited unattended 17 Pro | Runner `df73ca2`, app `6461a2b`, light/iOS 27.0: 11 passing happy/lifecycle assertions, including post-relaunch input, single native terminal creation, exact rename, archive/exclusion and Projects reopen. All 35 referenced artifacts hash-matched; cleanup/backend stop passed. Background-state qualification is excluded |
| Unattended Duo | `6fc` passed through live/source/static previews before blocking at post-foreground AX. Fresh `9c78f83` blocked at the first normal TLS invitation/catalog assertion after about 41 seconds; no later assertions or poses ran. Each case cleaned up; fresh backend stopped. Full unattended Duo and native AX-guard qualification remain open |
| Reviewed AX guards | Runner/test-only `9c78f8382656ddcbebe38bdd7e236327f644e0ae` requires meaningful named native roots, guarded absence and saved SpringBoard/Trill identity proof; independently approved and integrated in `87386db`. The fresh native run blocked at pairing/catalog, so native guard qualification is unestablished; no app changes |
| Tooling / website checks | Integrated `87386db`: 302 Python tests passed, zero skips, including 148 smoke and 6 deployment tests; website npm check passed. The AX guard's 10 new focused regressions were independently approved. These checks do not execute the native app or establish website publication |
| Website publication | Policy source includes Optional hosted demo, effective 25 October 2026; support FAQs distinguish own-Mac storage from the shared demo and route demo deletion/revocation to support. Published and verified 25 October 2026 after `c7b9ff3`: both public pages returned HTTP 200 and matched the reviewed source byte for byte |
| Dated uploaded build | Saved ASC snapshots from 24 October 2026 23:34 UTC (25 October Sydney) show `2026.10.25 (9)`: processing `VALID`, not expired, internal `IN_BETA_TESTING`, external `READY_FOR_BETA_SUBMISSION` |
| Prepared unsigned device archive | Source `8665ef1d59be0063b614194214d34392004260a9`, version `2026.10.25`, local placeholder build `1`; arm64 Release archive succeeded with iOS SDK 27.1. Packaged policy and all eight manifest entries match source. Unsigned, not uploaded, and not a TestFlight build |
| Signed build / review status | A new signed TestFlight build containing policy and consent changes remains pending. ASC live state was not refreshed; build 9's snapshots do not establish current approval or availability |

These are dated simulator and synthetic-backend observations, not physical
TestFlight or full Xirp/Herdr/AI integration evidence. Earlier blocked automation
and the attended rename discrepancy remain recorded; the fresh exact rename
pass does not explain that earlier failure or establish an app defect. Completed
attended Duo checks do not rewrite the original manifests as passing runs. See
[smoke qualification and limits](DEMO_SMOKE.md#qualification-status-25-october-2026).
Frozen-runner background checks could accept unnamed or empty AX as departure
from Trill. Without retained positive foreground-application identity, older
elapsed waits and later successful input are not independently confirmed
background measurements; attended observations remain dated facts. The audited
`df73ca2` run retained no positive background-identity proof. Revision `9c78f838`
adds explicit identity receipts, but its fresh Duo run blocked before later
assertions; no native qualification of those guards or poses is claimed.
Build 9's saved encryption attribute is `usesNonExemptEncryption=false`; it is
not a fresh questionnaire decision. Keep the marketing version and use a fresh
actual upload build number for the revised app.

## App Store Connect field checklist

Use **Apps → Trill → TestFlight → Additional → Test Information**, then the
selected build's testing/review information. Apple requires beta test information
for external review; Beta App Description is required, and Feedback Email is
used for tester contact. Verify all requested fields and localizations in the
current UI. [Apple: provide test information](https://developer.apple.com/help/app-store-connect/test-a-beta-version/provide-test-information/).

| Field or decision | Prepared value / release-owner check |
| --- | --- |
| Beta App Description | Copy the description below; distinguish the product from the scripted review host |
| Feedback Email | `trill@bcook.io`; release owner must confirm inbox delivery/monitoring |
| Marketing / Privacy / Support URLs, where requested | `https://trill.bcook.io`, `https://trill.bcook.io/privacy`, `https://trill.bcook.io/support` |
| Beta App Review contact | Release owner supplies authorized first/last name, phone and email privately; a support URL is not contact information |
| Sign-in / demo account information | Access is required for the Safari reviewer page. Supply username `reviewer` and the private handoff password in the protected account fields; explain this browser login in Notes |
| Review Notes | Copy the notes below; do not include a pre-generated, expiring pairing link |
| What to Test | Copy the tester text below; do not put reviewer credentials in this tester-visible field |
| Selected build | Record actual marketing version, build number, source/tag, processing status and expiry; confirm it is eligible for external testing |
| Export compliance | Read the actual selected build's status and recorded questionnaire decision; do not change answers merely to clear a gate |
| Privacy information | Complete the policy publication/build and label reconciliation below; do not default to “Data Not Collected” |
| External tester group / notifications | Release owner selects tester scope, compatible devices and invitation method. Creation, submission, automatic notifications and invites remain separate release-owner actions |

An internal group is needed before an external group. An upload marked
**TestFlight Internal Only** cannot be used externally. The first submitted
external build receives full review; later same-version builds may still need
review. Check the actual status rather than assuming approval transfers.
[Apple: external testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers/).

## Copy: Beta App Description

Trill is a native iPhone and iPad companion for Xirp and Herdr on your own Mac.
Browse projects and sessions, connect to a real encrypted Mosh terminal, write
and deliberately submit terminal input, inspect project files and previews,
and upload selected photos after consent. Commands and coding agents run on
the paired computer. Provider capabilities differ: Herdr supports terminal
creation and rename; Xirp also supports coding-session/worktree creation and
archive/reopen/stop. Remote access uses your configured network, including
LAN or a user-configured VPN; VPN service is separate from Trill.

For review, a developer-operated demo provides real HTTPS and Mosh transport
with scripted sample sessions and generated media. It runs no Xirp, Herdr,
shell, coding agent or AI service. The hosted demo is a review fixture, not a
hosted coding service or evidence of full provider compatibility. Use only
synthetic input and photos; demo data is retained on the review host.

## Copy: Beta App Review Notes

Trill remotely controls sessions on the user's own Mac through the Trill
companion. The app renders terminal output locally and provides native session,
project, file and preview navigation. Actual shells and coding agents execute
on that Mac, under the user's provider configuration. LAN and user-configured
VPN access are supported. This version has no purchase, subscription, social
login, advertising or tracking flow.

Reviewer access: open https://trill-demo.fly.dev/review in Safari. Use username
reviewer and the password supplied in the review account fields. This is a
browser login to obtain a fresh pairing invitation, not an Apple Account or an
in-app signup. Select “Create pairing invitation”, then “Pair this iPhone with
Trill Demo”, or scan the displayed QR from another screen. Each invitation is
single-use and lasts ten minutes; generate a new one if needed. The review
computer is https://trill-demo.fly.dev. It uses normal publicly trusted HTTPS
and real Mosh on UDP 60300–60301. A network blocking UDP can prevent terminals;
contact trill@bcook.io for assistance rather than bypassing certificate checks.

The supplied host is explicitly synthetic. No real Herdr/Xirp, general shell,
coding agent or AI account runs there. It demonstrates the ordinary native UI,
consent, companion API, transport, files/uploads and preview paths without
accessing personal sessions. Capability labels such as Coding agent and New
worktree on this host create scripted fixtures, not an actual agent or Git
worktree. Production provider behavior is separate.

This developer-operated Fly demo receives and can retain pairing/device names,
session changes, terminal text and uploaded photos. It can be shared by
reviewers; use only test content. Demo data resets after 24 hours of global
inactivity; any paired reviewer's activity can extend retention for everyone.
Forgetting the computer on the phone does not delete server data. Contact trill@bcook.io for demo data deletion or
revocation. Privacy and support are linked before pairing and in Connection
details. No Mac setup is needed for this fixture. Quick walkthrough:

1. Pair from the Safari page, confirm the demo host, then open Welcome under
   Demo · Xirp and choose Open terminal / Allow once.
2. Use Write for `help`, tap Insert, then deliberately Return. Repeat with
   `demo heading Hello from review`.
3. In Files and browser, inspect README.md, media/sample.png and
   media/sample.mp4. Preview web/index.html. For live preview, run `demo links`
   and use its localhost URL, or enter http://localhost:18080/ in Preview address.
4. In Write, Add photo, approve and choose a sample image. Send `demo photo `
   followed by its quoted uploaded path using Insert then Return; inspect Photos.
5. New session → Demo · Xirp → Terminal → Create terminal creates a sample
   session. Rename if desired, Archive session… and confirm; find it under
   Projects and choose Reopen session. Coding agent/New worktree create scripted
   fixtures here, not actual AI or Git worktrees.
6. Disconnect and Forget computer when finished. Contact support for server
   deletion/revocation. Do not reset the shared review host.

## Copy: Reviewer walkthrough

These are reviewer instructions; the evidence table above defines completed
qualification and its limits. Video playback and physical-device behavior need
separate evidence. Physical checks are explicitly deferred to the release owner.

1. Pair through the Safari reviewer page above. Confirm the demo address in
   Trill, choose a generic device name, and tap Pair. Do not enter personal data.
2. In Sessions, choose **Welcome** in **Demo · Xirp**, then **Open terminal**.
   Read the terminal access sheet and choose **Allow once**. Remembered terminal
   and photo choices are separate; Connection details can reset them.
3. Open **Write** and enter `help`. Tap **Insert**: it inserts the draft without
   executing it. Press **Return** deliberately to run the restricted demo command.
   Repeat with `demo heading Hello from review` and observe the acknowledgment.
4. Open **Files and browser**. Browse `README.md`, `src/main.swift`, nested sample
   files and `media/sample.png`. Open `web/index.html` as source and as static
   preview; its heading should match. Open `media/sample.mp4` for video playback.
5. For the live preview, run `demo links` and use its localhost URL. Alternatively,
   enter `http://localhost:18080/` in **Preview address**, using **Browser options →
   Enter a web address** if the browser is showing a static file. The companion
   authenticates the preview; localhost refers to the remote fixture. Try About,
   Gallery, reload/back, and a new `demo heading Another review heading` command.
   Static file preview blocks scripts; the live fixture supports interactive updates.
6. In **Write**, choose **Add photo**, approve upload, and select a generated or
   non-sensitive sample image. Selection uploads immediately and inserts a quoted
   path in the draft. To acknowledge it, use `demo photo ` followed by that quoted
   path, tap Insert, then deliberately Return. Expect “Photo received” and inspect
   the image under **Photos**. Closing the draft does not delete the upload.
7. To exercise native creation, return to the catalog, choose **New session**,
   select **Demo · Xirp**, choose **Terminal**, then **Create terminal**. Rename
   the new sample session if desired. For the scripted coding-style flow, choose
   **Coding agent**, enter a harmless goal such as “Update the sample heading”,
   choose **Project folder** or **New worktree**, then **Start session** and review
   consent. On this host those choices create fixtures only. If the result is
   uncertain, use **Check Sessions** before deciding whether to try again.
8. Use a sample Xirp session's context menu to choose **Archive session…** and
   confirm. It should leave the current Sessions list. Find it under Projects,
   choose **Reopen session**, and choose Open terminal to reconnect. Herdr fixtures
   intentionally lack Xirp lifecycle actions; do not expect those options there.
9. Disconnect the terminal. Use Connection details to reset remembered consent
   if needed, then **Forget computer** to remove the phone's primary pairing.
   Request server revocation/deletion through support if required; do not reset
   the shared review host. If capacity is busy, disconnect owned terminals and
   contact support rather than taking over another reviewer's session.

## Copy: What to Test

Please test pairing, the terminal consent sheet, Write/Insert versus deliberate
Return, Unicode and multiline draft editing, file/image/video viewing, selected
sample-photo upload, static/live previews, session creation/rename/archive/reopen
where the provider supports them, and reconnect after backgrounding. Record the
actual TestFlight version/build, device/OS, provider and network with feedback.
Use only disposable sessions and synthetic media. Do not send pairing codes,
credentials, Mosh keys, terminal secrets or private photos in feedback captures.

The review host uses real TLS/Mosh with scripted sessions. It does not test
actual agent startup, Git worktrees or full Herdr/Xirp compatibility. Recorded
Duo and native-form passes use simulator source builds and synthetic backends;
physical-device and exact signed-build checks remain separate.

## Privacy publication, build and label readiness

The source privacy policy is now patched, effective **25 October 2026**, in
`ios/Trill/PrivacyPolicy.txt` and `website/privacy/index.html`. It qualifies
user-owned-computer connections and adds the optional Fly demo's retained data,
developer access, shared reviewer access, sample-only use, global idle reset
and support deletion. `APP_REVIEW.md` also calls for privacy reconciliation.
The previous policy revision was reviewed and published. On **25 October
2026**, after pushing `c7b9ff3`, both the public privacy and support pages returned
HTTP 200 and matched their reviewed source files byte for byte. The support FAQs
explain shared-demo access and provide the deletion/revocation contact.

That earlier policy revision is included in simulator build `6461a2b`. The later
hosting-diagnostics paragraph and collection-manifest entries are prepared for
the next build and are not in `6461a2b`. After `8665ef1` was pushed, the public privacy page returned HTTP 200 and
matched the new source byte for byte. A website update does not replace an
installed app's bundled policy or establish a signed TestFlight upload.

The manifest now includes the hosted-demo and support collection categories in
[Privacy declarations](PRIVACY_DECLARATIONS.md). All purposes are App Functionality,
with no tracking. Matching App Store Connect answers are prepared but have not
been entered or published; account access and upload were deferred by the owner.
Confirm the listed support and telemetry assumptions before publishing.

| Observed implementation | Required disclosure / question |
| --- | --- |
| `sidecar/demo.go`: durable transcript/model, goals, names and headings in `demo-state.json`; generated HTML reflects headings | Developer-operated hosting can retain readable user text beyond one request; describe it separately from user-owned Mac processing |
| `sidecar/pairing/store.go`: device label/ID, timestamps and credential hashes | Disclose stored pairing registration metadata; use generic device names; assess identifiers/linkage rather than assuming anonymous use |
| `sidecar/attachments.go` with demo attachment root | Selected JPEG/PNG uploads persist on the Fly volume; Photos browsing is not a deletion action |
| Shared process/sandbox with host-level paired access | Do not promise per-reviewer isolation or exclusive files; only synthetic data belongs in the shared host |
| Fly terminates HTTPS and hosts the VM/storage | Identify the hosting provider and assess platform request/security logs; application quiet logs do not establish no infrastructure retention |
| Runtime inactivity cleanup / `--retention 24h` | One global timer resets the sandbox data; authenticated API, reviewer-page and terminal IPC activity from any reviewer extends it. Phone Forget is not server erasure; there is no per-upload deletion deadline |

Apple's collection definition includes off-device data accessible beyond the
real-time request. Optional-disclosure criteria are cumulative; being optional
or using a demo is not enough. Assess retained free-form text as **Other User
Content**, uploaded images as **Photos or Videos**, and registration/device
identifiers and hosting diagnostics according to their actual use and linkage.
This is the code-based assessment to resolve with the privacy owner, not an
ASC answer already applied. Reconcile the final label and privacy-manifest
answers; absence of analytics/tracking does not imply absence of collection.
[Apple: privacy definitions and data types](https://developer.apple.com/app-store/app-privacy-details/).

Exact retention basis: `runtimeState.touch` updates one `lastActivity` value.
Authenticated public API and reviewer-page access, plus GET/POST terminal IPC,
refresh it. `expireIdle` checks once per minute, stops the runtime after the
configured interval and invokes `clearData`; startup also checks stale retention
metadata with `expireStored`. Data reset includes demo state, fixture changes,
pairing registrations and uploads, while ownership/lock/TLS housekeeping remains.
The separately mounted reviewer-page password is outside this reset. Deletion
errors fail closed; this is a configured inactivity policy, not a guaranteed
wall-clock deletion SLA or a statement about Fly infrastructure logs/backups.

The revised policy and short agent, terminal and photo disclosures are present
in simulator source build `6461a2b`. Four native consent tests passed with zero
skips. The attended in-place upgrade retained pairing/v1 state, showed both
renewed v2 disclosures, remembered terminal approval and left photo approval
false after Cancel. New-v2 photo approval/upload was not exercised. Terminal
approval does not grant photo approval. These results close the simulator
consent checks, not signed-build qualification. A **new signed TestFlight build**
is still required, retaining `2026.10.25` and using a fresh actual upload build
number. The manifest and hosting-retention assessment are prepared; App Store Connect
publication and the account-side checks in Privacy declarations remain open.
This packet changes no export-compliance answer.

## Product scope for review

Describe actual user-owned Mac execution, local terminal rendering, LAN/VPN
access and the hosted fixture honestly. The hosted demo is not full provider
integration evidence or a promise of App Review acceptance. Answer Apple's
questions with the actual architecture and evidence.
[Apple's current review guidelines](https://developer.apple.com/app-store/review/guidelines/#minimum-functionality).

Use dated signed-archive, upload, toolchain and device evidence for release
status. Unchecked historical signing/archive boxes do not establish that no
signed build exists; simulator compilation does not establish an upload.

## Remaining external-beta gates

1. Produce and verify a new signed TestFlight build with the reviewed policy and
   consent changes. Keep `2026.10.25`; record source/tag, actual signed-export
   build number, processing, external eligibility (not Internal Only), expiry
   and encryption outcome. Build 9 is dated upload evidence, not this candidate.
2. Confirm the account-side assumptions and apply the prepared
   [privacy answers](PRIVACY_DECLARATIONS.md) in App Store Connect. The manifest
   now declares collection; that does not publish the store's privacy labels.
   The updated hosting-diagnostics paragraph was also verified live after
   `8665ef1`; this does not apply the separate App Store Connect answers.
3. Physical-device checks are **pending with the release owner, explicitly
   deferred**. Record the exact TestFlight build, device and results when run.
   Native video playback, minimum OS/iPad, real providers, camera QR/dictation,
   accessibility, network transitions and IPv6/NAT64 need their own evidence;
   attended Duo geometry and synthetic native creation do not establish them.
   Full unattended Duo and native AX-guard qualification remain open after the
   fresh pairing/catalog block recorded above.
4. Confirm review contact, support inbox, hosted availability and private reviewer
   credential validity. The release owner completes metadata and selects tester
   scope; submission, invitations and notifications require their own authorized
   execution. Apple determines the beta review outcome. This packet neither
   submits nor declares the build externally available.
