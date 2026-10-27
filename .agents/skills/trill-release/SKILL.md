---
name: trill-release
description: Prepare and ship Trill iOS builds through Xcode Cloud and TestFlight, preserving the marketing version for small updates. Use for Trill release planning, validation, archive or distribution requests, not routine app edits or website-only deployment.
---

# Trill release

Use the Trill repository’s [release procedure](../../../docs/RELEASING.md) and [development checks](../../../docs/DEVELOPMENT.md). Resolve these paths relative to this skill in the checkout. Keep this canonical skill in `.agents/skills/trill-release`; a user-level installation may link to it.

## Choose the release intent

Inspect the working tree, current `ios/Version.xcconfig`, intended commit and existing tags. Run `python3 scripts/release.py plan`. A local tag or this skill does not establish external approval or Cloud configuration.

For a small fix or polish update, **retain the marketing version** and produce a new build. Xcode Cloud increments the build number; the local placeholder stays at 1. Start a new CalVer marketing train only when the user requests it or the release scope requires it. Explain the choice. Use `scripts/release.py set-version YYYY.MM.DD` for an intentional version change, with a valid calendar date and zero-padded month and day. The date names the release train; later small updates keep it. Never promise to bypass Apple review: later builds of a reviewed version may still require it.

Use one immutable `vVERSION-build.COMMIT12` tag for both iOS and the Mac companion, not force-moved or separate companion tags. Both builders read the marketing version from `ios/Version.xcconfig`; the companion workflow signs/notarizes its artifacts and creates the GitHub release under this same tag. For a combined release, include Go/Python checks and companion packaging validation in the preparation. GitHub runs validation once on main; the companion release workflow waits for the successful Checks run at the exact tagged commit instead of repeating its tests. Same-version builds on new commits get new tags. For manual uploads, confirm an unused build number and reconcile later Cloud numbering. Never overwrite an archive or reuse an uploaded build number.

Routine commit-and-release requests end after pushing the verified commit and its new immutable release tag. Do not open Xcode or App Store Connect, inspect Cloud/TestFlight release status, poll builds or wait for archive, upload processing or tester availability unless the user explicitly asks to monitor or troubleshoot the release. Report the commit, tag and completed validation; do not infer beta availability from a tag push.

## Prepare a reviewable release

Follow `docs/RELEASING.md` and `docs/APP_REVIEW.md`; inspect actual code and check results. Run Go/Python checks and the native iOS tests on an isolated simulator with the required SDK. Verify current privacy/export-compliance answers, authentication and companion compatibility. Scan publishable source/history as appropriate and review public media. For major visible changes, invoke `$trill-marketing-upkeep` and repair stale capture instructions in that skill when needed.

Never attach to, type into, stop, steal, archive or replace personal sessions, restart the production companion, or drive a personal simulator to validate a release. Use the repository-owned lab and its ownership checks. Missing access to a physical phone is a stated validation gap, not permission to claim a passed device test.

Before distribution, identify the exact commit, marketing version, proposed tag, test results, material limitations and intended tester group. Complete all authorized preparation before requesting any missing release authorization. Existing authorization to ship the specific release is sufficient; ordinary permission to commit/push app or website changes does not imply starting TestFlight distribution.

## Manage Cloud configuration

Keep automatic Cloud tests limited to pushes to `main`; do not enable automatic or manual PR start conditions. GitHub checks likewise run only on `main` in the upstream repository. Verify all-external-contributor fork workflow approval after the repository becomes public; GitHub does not allow setting it while private. Review incoming build scripts and workflow changes before merging.

Use `scripts/xcode-cloud.swift` with credentials injected by the secret manager to inspect, create or update workflows. Read the live product/workflows first; save a private before-state and verify every write with a fresh GET. The CLI intentionally cannot start builds or delete workflows. Do not retry an ambiguous write until readback establishes the result.

For a Cloud product without an App Store Connect record, Xcode’s Reports → Cloud → product context menu → Set Up Distribution can create it. Inspect any starter workflow Xcode adds before adapting it. App-record and workflow configuration alone do not authorize clicking Start Build. Cloud tag filters support prefix/exact matching; use the `v` prefix with the existing `ci_post_clone` hook enforcing `vVERSION-build.COMMIT12`, rather than treating a glob string as a supported filter.

Current CI uses iPhone Duo/iOS 27.1 and iPhone 17 Pro/iOS 27.0; Cloud rejected the 17 Pro/27.1 combination despite listing it.

## Publish a release

Before archiving, confirm the outcome of Apple’s encryption questionnaire. The initial internal beta received “No documentation required” for standard third-party AES-128/OCB, no proprietary algorithm, and no French App Store distribution; The Herdr-only iOS client now uses system HTTPS/WSS; the removed transport’s AES/OCB implementation is no longer linked. Retain the existing NO flag for the established internal-beta scope; reassess current implementation and questionnaire requirements when encryption or availability changes, especially before French distribution. Do not confuse encryption use with a requirement to upload documentation. If documentation is required, use Apple’s approved code. An absent declaration API record can be normal for a no-documentation result.

For a generic Cloud upload-preparation failure, validate the exact signed App Store IPA with Apple’s CLI and inspect the concrete error before changing code. Verify version/build in the exported IPA, not only the raw archive; Cloud updates the export’s build number. Keep authentication and diagnostic logs private.


Only after release distribution is authorized, create/push the exact new tag from a clean verified commit or use the documented signed local archive fallback. Never change global Xcode selection or commit a signing team/key. Cloud must offer the required SDK; do not silently downgrade to make a build pass. Cloud setup is a separate task; routine releases use the existing workflow.

After pushing the commit and tag, stop. Monitor archive/upload progress, build numbers or TestFlight availability only when explicitly requested. For an explicitly requested external promotion, perform the real-device release check and honor Apple’s review requirements and the approved tester scope.

If the workflow, scripts, version source, provider behavior or required release gates drift, fix the relevant repository documentation and this skill in the same work. Validate the skill after edits. Keep factual release evidence in release notes/handoffs rather than accumulating personal-path work logs here.
