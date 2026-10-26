# App Store submission preparation

Prepared 24 October 2026. This is a working release document, not a claim of Apple approval. Do not paste incomplete checklist entries into Notes for Review.

For the hosted reviewer sandbox and current external-beta preparation, use the [TestFlight review packet](TESTFLIGHT_REVIEW.md). The dated checklist below is not a live App Store Connect status report; verify signing, build availability and review state before release.

## Identity and public information

- App bundle identifier: `io.bcook.trill`
- Unit/UI test identifiers: `io.bcook.trill.tests`, `io.bcook.trill.uitests`
- Public website: https://trill.bcook.io
- Privacy policy: https://trill.bcook.io/privacy
- Support: https://trill.bcook.io/support
- Contact: trill@bcook.io

The identity change installs as a separate app from `dev.trill.ios`; re-pair on existing test devices. It does not migrate the previous app’s Keychain access or App Store record. Register the new App ID/provisioning profile with the selected signing team and use a matching App Store Connect record. The `trill://pair` URL scheme is unchanged.

## Public positioning

Lead with “Remote control for Xirp and Herdr.” Explain that sessions, commands, and coding agents execute on the user’s own Mac; the iPhone provides native navigation, terminal display/input, and previews. Describe supported actions and provider differences accurately. The website, metadata, and review notes must describe the same product.

This wording is not a compliance exemption. [Guideline 4.2.7](https://developer.apple.com/app-store/review/guidelines/#minimum-functionality), checked 24 October 2026, includes LAN and host-rendering conditions. Trill renders a terminal locally and supports user-configured VPN access; keep both facts in the review inquiry and resolve their treatment with Apple before submission.

## Draft Notes for Review

Trill is a native iPhone/iPad remote control for Xirp and Herdr on a user-owned Mac. SwiftTerm renders a real terminal locally; encrypted Mosh connects through the Trill companion to Xirp’s tmux sessions or Herdr’s existing terminals. Shells and coding agents execute on the Mac. The app supports standalone shell sessions and Xirp coding-agent sessions, project browsing, session creation, rename, archive, reopen, and confirmed stop. It does not download executable iOS code or sell access to hosted agents. No purchase, subscription, social login, advertising, or tracking flow is implemented in this version.

A Mac running Xirp or Herdr and the Trill companion is required. Both providers can be enabled on the same Mac. Herdr supports terminal attachment, rename, new terminal tabs, spaces, and Git worktrees. Coding-agent launch and archive/reopen/stop remain Xirp actions; agents can also be started inside Herdr terminals. Local LAN connections and user-configured VPN/Tailscale access are implemented; VPN service is external to Trill. Trill is an independent project, not affiliated with Spotify.

Privacy and support are available from the welcome screen before pairing and from Connection details after pairing. The policy is also bundled for offline reading. Permission is requested before opening a terminal, starting a coding session, and selecting photos for upload. The terminal sheet offers Allow once or a remembered choice scoped to the current computer address, credential, and pinned certificate. Connection details can reset it; forgetting or pairing the computer again clears it. Photo upload approval is remembered separately for that pairing and can be reset in Connection details. Neither remembered choice bypasses companion read-only restrictions, certificate validation, or coding-session prompts. Agents run under the user’s configuration on the Mac and may send input and project content to the configured third-party AI provider. Photos selected after confirmation upload immediately to the connected computer; sending their paths to an agent may cause onward provider sharing. Connections to the user’s own computer do not pass through a developer-operated relay. The optional hosted demo is a developer-operated, shared sandbox that receives and stores demo content; its policy explains shared access, retention, and deletion. Use sample content only.

Pairing invitations expire after ten minutes and are single-use. Production security remains unchanged for review. The companion starts read-only; enable control on the dedicated review Mac with `trill control --enable` before review. Use an isolated project and tmux session; never provide a personal working environment or production credential.

Reviewer walkthrough:
1. Review the supplied physical-device video and the agreed Mac/network access arrangement.
2. Choose Pair a computer. Scan a fresh invitation or paste the supplied link; review the host and tap Pair.
3. Open Sessions, select the isolated review session, then Open terminal. Review and accept the sharing sheet. Demonstrate a harmless command in the isolated shell.
4. Open Write, enter a draft, and Insert. No Return is appended automatically. Press Return deliberately to submit.
5. Demonstrate session/project browsing and lifecycle controls in the isolated project. Demonstrate coding-agent actions only with a disposable provider account and test files.
6. Add photo, review the upload disclosure, select a non-sensitive test image, and inspect the resulting path in the draft. Closing the draft does not delete the uploaded image.
7. Disconnect and confirm that tmux still runs on the Mac. Forget computer clears the primary phone credential; revoke the device separately on the Mac after review.

Before submitting, add: exact tested devices/OS versions, app/build version, review video URL, agreed reviewer access instructions, external agent/provider configuration used for review, and availability dates/contact details. Do not put live secrets in this repository.

## Draft App Review inquiry: 4.2.7

Trill is a native remote control for Xirp and Herdr on a user-owned Mac. It supports standalone shells as well as coding-agent sessions. Commands and coding agents execute on the Mac; Trill receives a Mosh terminal stream and uses native project/session navigation. It is not a cloud-hosted app catalog or a mirrored graphical desktop. It supports LAN access and user-configured VPN/Tailscale access outside the LAN.

Does Apple consider this architecture a terminal/developer tool, or a client mirroring specific software under guideline 4.2.7? If 4.2.7 applies, how should its LAN restriction be applied to this user-owned-host terminal architecture? We can provide a physical-device video, technical details, and an isolated review environment.

Send through the developer’s App Review channel or discuss at a one-on-one appointment. This draft has not been sent; Apple’s response remains required to resolve the interpretation. Do not hide remote access or relabel the implemented behavior to evade review.

## Privacy manifest and label audit

`PrivacyInfo.xcprivacy` is copied into the app bundle. Current reasons:

| Category | Reason | Source/use |
| --- | --- | --- |
| UserDefaults | CA92.1 | AppStorage terminal font preference, confined to app defaults |
| SystemBootTime | 35F9.1 | Mosh elapsed timing; vendored clock removes device-boot origin before transmission |
| FileTimestamp | C617.1 | SwiftTerm fstat for file mode/size in its Kitty image readers; iOS sandbox constrains accessible files |

SwiftTerm’s macOS-only UserDefaults access does not run on iOS. Its PNG/LZ77 portable-graphics dependencies are not linked for iOS. Re-audit with every dependency update and inspect the final archive’s privacy report. The manifest now declares the optional hosted demo and support data described in [Privacy declarations](PRIVACY_DECLARATIONS.md), with App Functionality as the purpose and no tracking. App Store Connect answers are prepared but have not been entered or published. That is not an automatic App Store privacy-label determination: evaluate the developer-operated hosted demo (including stored user content and device registrations), user-controlled host processing, configured AI-provider sharing, support correspondence, any optional Apple diagnostics, and website hosting against Apple’s definitions before completing App Store Connect. Apply the prepared answers in App Store Connect and confirm the listed account-side assumptions before external distribution; do not claim “Data Not Collected”.

## Release gates still requiring evidence

- [ ] Verify that trill@bcook.io receives and can answer support/privacy messages. Website URLs and policy alignment are checked separately below; inbox delivery has not been tested by sending mail.
- [ ] Register/sign the new bundle identifier and validate a Release archive.
- [ ] Build with an App Store-supported release Xcode/SDK; the current local toolchain has been described as beta in prior validation.
- [ ] Resolve/document the 4.2.7 interpretation with Apple.
- [ ] Agree reviewer access; provide a physical Apple-device video showing actual pairing and interaction with the Mac. Avoid expiring credentials before review starts.
- [ ] Complete physical-device LAN/VPN, Wi-Fi/cellular handover, long suspension, process termination, hardware keyboard, VoiceOver, large text, battery, and sustained-output testing.
- [ ] Verify IPv6-only/DNS64/NAT64 networking through API pairing and Mosh, rather than assuming Network.framework alone proves it.
- [ ] Verify ordinary iPhone sizes, iPad, minimum supported iOS 18, and Duo layouts/reserved regions.
- [ ] Exercise coding-agent creation/worktree/root, ended-session resume, and photo sharing end-to-end in a disposable project.
- [ ] Recheck encryption classification before public distribution or changes to cryptography/availability. For the initial internal beta on 25 October 2026, the owner completed Apple’s questionnaire with standard third-party AES-128/OCB encryption, no proprietary algorithm, and no French App Store distribution. Apple reported “No documentation required.” `ITSAppUsesNonExemptEncryption` is therefore NO for this scope; this does not mean Trill has no encryption. Reassess French availability before enabling it, and review any applicable reporting obligations separately.
- [ ] Complete privacy labels, accurate age rating, screenshots, description, availability, and review metadata.

Sources checked:
- [App Review Guidelines](https://developer.apple.com/app-store/review/guidelines/)
- [Complete review information and physical hardware video](https://developer.apple.com/help/app-review/before-submitting-for-review/complete-review)
- [Required-reason API declarations](https://developer.apple.com/documentation/technotes/tn3183-adding-required-reason-api-entries-to-your-privacy-manifest)
- [Required-reason categories and permitted reasons](https://developer.apple.com/documentation/bundleresources/app-privacy-configuration/nsprivacyaccessedapitypes/nsprivacyaccessedapitype)
- [Encryption documentation](https://developer.apple.com/help/app-store-connect/manage-app-information/determine-and-upload-app-encryption-documentation)
- [Supported upload toolchains](https://developer.apple.com/help/app-store-connect/release-notes/)

## Deployment status

The permanent domain https://trill.bcook.io is live on Vercel. The homepage, `/privacy`, and `/support` remain public, require no account, and are linked from the site. The app uses these same permanent privacy/support URLs and includes an offline policy. The GitHub integration publishes `main` with `website` as its root; private companion state and repository files are not part of the public site.

The current workspace also includes a project-file browser and nonpersistent WebKit page previews. The policy describes their file requests, local-page forwarding, expiring preview credentials, external web requests, and page scripts. Include those features in the final reviewer walkthrough and test their read-only authorization, path confinement, preview credential isolation, and external navigation before submission.


## Website requirements retained in the marketing redesign

Checked against Apple’s documentation on 24 October 2026. Website readiness does not complete the native release gates above.

| App Store Connect field | Value | Website requirement |
| --- | --- | --- |
| Marketing URL | `https://trill.bcook.io` | Clear, accurate app information; current early-access availability |
| Support URL (required) | `https://trill.bcook.io/support` | Public help and a working contact link to `trill@bcook.io` |
| Privacy Policy URL (required) | `https://trill.bcook.io/privacy` | Public policy covering data handling, AI sharing, retention, revocation and deletion |
| User Privacy Choices URL (optional) | `https://trill.bcook.io/privacy#privacy-choices` | Existing deletion and consent instructions |

The app still links to support and the policy before pairing and from Connection details. Public policy paragraphs must match `ios/Trill/PrivacyPolicy.txt`; both include the optional hosted demo’s shared storage and retention. A new iOS build must include this policy before external promotion; updating the website does not update an installed build’s offline copy. There is no App Store badge or claimed release until an actual listing exists. Website videos are marketing demonstrations, not submission-ready App Store preview exports. Store screenshots, descriptions, privacy labels, reviewer contact/access, signing, export declarations, and native-device validation remain App Store Connect/release work. No App Store record was edited or submitted by this website task.

Validation for each site release: confirm HTTPS 200 for home/support/privacy; verify email links and footer navigation; compare public policy paragraphs with the bundled text; inspect desktop/mobile layouts and keyboard navigation; check that no tracking or third-party player scripts were introduced. A contact link alone does not establish inbox delivery.

Sources:
- [Apple platform version information: support and marketing URLs](https://developer.apple.com/help/app-store-connect/reference/app-information/platform-version-information/)
- [Apple app information: required privacy policy URL](https://developer.apple.com/help/app-store-connect/reference/app-information/app-information)
- [App Review: broken links and required contact/privacy pages](https://developer.apple.com/app-store/review/)
- [App Review Guidelines, 1.5, 5.1.1, and 5.1.2](https://developer.apple.com/app-store/review/guidelines/)
- [App privacy details: policy and privacy-choice links](https://developer.apple.com/app-store/app-privacy-details/)
