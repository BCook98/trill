# Privacy declarations

Published by the owner on 25 October 2026 and verified in App Store Connect.
The nine categories match `ios/Trill/PrivacyInfo.xcprivacy` at commit `c564d92`:
App Functionality only, no tracking, Performance Data unlinked, and the other
eight categories linked. Both privacy URLs are saved.

These declarations are ready for the next build on marketing version
**2026.10.25**. Publishing the label does not upload or distribute a new build.

## Published answers

Select **Yes, we collect data from this app**. The optional developer-operated
hosted demo stores data beyond the request that supplied it. Ordinary connections
to a user's own companion do not pass through that hosted service.

For every category below, select **App Functionality** as the purpose and
**No** for tracking. There is no advertising or behavioral analytics SDK in the
app. Aggregate service-performance measurements support uptime, not marketing.

| App Store Connect category | Manifest suffix after `NSPrivacyCollectedDataType` | Linked to the user | What is retained |
| --- | --- | --- | --- |
| Photos or Videos | `PhotosorVideos` | Yes | Selected photos uploaded to the hosted demo; images are re-encoded without EXIF but their visible content is not anonymized |
| Other User Content | `OtherUserContent` | Yes | Device labels, session names and goals, submitted terminal text, headings and generated files |
| Device ID | `DeviceID` | Yes | Random per-pairing device registration identifiers and credential hashes associated with device labels; not IDFA or a hardware identifier |
| Product Interaction | `ProductInteraction` | Yes | Session creation/lifecycle state, task progression, update timestamps and terminal dimensions needed to restore and display sessions |
| Other Diagnostic Data | `OtherDiagnosticData` | Yes | Service troubleshooting records, including Fly proxy error paths that can contain session, lease or preview identifiers |
| Performance Data | `PerformanceData` | No | Default aggregate Fly infrastructure timings and metrics, labeled by app/host/region rather than paired phone |
| Crash Data | `CrashData` | Yes | Individual Apple crash reports used to diagnose and fix crashes; reports can include device identifiers or tester details |
| Email Address | `EmailAddress` | Yes | Sender addresses when a user contacts support |
| Customer Support | `CustomerSupport` | Yes | Support messages and attachments intentionally sent by the user |

The manifest sets tracking to false for every entry and keeps tracking domains
empty. The existing UserDefaults, system-boot-time and file-timestamp
required-reason declarations are unchanged.

Use `https://trill.bcook.io/privacy` for the Privacy Policy URL. The optional
Privacy Choices URL can be `https://trill.bcook.io/support`, which describes
revocation and deletion requests.

## Basis for the answers

The demo stores transcripts and session state in `sidecar/demo.go`, generated
files in `sidecar/demo_fixtures.go`, registrations in `sidecar/pairing/store.go`,
and photos through `sidecar/attachments.go`. Authenticated requests and live
leases/preview grants associate device registrations with sessions. Not every
stored content record has an author field, but the service does not anonymize
the content before collection. Shared storage and random identifiers do not
justify an unlinked declaration. Trill does not require an app account, but
pairing registrations and tester contact details can still link data to a device
or person.

Generic text fields belong under Other User Content; they do not require every
possible sensitive category a user might type. Photo uploads have their own
category. Trill's camera scans pairing codes and does not upload camera frames;
system-keyboard dictation supplies text, not microphone recordings, to Trill.
User-directed browsing of the open web is separate from the hosted demo.

Support uses an ordinary `mailto:` link with no automatically attached logs.
Email Address and Customer Support are included rather than relying on Apple's
optional-support exception. The policy describes correspondence retained to
answer requests and maintain support records. The owner confirmed that email
is not used for tracking. Confirm the remaining support handling before
publishing these answers.

Apple defines collection by off-device retention, including collection solely
for app functionality. Optional features and an initial consent prompt do not
exempt ongoing storage. Linked-data answers include associations through a
device, not just a named account.
[Apple's privacy definitions](https://developer.apple.com/app-store/app-privacy-details/)
and [manifest structure](https://developer.apple.com/documentation/technotes/tn3184-adding-data-collection-details-to-your-privacy-manifest).

## Apple crash reports

For Apple-provided diagnostics, the owner confirmed individual crash reports
only. The inspected app has no third-party crash-reporting SDK. Crash Data uses
App Functionality to cover diagnosing and fixing crashes; no Analytics purpose
is inferred.

Individual crash reports can contain a per-device CrashReporter Key or a
TestFlight Beta Identifier. TestFlight crash feedback can also include tester
contact details and comments. Xcode Organizer can expose individual reports, so
grouping reports by crash does not establish that they are aggregate-only.
The manifest and App Store Connect answers declare Crash Data as linked,
matching the owner's confirmed use of individual reports.
[Apple's crash-report fields](https://developer.apple.com/documentation/xcode/examining-the-fields-in-a-crash-report)
and [TestFlight feedback](https://developer.apple.com/help/app-store-connect/test-a-beta-version/view-tester-feedback/).

Apple distinguishes data the developer obtains from its services from data
Apple collects for itself. The crash-report declaration covers the developer's
use and does not claim responsibility for all of Apple's collection.

## Hosting and retention

The demo's global 24-hour inactivity timer resets stored fixtures, session data,
registrations and uploads. Any tester's activity or an active terminal can
extend it. Forgetting the computer does not erase server data. Scheduled volume
snapshots are disabled in the checked-in Fly configuration.

Fly documents seven-day application stdout/stderr retention, proxy error
records containing app ID, hostname and request path, and about 15 days of
metrics. It says inbound client IP addresses are not retained in its log store;
that is not a claim that IPs are never processed. These operational records are
separate from the demo reset. Trill's demo HTTP error loggers discard output,
and the inspected code has no access-log database, crash SDK or log exporter.
[Fly logging and retention](https://fly.io/legal/security-questionnaire/#compliance-12)
· [default metrics](https://fly.io/docs/monitoring/metrics/).

## When practices change or a build ships

1. Confirm no additional log shipping, custom telemetry, identifier joins or
   retained copies have been enabled outside this checkout. Revisit linkage if
   infrastructure metrics are joined with device identifiers.
2. Confirm the remaining support handling. Update these answers if the developer
   uses Apple diagnostics beyond the confirmed individual crash reports.
3. Keep the published categories, purposes, linkage and tracking answers in
   App Store Connect consistent with the manifest and privacy policy.
   A bundled manifest does not populate these answers automatically.
4. Refresh actual build numbers and Cloud configuration before releasing. Keep
   marketing version `2026.10.25` and use a fresh build number. The locally
   prepared unsigned archive is not an uploadable or distributed TestFlight build.

[Apple's privacy-answer workflow](https://developer.apple.com/help/app-store-connect/manage-app-information/manage-app-privacy/).
