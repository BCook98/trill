import SwiftUI

struct ComputerView: View {
    @Environment(AppStore.self) private var store
    @State private var showForget = false
    @State private var editPairing = false
    @State private var showComputers = false
    @AppStorage("terminalFontSize") private var fontSize = 14.0
    @AppStorage(TerminalConsentPreference.storageKey) private var terminalConsent = ""
    @AppStorage(PhotoConsentPreference.storageKey) private var photoConsent = ""
    var body: some View {
        List {
            if let computer = store.computer {
                Section {
                    HStack(spacing: 16) {
                        Image(systemName: "laptopcomputer").font(.largeTitle).foregroundStyle(Color.accentColor)
                        VStack(alignment: .leading, spacing: 5) {
                            Text(computer.name).font(.headline)
                            Text(computer.address).font(.subheadline).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }.padding(.vertical, 12)
                    LabeledContent("Access", value: store.readOnly ? "Read-only" : "Session control")
                    if let date = store.refreshedAt { LabeledContent("Last connected") { Text(date, style: .relative) } }
                    if let health = store.health { LabeledContent("Sidecar", value: health.version) }
                    if let error = store.error { Label(error, systemImage: "wifi.exclamationmark").font(.subheadline).foregroundStyle(.secondary) }
                    Button("Check connection", systemImage: "arrow.clockwise") { Task { await store.refresh() } }.disabled(store.loading)
                } header: { Text("Your computer") }
                Section {
                    Button("Saved computers", systemImage: "laptopcomputer") { showComputers = true }
                    Button("Edit pairing", systemImage: "pencil") { editPairing = true }
                    Button("Forget computer", systemImage: "link.badge.plus", role: .destructive) { showForget = true }
                } footer: { Text("This device keeps your pairing details in Keychain. Your sessions run on your Mac.") }
                Section {
                    LabeledContent("Terminal permission", value: TerminalConsentPreference.contains(computer, in: terminalConsent) ? "Remembered" : "Ask each time")
                    if TerminalConsentPreference.contains(computer, in: terminalConsent) {
                        Button("Ask before opening terminals") {
                            terminalConsent = TerminalConsentPreference.forgetting(computer, in: terminalConsent)
                        }.accessibilityIdentifier("reset-terminal-permission")
                    }
                    LabeledContent("Photo permission", value: PhotoConsentPreference.contains(computer, in: photoConsent) ? "Remembered" : "Not granted")
                    if PhotoConsentPreference.contains(computer, in: photoConsent) {
                        Button("Ask before uploading photos") {
                            photoConsent = PhotoConsentPreference.forgetting(computer, in: photoConsent)
                        }.accessibilityIdentifier("reset-photo-permission")
                    }
                } header: { Text("Permissions") } footer: {
                    Text("These choices apply only to this pairing. You still choose each photo. Resetting a permission does not disconnect terminals or delete photos already uploaded.")
                }
            } else {
                Section {
                    FeatureRow(symbol: "laptopcomputer", title: "Connect your Mac", text: "Run Trill's setup on your Mac, then scan its pairing code here.").padding(.vertical, 12)
                    Button("Pair a computer", systemImage: "link") { store.pairingPresented = true }
                }
            }
            Section("Terminal") {
                Stepper(value: $fontSize, in: 10...28, step: 1) {
                    LabeledContent("Text size", value: "\(Int(fontSize)) pt")
                }
                Text("Aa Bb 0123 →").font(.system(size: fontSize, design: .monospaced)).padding(.vertical, 6).accessibilityLabel("Terminal font preview")
            }
            Section("About Trill") {
                Label { Text("A terminal for your Herdr sessions.") } icon: {
                    Image("TrillWave").resizable().scaledToFit().frame(width: 22, height: 16).foregroundStyle(Color.accentColor)
                }
                Text("Trill is an independent companion for Herdr. It uses SwiftTerm and is not affiliated with Herdr.").font(.footnote).foregroundStyle(.secondary)
                NavigationLink("Privacy policy") { PrivacyPolicyView() }
                NavigationLink("Support") { SupportView() }
                NavigationLink("Acknowledgements") { BundledDocumentView(title: "Acknowledgements", resource: "Acknowledgements") }
            }
        }
        .navigationTitle("Connection details")
        .sheet(isPresented: $editPairing) { PairComputerView() }
        .sheet(isPresented: $showComputers) { ComputersView() }
        .alert("Forget this computer?", isPresented: $showForget) {
            Button("Cancel", role: .cancel) {}
            Button("Forget computer", role: .destructive) { Task { await store.forget(); if store.computer == nil { store.connectionDetailsPresented = false } } }
        } message: { Text("Trill will remove the pairing details from this device and disconnect. Your sessions will keep running on your Mac.") }
    }
}

struct PairComputerView: View {
    var prefillCurrent = true
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var name = "My Mac"
    @State private var address = ""
    @State private var token = ""
    @State private var fingerprint = ""
    @State private var pairing = false
    @State private var error: String?
    @State private var didInitialize = false
    @State private var manual = false
    @State private var scanner = false
    @State private var discardPending = false
    @State private var invitation: PairingInvitation?
    @State private var deviceName = "My iPhone"
    @AccessibilityFocusState private var errorFocused: Bool

    var body: some View {
        NavigationStack {
            Form {
                if let pending = store.pendingEnrollment {
                    Section {
                        Label(pending.name, systemImage: "laptopcomputer").font(.headline)
                        Text(pending.address).font(.subheadline).textSelection(.enabled)
                        Label(store.pendingEnrollmentSaved ? "Device credential saved" : "Device credential needs saving", systemImage: "key")
                        Text("Your Mac approved this device. Tap Pair to check the connection and finish pairing. Trill will keep your current computer paired until then.").font(.subheadline).foregroundStyle(.secondary)
                        Button("Discard unfinished pairing", role: .destructive) { discardPending = true }
                    } header: { Text("Finish pairing") }
                } else if let invitation {
                    Section {
                        Label(invitation.name, systemImage: "laptopcomputer").font(.headline)
                        Text(invitation.address).font(.subheadline).textSelection(.enabled)
                        Label(invitation.fingerprint.isEmpty ? "Trusted HTTPS certificate" : "Certificate included in pairing code", systemImage: "lock.shield")
                            .font(.subheadline).foregroundStyle(.secondary)
                        TextField("This device's name", text: $deviceName).accessibilityIdentifier("device-name")
                        LabeledContent("Code expires") { Text(invitation.expiresAt, style: .time) }
                        Button("Use a different code", systemImage: "qrcode") { self.invitation = nil; error = nil }.disabled(pairing)
                    } header: { Text("Review this computer") }
                    footer: { Text("Use a code you created on your own Mac. Pairing lets this device connect to the companion. You enable terminal access separately on the Mac.") }
                } else {
                    Section {
                        FeatureRow(symbol: "qrcode.viewfinder", title: "Pair with your Mac", text: "Run Trill's setup on your Mac, then scan its pairing code. Keep both devices on the same Wi-Fi or Tailscale network.").padding(.vertical, 8)
                        Button("Scan pairing code", systemImage: "qrcode.viewfinder") { scanner = true }.accessibilityIdentifier("scan-pairing")
                        PasteButton(payloadType: String.self) { values in if let text = values.first { importCode(text) } }
                            .accessibilityLabel("Paste pairing link")
                    } footer: { Text("Each code works once and expires after ten minutes.") }
                    Section {
                        DisclosureGroup("Enter details manually", isExpanded: $manual) {
                            TextField("Name", text: $name).textContentType(.nickname).accessibilityIdentifier("computer-name")
                            TextField("https://mac.example:7443", text: $address).keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled().accessibilityLabel("Sidecar address").accessibilityIdentifier("sidecar-address")
                            SecureField("Pairing token", text: $token).textInputAutocapitalization(.never).autocorrectionDisabled().accessibilityIdentifier("pairing-token")
                            TextField("Certificate fingerprint (self-signed)", text: $fingerprint, axis: .vertical)
                                .font(.system(.footnote, design: .monospaced)).textInputAutocapitalization(.never).autocorrectionDisabled()
                        }
                    } footer: {
                        if manual { Text("A self-signed certificate requires its SHA-256 fingerprint. For a trusted Tailscale certificate, use the full .ts.net hostname and leave the fingerprint blank.") }
                    }
                }
                if let error {
                    Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red).font(.subheadline).accessibilityFocused($errorFocused).accessibilityIdentifier("pairing-error") }
                }
                if pairing { Section { HStack { ProgressView(); Text("Connecting securely…") } } }
            }
            .disabled(pairing)
            .navigationTitle("Pair a computer").navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel", systemImage: "xmark") { dismiss() }.disabled(pairing) }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Pair", systemImage: "link", action: pair)
                        .disabled(pairing || !canPair).accessibilityLabel("Pair computer").accessibilityIdentifier("confirm-pairing")
                }
            }
            .interactiveDismissDisabled(pairing)
            .alert("Discard unfinished pairing?", isPresented: $discardPending) {
                Button("Cancel", role: .cancel) {}
                Button("Discard", role: .destructive) {
                    do { try store.discardPendingEnrollment(); error = nil }
                    catch { self.error = error.localizedDescription; errorFocused = true }
                }
            } message: { Text("Trill will remove the unfinished pairing from this phone. Revoke its device entry on the Mac if you no longer want it registered. Your current computer will stay paired.") }
            .sheet(isPresented: $scanner) {
                NavigationStack {
                    PairingScanner(onCode: { scanner = false; importCode($0) }, onFailure: { scanner = false; error = $0; errorFocused = true })
                        .overlay(alignment: .bottom) { Text("Point the camera at the code on your Mac").font(.callout).padding().background(.regularMaterial, in: Capsule()).padding() }
                        .navigationTitle("Scan pairing code").navigationBarTitleDisplayMode(.inline)
                        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel", systemImage: "xmark") { scanner = false } } }
                }
            }
            .onAppear {
                guard !didInitialize else { return }
                didInitialize = true
                if prefillCurrent, let computer = store.computer { name = computer.name; address = computer.address; token = computer.token; fingerprint = computer.fingerprint; manual = true }
                consumeLink()
            }
            .onChange(of: store.pairingLink) { _, _ in consumeLink() }
        }
    }
    private var canPair: Bool {
        if store.pendingEnrollment != nil { return true }
        if invitation != nil { return !deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
        return manual && !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !address.isEmpty && !token.isEmpty
    }
    private func consumeLink() {
        guard !pairing, let link = store.pairingLink else { return }
        store.pairingLink = nil
        importCode(link)
    }
    private func importCode(_ text: String) {
        do { invitation = try PairingInvitation.parse(text); error = nil; errorFocused = false }
        catch { self.error = error.localizedDescription; errorFocused = true }
    }
    private func pair() {
        pairing = true; error = nil; errorFocused = false
        Task {
            do {
                if let pending = store.pendingEnrollment {
                    try await store.pairEnrollment(pending)
                } else if let invitation {
                    let computer = try await APIClient.redeem(invitation, deviceName: deviceName.trimmingCharacters(in: .whitespacesAndNewlines))
                    self.invitation = nil
                    try await store.pairEnrollment(computer)
                } else {
                    try await store.pair(Computer(name: name.trimmingCharacters(in: .whitespacesAndNewlines), address: address.trimmingCharacters(in: .whitespacesAndNewlines), token: token.trimmingCharacters(in: .whitespacesAndNewlines), fingerprint: CertificateFingerprint.normalized(fingerprint)))
                }
                dismiss()
            } catch { self.error = error.localizedDescription; errorFocused = true }
            pairing = false
        }
    }
}

struct SessionCreationFailure {
    let outcomeUncertain: Bool
    let message: String

    init(_ error: Error) {
        if error is SessionCreationPreflightError {
            outcomeUncertain = false
        } else if let response = error as? APIRequestError {
            outcomeUncertain = response.status == 409 && response.message.contains("unconfirmed")
        } else {
            // Transport errors are wrapped in TrillError, and decoding can fail
            // after the server has accepted a request. Neither is safe to retry.
            outcomeUncertain = true
        }
        message = outcomeUncertain
            ? "Trill could not confirm whether your computer created the session. Check Sessions before trying again."
            : error.localizedDescription
    }
}

struct NewSessionView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var projectID = ""
    @State private var creating = false
    @State private var creationUncertain = false
    @State private var error: String?
    var body: some View {
        NavigationStack {
            Form {
                Section("Space") {
                    Picker("Space", selection: $projectID) {
                        Text("Select a space").tag("")
                        ForEach(store.creatableProjects) { Text($0.displayName).tag($0.id) }
                    }
                    Text("Opens a shell in this Herdr space. Start your coding agent in its terminal.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                if let error { Text(error).foregroundStyle(.red) }
            }
            .navigationTitle("New terminal")
            .safeAreaInset(edge: .bottom) {
                Button(creating ? "Creating…" : "Create terminal") {
                    guard !creating, !creationUncertain else { return }
                    creating = true
                    Task {
                        do { try await store.createTerminal(projectID: projectID); dismiss() }
                        catch {
                            let failure = SessionCreationFailure(error)
                            self.error = failure.message
                            creationUncertain = failure.outcomeUncertain
                            creating = false
                        }
                    }
                }
                .buttonStyle(.borderedProminent).controlSize(.large)
                .disabled(creating || creationUncertain || store.readOnly || !store.canCreateTerminal(projectID: projectID))
                .accessibilityIdentifier("new-session-submit").padding()
            }
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.disabled(creating) } }
            .interactiveDismissDisabled(creating)
            .onAppear { if projectID.isEmpty { projectID = store.creatableProjects.first?.id ?? "" } }
        }
    }
}

struct NewHerdrItemView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    let isWorktree: Bool
    @State private var targetID = ""
    @State private var name = ""
    @State private var directoryOrBranch = ""
    @State private var hasPreparedForm = false
    @State private var creating = false
    @State private var creationUncertain = false
    @State private var error: String?
    private var trimmedName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var trimmedLocation: String { directoryOrBranch.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var targetAvailable: Bool {
        isWorktree ? store.worktreeProjects.contains { $0.id == targetID } : store.spaceProviders.contains { $0.id == targetID }
    }
    var body: some View {
        NavigationStack {
            Form {
                Section(isWorktree ? "Repository space" : "Herdr server") {
                    Picker(isWorktree ? "Space" : "Server", selection: $targetID) {
                        Text("Select…").tag("")
                        if isWorktree {
                            ForEach(store.worktreeProjects) { project in
                                Text(project.displayName + (store.spaceProviders.count > 1 ? " · " + (project.providerName ?? "Herdr") : "")).tag(project.id)
                            }
                        } else {
                            ForEach(store.spaceProviders, id: \.id) { provider in Text(provider.name).tag(provider.id) }
                        }
                    }
                }
                Section {
                    TextField(isWorktree ? "Name (optional)" : "Space name", text: $name)
                        .accessibilityIdentifier("new-herdr-name")
                    TextField(isWorktree ? "New branch, e.g. feature/search" : "Folder path on your Mac", text: $directoryOrBranch)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .accessibilityIdentifier("new-herdr-location")
                } footer: {
                    Text(isWorktree
                         ? "Herdr creates a branch and worktree from the selected primary repository space and opens a shell there. It appears beneath its repository space."
                         : "Use the full path to an existing folder, for example /Users/you/code/app. Herdr opens a space with a shell in that folder.")
                }
                Section {
                    Label("Creates in Herdr on your computer", systemImage: "laptopcomputer")
                    Text("You can open the terminal in Trill and start your coding agent there.")
                        .font(.subheadline).foregroundStyle(.secondary)
                }
                if let error {
                    Section {
                        Text(error).foregroundStyle(.red)
                        if creationUncertain {
                            Button("Check Sessions") { Task { await store.refresh(); dismiss() } }
                        }
                    }
                }
            }
            .disabled(creating)
            .navigationTitle(isWorktree ? "New worktree" : "New space")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.disabled(creating) }
                ToolbarItem(placement: .confirmationAction) {
                    Button(creating ? "Creating…" : "Create") { create() }
                        .disabled(creating || creationUncertain || store.busy || store.readOnly || !targetAvailable || trimmedLocation.isEmpty || (!isWorktree && (trimmedName.isEmpty || !trimmedLocation.hasPrefix("/"))))
                        .accessibilityIdentifier("new-herdr-submit")
                }
            }
            .interactiveDismissDisabled(creating)
            .onAppear {
                guard !hasPreparedForm else { return }
                hasPreparedForm = true
                targetID = isWorktree ? (store.worktreeProjects.first?.id ?? "") : (store.spaceProviders.first?.id ?? "")
                if isWorktree {
                    let suggestedName = Self.suggestedWorktreeName()
                    name = suggestedName
                    directoryOrBranch = suggestedName
                }
            }
        }
    }
    private static func suggestedWorktreeName() -> String {
        let adjectives = ["bright", "calm", "clever", "gentle", "lively", "nimble", "quiet", "silver", "swift", "vivid", "warm", "wild"]
        let birds = ["finch", "heron", "kestrel", "kingfisher", "lark", "osprey", "owl", "robin", "sparrow", "swift", "tern", "wren"]
        let adjective = adjectives[Int.random(in: adjectives.indices)]
        let bird = birds[Int.random(in: birds.indices)]
        let suffix = UUID().uuidString.prefix(4).lowercased()
        return "\(adjective)-\(bird)-\(suffix)"
    }
    private func create() {
        guard !creating, !creationUncertain else { return }
        creating = true; error = nil
        Task {
            do {
                try await store.createHerdrItem(isWorktree: isWorktree, targetID: targetID, name: trimmedName, directoryOrBranch: trimmedLocation)
                dismiss()
            } catch {
                let failure = SessionCreationFailure(error)
                creationUncertain = failure.outcomeUncertain
                self.error = failure.outcomeUncertain
                    ? "Trill could not confirm creation. Refresh Spaces and check Herdr before trying again."
                    : failure.message
            }
            creating = false
        }
    }
}
