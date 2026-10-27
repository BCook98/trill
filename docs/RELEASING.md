# Releasing Trill

Trill uses Xcode Cloud and TestFlight: build and test the shared scheme, archive with a pinned Xcode version, distribute internally, verify on a real phone, then promote to external testers. The repository supplies version configuration and a Cloud preflight hook. The upstream project has an App Store Connect record and separate CI and Release workflows. Forks need their own account setup. A configured workflow does not mean a build has been uploaded or tested.

For routine release requests, finish after pushing the verified commit and its new release tag. Do not open Xcode or App Store Connect, poll Cloud/TestFlight status, or wait for archive, processing or tester availability unless the user explicitly asks to monitor or troubleshoot the release. Report the published tag and completed validation without claiming that the beta is available.

The macOS Go companion has a separate [signing, notarization and update workflow](COMPANION_RELEASES.md). This guide and the Xcode project concern the iOS app.

## Versions and repeat builds

`ios/Version.xcconfig` is the shared source for the app’s marketing version and local build number. The checked-in Xcode project and generator both reference it.

- Small fixes and polish keep the current `MARKETING_VERSION` (currently `2026.10.25`). A new binary gets a new build number, not a new marketing version.
- Xcode Cloud assigns an increasing build number at archive time. Keep the local placeholder at `1`. Local manual uploads must choose an unused build number above prior uploads, including Cloud builds.
- A deliberately new release train uses zero-padded CalVer `YYYY.MM.DD` (for example, `2026.07.12`). It is the train’s start date, not the date of every later build. Use `python3 scripts/release.py set-version 2026.10.25` only when starting that train, then commit the change. Do not automatically bump the marketing version on every release request.
- Each build uses a new immutable tag `v<marketing-version>-build.<commit12>`. This preserves a stable source reference while allowing repeated builds of the same marketing version. Rerun a failed Cloud build through Cloud; never force-move an existing tag. The suffix is a source identifier, **not** the uploaded build number.
- App Store releases are separate from beta distribution. An already released App Store version cannot be replaced as a new store release simply by incrementing its build number.

Apple says the first external TestFlight build requires full review and later builds of the same version **might not**. Keeping the version does not guarantee review avoidance. Always follow the status and required actions in App Store Connect. [External testing](https://developer.apple.com/help/app-store-connect/test-a-beta-version/invite-external-testers/) · [Cloud build numbering](https://developer.apple.com/documentation/xcode/setting-the-next-build-number-for-xcode-cloud-builds/)

## One-time Xcode Cloud setup

Use `ios/Trill.xcodeproj`, shared scheme `Trill`, bundle ID `io.bcook.trill`. The upstream workflows pin Xcode 27.1 (27A9275) and macOS 27.0.1 (26A434). Change these only after validating a later version with the Duo SDK. The minimum deployment target is iOS 18.0; that is not the compiler requirement. Cloud must actually offer the required toolchain before it can build this project. Otherwise use the documented local build/test commands and a signed local archive; do not silently downgrade the SDK.

| Workflow | Start condition | Actions |
| --- | --- | --- |
| CI | Push to `main` only | Build and test on iPhone Duo (iOS 27.1) and iPhone 17 Pro (iOS 27.0) |
| Release | Tags beginning with `v`; preflight enforces `vVERSION-build.COMMIT12` | Archive for TestFlight and App Store; internal testing before external promotion |

For a new installation, connect the GitHub repository, select the owning Apple team in Cloud, and verify provisioning. No team is committed in the shared project. The upstream Release workflow distributes to the `Internal` tester group through a TestFlight Internal Testing post-action. New installations must create/select their own internal group; external distribution remains a separate step. Do not mark a release intended for eventual external testing as **TestFlight Internal Only**. The upstream Release workflow restricts editing to the Account Holder, Admins and App Managers, uses clean builds, and permits manual starts from tags only. The preflight hook rejects tags that do not identify the configured version and exact commit. Configure external promotion manually after the device check.

`ios/ci_scripts/ci_post_clone.sh` validates the version and, for tagged builds, matches the tag to the version and checked-out commit. It does not install arbitrary dependencies, sign, upload, or override Cloud’s build number. Configure build/test/archive actions in App Store Connect; checking in a hook alone does not create a workflow. [Cloud setup](https://developer.apple.com/documentation/xcode/configuring-your-first-xcode-cloud-workflow)

Pull requests do not start Xcode Cloud or GitHub Actions jobs. GitHub checks accept only pushes to `main` and maintainer dispatches on `main` in this repository. Workflow tokens have read-only permissions, cannot approve PRs, and are not persisted by checkout. Review incoming code and workflow/build-script changes before merging: merging makes that code eligible to execute in CI.

Before making the repository public, set **Settings → Actions → General → Approval for running fork pull request workflows → Require approval for all external contributors**. GitHub rejects this setting while the repository is private, so verify it immediately after a visibility change. Keep PR triggers disabled; do not add `pull_request_target`, comment-triggered builds, or a job that checks out and runs an unreviewed PR head with trusted credentials. Do not make PR checks required when they intentionally never run.

## Manage workflows from the CLI

`scripts/xcode-cloud.swift` reads App Store Connect resources and creates or updates Cloud workflows. It requires macOS, Swift from Xcode, and an App Store Connect API key with access to the app and workflow configuration. It cannot start builds, delete workflows, or upload apps.

Provide `ASC_KEY_ID`, `ASC_ISSUER_ID`, and `ASC_KEY_PEM` (or `ASC_KEY_PATH`) through your secret manager. With 1Password, set `TRILL_ASC_ENV` to a private env file containing `op://` references, then run:

```sh
op run --env-file="$TRILL_ASC_ENV" -- \
  swift scripts/xcode-cloud.swift GET /v1/ciProducts

# Save a reviewed request body outside Git before applying it.
op run --env-file="$TRILL_ASC_ENV" -- \
  swift scripts/xcode-cloud.swift PATCH "/v1/ciWorkflows/$TRILL_WORKFLOW_ID" .local/workflow-update.json
```

Use an API key with the permissions required for the intended workflow change, or the signed-in Xcode account.

For a new workflow, use `POST /v1/ciWorkflows` with a create-request JSON body. Read the existing workflows first to avoid duplicates. Save a private before-state, apply the reviewed change, then GET the workflow to verify it. If a write times out, read back the state before retrying. Keep credentials, tokens and API response snapshots out of Git. [Apple’s workflow API](https://developer.apple.com/documentation/appstoreconnectapi/workflows)

To create the app record through Xcode, open **Reports → Cloud**, Control-click **Trill**, and choose **Set Up Distribution**. Verify the team, name, bundle ID, SKU and primary language, then create the record. Xcode may add a starter distribution workflow; inspect and adapt it rather than creating a duplicate. Close the assistant before **Start Build** when only configuring the app. [Apple’s distribution setup](https://developer.apple.com/documentation/xcode/distributing-your-xcode-cloud-builds-through-testflight)

## Release procedure

1. Review the intended changes and select **same marketing version** unless a new train is explicitly warranted. Run `python3 scripts/release.py plan` to inspect version, source commit, dirty state and proposed tag. This command is read-only.
2. Run the [development checks](DEVELOPMENT.md) and review [publication validation](VALIDATION.md). Build/test from a clean checkout. Do not install the companion over a personal setup to test a release.
3. Check companion/iOS API compatibility, provider capabilities and terminal reconnection. Any terminal input or creation test belongs in an owned isolated lab. The production companion and personal sessions are not release fixtures.
4. When preparing an App Review or external TestFlight submission, finish [App Review preparation](APP_REVIEW.md) and the [external TestFlight review packet](TESTFLIGHT_REVIEW.md): privacy manifest and answers, encryption/export compliance, support/privacy URLs, reviewer access to a disposable host, remote-control positioning, and real-device checks.
5. For material UI changes, run [$trill-marketing-upkeep](../.agents/skills/trill-marketing-upkeep/SKILL.md); inspect actual videos/screenshots and update claims. A documentation-only change does not require rerecording unchanged UI.
6. Complete the relevant local checks, commit, ensure the working tree is clean, then obtain a fresh plan. When authorized to distribute, create and push **only** the displayed new tag, for example `git tag -a "$TRILL_RELEASE_TAG" -m "Trill TestFlight build"` and `git push origin "$TRILL_RELEASE_TAG"`. Set `TRILL_RELEASE_TAG` to the exact reviewed plan value. A regular `main` push deploys the website; it does not itself request a TestFlight release under this strategy.
7. Stop after publishing the commit and new tag. Check Cloud runs, archive/upload processing, build numbers or internal availability only when the user explicitly requests release monitoring or troubleshooting.
8. Before an explicitly requested external promotion, install that exact build on a real phone. Check pairing/revocation, terminals/input/dictation, background recovery, session management, files, photos and previews using disposable data. Record any untested minimum-OS, Duo or roaming coverage. Promote to external testers only after this check and any Apple review requirement.
9. Record the immutable tag, commit, marketing version, completed tests and validation limitations in the release handoff. Include Cloud run links, uploaded build numbers or distribution status only when monitoring was requested and those facts were observed. TestFlight builds expire after 90 days; replace an expiring build intentionally.

## Encryption answers and upload validation

Use the actual result of Apple’s encryption questionnaire. `ITSAppUsesNonExemptEncryption` describes whether documentation is required, not whether the app encrypts data at all. The owner’s initial internal-beta answers were standard third-party AES-128/OCB, no proprietary cryptography, and no French App Store distribution. Apple returned “No documentation required,” so the app sets the key to NO. Review this decision again if cryptography or distribution changes; French store availability has not been cleared by this beta workflow. Applicable government reporting obligations require separate review.

When Apple requires documentation, keep the key YES and use the approved `ITSEncryptionExportComplianceCode` supplied by Apple. Never invent a code or change the flag merely to suppress an upload error. A no-documentation outcome may leave no declaration record in the API.

Cloud can archive and sign successfully yet report only “Preparing build for App Store Connect failed.” Download the App Store export and validate that exact IPA with `xcrun altool --validate-app` to obtain the underlying error. Inject credentials through the secret manager; keep logs private. Check the exported IPA’s version/build: the raw Cloud archive may retain the local build placeholder while the signed export has Cloud’s actual build number.

[Apple’s encryption key definition](https://developer.apple.com/documentation/bundleresources/information-property-list/itsappusesnonexemptencryption) · [Questionnaire and documentation](https://developer.apple.com/help/app-store-connect/manage-app-information/determine-and-upload-app-encryption-documentation/)

## Local archive fallback

Use an explicitly selected SDK and your own signing team; credentials stay in Xcode/Keychain or the CI secret store. For example, after completing tests:

```sh
# Set DEVELOPER_DIR to your installed Xcode 27.1+ developer directory.
# Set TRILL_SIGNING_TEAM and TRILL_BUILD_NUMBER to your own team and an unused build number.
xcodebuild -project ios/Trill.xcodeproj -scheme Trill \
  -configuration Release -destination 'generic/platform=iOS' \
  -archivePath .build/release/Trill.xcarchive \
  -skipPackagePluginValidation \
  DEVELOPMENT_TEAM="$TRILL_SIGNING_TEAM" CURRENT_PROJECT_VERSION="$TRILL_BUILD_NUMBER" archive
```

Inspect the archive’s `Info.plist`, privacy report and actual version/build before using Xcode Organizer to validate and distribute it. An archive is not an upload. Provisioning/export settings vary by account; do not check signing credentials or an account-specific export file into Git. Ensure subsequent Cloud numbering cannot collide with manual uploads.
