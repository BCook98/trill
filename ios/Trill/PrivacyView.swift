import SwiftUI
import CryptoKit

enum TerminalConsentPreference {
    // Bump the version if the terminal disclosure or permission scope changes.
    static let storageKey = "terminalConsent.v2"

    private static func identity(for computer: Computer) -> String {
        let parts = [computer.address, CertificateFingerprint.normalized(computer.fingerprint), computer.token]
        let identity = parts.map { "\($0.utf8.count):\($0)" }.joined()
        return SHA256.hash(data: Data(identity.utf8)).map { String(format: "%02x", $0) }.joined()
    }
    static func contains(_ computer: Computer, in value: String) -> Bool {
        value.split(separator: ",").contains(Substring(identity(for: computer)))
    }
    static func remembering(_ computer: Computer, in value: String) -> String {
        var identities = Set(value.split(separator: ",").map(String.init))
        identities.insert(identity(for: computer))
        return identities.sorted().joined(separator: ",")
    }
    static func forgetting(_ computer: Computer, in value: String) -> String {
        value.split(separator: ",").map(String.init).filter { $0 != identity(for: computer) }.joined(separator: ",")
    }
    static func clear(_ computer: Computer, defaults: UserDefaults = .standard) {
        let value = defaults.string(forKey: storageKey) ?? ""
        defaults.set(forgetting(computer, in: value), forKey: storageKey)
    }
}

// A separate permission: approving terminal input does not approve photo uploads.
enum PhotoConsentPreference {
    static let storageKey = "photoConsent.v2"
    static func contains(_ computer: Computer, in value: String) -> Bool { TerminalConsentPreference.contains(computer, in: value) }
    static func remembering(_ computer: Computer, in value: String) -> String { TerminalConsentPreference.remembering(computer, in: value) }
    static func forgetting(_ computer: Computer, in value: String) -> String { TerminalConsentPreference.forgetting(computer, in: value) }
    static func clear(_ computer: Computer, defaults: UserDefaults = .standard) {
        defaults.set(forgetting(computer, in: defaults.string(forKey: storageKey) ?? ""), forKey: storageKey)
    }
}

// Show these disclosures before the user grants the corresponding access.
enum SharingDisclosure {
    // Explain both contexts without treating a computer's display name or URL as a trust signal.
    private static let hostedDemo = "\n\nIn the optional Trill demo, sessions are scripted with no shell or AI. The developer and other paired testers can access saved session content, files, and uploads. Use sample text and photos only."

    static let agent = "Starting a session sends your goal and project context to the connected computer. A coding agent may send prompts, code, files, and photos to its AI provider. Check the agent's provider and privacy settings on that computer first." + hostedDemo + "\n\nDisconnecting or forgetting the computer does not delete shared data."
    static let terminal = "Your input goes to the program on the connected computer. A coding agent may send prompts, code, files, and photos to its AI provider. Check the provider and privacy settings on that computer first." + hostedDemo + "\n\nDisconnect stops further input but leaves the session running. Disconnecting or forgetting the computer does not delete shared data."
    static let photo = "Insert uploads your selected photos as JPEG copies to the connected computer, without location or EXIF metadata. Queued photos stay on this device until you insert them. If a coding agent reads it, the agent may send it to its AI provider. Check the provider and privacy settings first." + hostedDemo + "\n\nClosing the draft or forgetting the computer does not delete the uploaded copy."
}

struct PrivacyPolicyView: View {
    var body: some View {
        BundledDocumentView(title: "Privacy policy", resource: "PrivacyPolicy") {
            Link("Read online", destination: URL(string: "https://trill.bcook.io/privacy")!)
            Link("Privacy questions", destination: URL(string: "mailto:trill@bcook.io?subject=Trill%20privacy")!)
        }
    }
}

struct BundledDocumentView<Footer: View>: View {
    let title: String
    let resource: String
    @ViewBuilder var footer: () -> Footer
    init(title: String, resource: String, @ViewBuilder footer: @escaping () -> Footer) {
        self.title = title; self.resource = resource; self.footer = footer
    }
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                Text(contents).font(.body).textSelection(.enabled)
                footer()
            }.frame(maxWidth: 680, alignment: .leading).padding(24).frame(maxWidth: .infinity)
        }.navigationTitle(title).navigationBarTitleDisplayMode(.inline)
    }
    private var contents: String {
        guard let url = Bundle.main.url(forResource: resource, withExtension: "txt"),
              let text = try? String(contentsOf: url, encoding: .utf8) else {
            return "Trill could not open this document. Visit trill.bcook.io or email trill@bcook.io for a copy."
        }
        return text
    }
}

extension BundledDocumentView where Footer == EmptyView {
    init(title: String, resource: String) { self.init(title: title, resource: resource) { EmptyView() } }
}

struct SupportView: View {
    var body: some View {
        List {
            Section("Contact") {
                Link("Email trill@bcook.io", destination: URL(string: "mailto:trill@bcook.io?subject=Trill%20support")!)
                Link("Trill support", destination: URL(string: "https://trill.bcook.io/support")!)
                Text("Include your app and iOS versions and describe what happened. Leave out pairing codes, credentials, and private terminal content.").font(.footnote).foregroundStyle(.secondary)
            }
            Section("Pairing") {
                Text("Run Trill's setup on your Mac, keep both devices on the same Wi-Fi or your configured VPN, then scan a fresh pairing code. You can paste a pairing link if camera access is unavailable.")
            }
            Section("Read-only access") {
                Text("Run trill control --enable on your Mac to allow terminals and session actions. Enable it only for devices you trust.")
            }
            Section("Disconnecting and deleting data") {
                Text("Disconnect leaves the session running on the connected computer. Forget computer removes this phone's primary pairing details; it does not revoke its device registration or delete uploaded photos or session history. On your own Mac, run trill devices and trill revoke DEVICE_ID to revoke access, and delete data through companion storage, the program, or the AI provider that received it. For the optional shared developer-hosted demo, use sample content only and email trill@bcook.io to request server-data deletion. Other testers and active terminals can extend its 24-hour inactivity retention. Leave credentials and private content out of support requests.")
            }
            Section { NavigationLink("Privacy policy") { PrivacyPolicyView() } }
        }.navigationTitle("Support").navigationBarTitleDisplayMode(.inline)
    }
}

struct SharingConsentView: View {
    let title: String
    let destination: String
    let message: String
    let actionTitle: String
    let allow: () -> Void
    var allowAndRemember: (() -> Void)? = nil
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    Image(systemName: "terminal").font(.system(size: 30, weight: .medium))
                        .foregroundStyle(Color.accentColor).padding(20)
                        .background(Color.accentColor.opacity(0.10), in: RoundedRectangle(cornerRadius: 22))
                    VStack(alignment: .leading, spacing: 10) {
                        Text(title).font(.largeTitle.bold())
                        Label(destination, systemImage: "laptopcomputer").font(.headline).foregroundStyle(.secondary)
                    }
                    Text(message).font(.body).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    if allowAndRemember != nil {
                        Text("Remembering this applies to any terminal on this paired computer. Different agents may use different AI providers. You can reset the choice in Connection details.")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                    NavigationLink { PrivacyPolicyView() } label: { Label("How Trill handles your data", systemImage: "hand.raised") }
                        .font(.subheadline)
                }.frame(maxWidth: 560, alignment: .leading).padding(28).frame(maxWidth: .infinity)
            }
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: 10) {
                    Button { dismiss(); allow() } label: {
                        Text(actionTitle).font(.headline).frame(maxWidth: .infinity).padding(.vertical, 8)
                    }.buttonStyle(.borderedProminent).controlSize(.large)
                    if let allowAndRemember {
                        Button { dismiss(); allowAndRemember() } label: {
                            Text("Allow and don't ask again").frame(maxWidth: .infinity).padding(.vertical, 8)
                        }.buttonStyle(.bordered).controlSize(.large)
                            .accessibilityIdentifier("remember-terminal-permission")
                    }
                }.padding(.horizontal, 28).padding(.vertical, 16).background(.bar)
            }
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }.presentationDetents([.large]).presentationDragIndicator(.visible)
    }
}
