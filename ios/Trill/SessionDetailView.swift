import SwiftUI
import PhotosUI
import ImageIO

// One item owns both sheet presentation and the destination the user approves.
struct TerminalConsentRequest: Identifiable {
    let id = UUID()
    let computer: Computer
    let sessionID: String
    let agent: String

    var destination: String { "\(agent) · \(computer.name)" }

    func isCurrent(computer: Computer?, sessionID: String?, changingComputer: Bool, readOnly: Bool) -> Bool {
        self.computer == computer && self.sessionID == sessionID && !changingComputer && !readOnly
    }
}

struct SessionDetailView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.openURL) private var openURL
    let session: RemoteSession
    @Bindable var connection: TerminalConnection
    @State private var showInputNotice = false
    @State private var showInfo = false
    @State private var tabletop = false
    @State private var terminalSurfaceID = UUID()
    @Binding var showWorkspace: Bool
    @Binding var workspaceTab: WorkspaceTab
    @Binding var previewAddress: String?
    @State private var showWriter = false
    @State private var terminalConsentRequest: TerminalConsentRequest?
    @AppStorage(TerminalConsentPreference.storageKey) private var terminalConsent = ""
    @AppStorage("terminalFontSize") private var fontSize = 14.0

    var body: some View {
        VStack(spacing: 0) {
            if connection.isOpen || isFailed {
                TerminalInteractionRegion(tabletop: $tabletop) {
                    ZStack(alignment: .top) {
                        TerminalSurface(connection: connection)
                            .background(.black)
                        if connection.phase == .connecting || connection.phase == .reconnecting {
                            VStack(alignment: .leading, spacing: 10) {
                                HStack(spacing: 10) {
                                    ProgressView()
                                    Text(connection.phase == .reconnecting ? "Reconnecting…" : "Opening terminal…").font(.headline)
                                }
                                if let message = connection.recoveryMessage {
                                    Text(message).font(.subheadline).textSelection(.enabled)
                                }
                                if connection.canRetryRecovery {
                                    Button("Retry now") { Task { await connection.retryRecoveryNow() } }
                                        .buttonStyle(.bordered).disabled(store.readOnly)
                                        .accessibilityIdentifier("terminal-retry-now")
                                }
                            }
                            .padding().frame(maxWidth: 460)
                            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 16)).padding()
                            .accessibilityIdentifier("terminal-recovery-status")
                        }
                        if case .failed(let message) = connection.phase {
                            VStack(alignment: .leading, spacing: 10) {
                                Label("Connection interrupted", systemImage: "wifi.exclamationmark").font(.headline)
                                Text(message).font(.subheadline).textSelection(.enabled)
                                Button("Reconnect") { connect() }.buttonStyle(.borderedProminent).disabled(store.readOnly)
                            }.padding().frame(maxWidth: 460).background(.regularMaterial, in: RoundedRectangle(cornerRadius: 16)).padding()
                        }
                    }
                } controls: { lowerScreen in
                    terminalControls(lowerScreen: lowerScreen)
                }.background(.black)
            } else {
                ScrollView {
                    VStack(spacing: 24) {
                        Image(systemName: "terminal").font(.system(size: 44, weight: .light)).foregroundStyle(Color.accentColor)
                            .padding(24).background(Color.accentColor.opacity(0.08), in: RoundedRectangle(cornerRadius: 28))
                        VStack(spacing: 10) {
                            Text("Open this session").font(.title2.bold())
                            Text(session.attachable ? "Connect to the session already running on your Mac." : "This session doesn't have a running terminal.")
                                .foregroundStyle(.secondary).multilineTextAlignment(.center)
                        }
                        if store.readOnly {
                            Label("Read-only access", systemImage: "eye").font(.headline)
                            Text("Run trill control --enable on your Mac to open terminals and manage sessions.")
                                .font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
                        } else if session.attachable {
                            Button(action: connect) { Label("Open terminal", systemImage: "terminal").padding(.horizontal, 20).padding(.vertical, 4) }
                                .buttonStyle(.borderedProminent).controlSize(.large).accessibilityIdentifier("open-terminal")
                        } else if session.resumable {
                            Button("Resume session") { Task { await store.action("resume", session: session) } }.buttonStyle(.borderedProminent).disabled(store.busy)
                        }
                        if let warning = connection.cleanupWarning { Text(warning).font(.footnote).foregroundStyle(.secondary) }
                        if let error = store.error { Text(error).font(.footnote).foregroundStyle(.red) }
                    }.padding(32).frame(maxWidth: 500).frame(maxWidth: .infinity)
                }.frame(maxHeight: .infinity)
            }
        }
        .navigationTitle(connection.isOpen || isFailed ? "" : session.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { terminalToolbar }
        .modifier(TerminalBarBehavior())
        .onAppear {
            connection.terminalDidAppear(surfaceID: terminalSurfaceID)
            connection.setFontSize(fontSize)
        }
        .onDisappear { connection.terminalDidDisappear(surfaceID: terminalSurfaceID) }
        .onChange(of: fontSize) { _, value in connection.setFontSize(value) }
        .sheet(item: $terminalConsentRequest) { request in
            SharingConsentView(title: "Open your terminal", destination: request.destination, message: SharingDisclosure.terminal, actionTitle: "Allow once", allow: {
                connectAfterConsent(request)
            }, allowAndRemember: {
                guard request.isCurrent(computer: store.computer, sessionID: store.selectedID,
                                        changingComputer: store.changingComputer, readOnly: store.readOnly) else { return }
                terminalConsent = TerminalConsentPreference.remembering(request.computer, in: terminalConsent)
                connectAfterConsent(request)
            })
        }
        .onChange(of: connection.pendingLink) { _, url in
            guard let url else { return }
            connection.pendingLink = nil
            guard !store.changingComputer else { return }
            _ = connection.terminal.resignFirstResponder()
            switch TerminalLinkRoute.destination(for: url, computer: store.api?.baseURL) {
            case .preview(let address): openWorkspace(.browser, address: address.absoluteString)
            case .external(let address):
                // Prefer an associated app, then the user's default browser.
                UIApplication.shared.open(address, options: [.universalLinksOnly: true]) { opened in
                    if !opened { openURL(address) }
                }
            case .none: break
            }
        }
        .sheet(isPresented: $showWriter) {
            TerminalWriter(connection: connection, sessionID: session.id, sessionName: session.name)
        }
        .sheet(isPresented: $showInputNotice) {
            NavigationStack {
                ScrollView {
                    Text(connection.inputNotice ?? "No input notices.")
                        .frame(maxWidth: .infinity, alignment: .leading).padding()
                }
                .navigationTitle("Terminal input").navigationBarTitleDisplayMode(.inline)
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { showInputNotice = false } } }
            }.presentationDetents([.medium, .large])
        }
        .sheet(isPresented: $showInfo) {
            NavigationStack {
                List {
                    LabeledContent("Session", value: session.name)
                    LabeledContent("Project", value: session.projectName)
                    LabeledContent("Agent", value: session.displayAgent)
                    LabeledContent("Status", value: session.statusLabel)
                    if !session.branch.isEmpty { LabeledContent("Branch", value: session.branch) }
                    Section("Connection") {
                        LabeledContent("State", value: connection.phaseLabel)
                        LabeledContent("Transport", value: "Mosh · UDP")
                        LabeledContent("Terminal", value: "SwiftTerm")
                        if let warning = connection.cleanupWarning { Text(warning).foregroundStyle(.secondary) }
                        Text("Disconnecting closes Trill's connection. Your session keeps running on your computer.").foregroundStyle(.secondary)
                    }
                }.navigationTitle("Session details").navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { showInfo = false } } }
            }.presentationDetents([.medium, .large])
        }
    }
    private func terminalControls(lowerScreen: Bool) -> some View {
        TerminalKeys(connection: connection) {
            if connection.inputNotice != nil {
                Button("Input notice", systemImage: "exclamationmark.bubble") { showInputNotice = true }
                    .labelStyle(.iconOnly).frame(minWidth: 44, minHeight: 44)
                    .accessibilityIdentifier("terminal-input-notice")
                    .accessibilityShowsLargeContentViewer()
            }
            if lowerScreen {
                Button("Write", systemImage: "square.and.pencil", action: openWriter)
                    .labelStyle(.iconOnly).frame(minWidth: 44, minHeight: 44)
                    .accessibilityIdentifier("terminal-write")
                    .accessibilityShowsLargeContentViewer()
                Button(showWorkspace ? "Hide files and browser" : "Files and browser", systemImage: "sidebar.right", action: toggleWorkspace)
                    .labelStyle(.iconOnly).frame(minWidth: 44, minHeight: 44)
                    .disabled(store.readOnly)
                    .accessibilityIdentifier("session-workspace")
                    .accessibilityShowsLargeContentViewer()
                Menu { sessionActions } label: {
                    Label("Session actions", systemImage: "ellipsis").labelStyle(.iconOnly)
                        .frame(minWidth: 44, minHeight: 44)
                }.accessibilityShowsLargeContentViewer()
                Divider().frame(height: 20)
            }
        }
        .font(.system(size: 17))
        .frame(height: 50)
        .background(.bar)
    }

    @ToolbarContentBuilder private var terminalToolbar: some ToolbarContent {
        if !tabletop || !(connection.isOpen || isFailed) {
            if #available(iOS 27.0, *) {
                if connection.isOpen || isFailed {
                    ToolbarItem(placement: .primaryAction) {
                        Button("Write", systemImage: "square.and.pencil", action: openWriter)
                            .accessibilityHint("Type or dictate a draft before inserting it into the terminal")
                            .accessibilityIdentifier("terminal-write")
                    }.visibilityPriority(.high)
                }
                ToolbarItem(placement: .primaryAction) {
                    Button(showWorkspace ? "Hide files and browser" : "Files and browser", systemImage: "sidebar.right") {
                        toggleWorkspace()
                    }
                        .disabled(store.readOnly).accessibilityIdentifier("session-workspace")
                }.visibilityPriority(.high)
                ToolbarOverflowMenu { sessionActions }
            } else {
                ToolbarItemGroup(placement: .primaryAction) {
                    if connection.isOpen || isFailed {
                        Button("Write", systemImage: "square.and.pencil", action: openWriter)
                            .accessibilityIdentifier("terminal-write")
                    }
                    Menu { sessionActions } label: { Label("Session actions", systemImage: "ellipsis.circle") }
                }
            }
        }
    }

    @ViewBuilder private var sessionActions: some View {
        Button("Browse files", systemImage: "folder") { openWorkspace(.files) }.disabled(store.readOnly)
        Button("Preview a page", systemImage: "globe") { openWorkspace(.browser) }.disabled(store.readOnly)
        Divider()
        Button("Session details", systemImage: "info.circle") { showInfo = true }
        Button("Rename", systemImage: "pencil") { store.requestSessionPrompt(.rename, sessionID: session.id) }.disabled(store.readOnly || !store.supports("rename", session: session))
        ControlGroup {
            Button("Smaller text", systemImage: "textformat.size.smaller") { fontSize = max(10, fontSize - 1) }
            Button("Larger text", systemImage: "textformat.size.larger") { fontSize = min(28, fontSize + 1) }
        }
        if connection.isOpen || isFailed {
            Button("Disconnect", systemImage: "network.slash") { Task { await connection.disconnect() } }
        }
        Divider()
        Button("Archive session…", systemImage: "archivebox") { store.requestSessionPrompt(.archive, sessionID: session.id) }
            .disabled(store.readOnly || !store.supports("archive", session: session) || store.busy || session.isArchived)
        if session.attachable && store.supports("stop", session: session) {
            Button("Stop session…", systemImage: "stop.circle", role: .destructive) { store.requestSessionPrompt(.stop, sessionID: session.id) }.disabled(store.readOnly || !store.supports("stop", session: session) || store.busy)
        } else if session.resumable && store.supports("resume", session: session) {
            Button("Resume session", systemImage: "play") { Task { await store.action("resume", session: session) } }.disabled(store.readOnly || !store.supports("resume", session: session) || store.busy)
        }
    }

    private func toggleWorkspace() {
        if showWorkspace { showWorkspace = false; previewAddress = nil }
        else { openWorkspace(workspaceTab) }
    }

    private func openWorkspace(_ tab: WorkspaceTab, address: String? = nil) {
        _ = connection.terminal.resignFirstResponder()
        previewAddress = address; workspaceTab = tab; showWorkspace = true
    }

    private func openWriter() {
        _ = connection.terminal.resignFirstResponder()
        showWriter = true
    }

    var isFailed: Bool { if case .failed = connection.phase { true } else { false } }
    func connect() {
        guard let computer = store.computer, !store.changingComputer, !store.readOnly else { return }
        let request = TerminalConsentRequest(computer: computer, sessionID: session.id, agent: session.displayAgent)
        if TerminalConsentPreference.contains(computer, in: terminalConsent) {
            connectAfterConsent(request)
        } else {
            terminalConsentRequest = request
        }
    }
    private func connectAfterConsent(_ request: TerminalConsentRequest) {
        guard let api = store.api,
              request.isCurrent(computer: store.computer, sessionID: store.selectedID,
                                changingComputer: store.changingComputer, readOnly: store.readOnly) else { return }
        Task {
            guard store.api === api,
                  request.isCurrent(computer: store.computer, sessionID: store.selectedID,
                                    changingComputer: store.changingComputer, readOnly: store.readOnly) else { return }
            await connection.connect(sessionID: request.sessionID, api: api, sessionProvider: session.provider)
        }
    }
}

struct TerminalKeys<Actions: View>: View {
    @Bindable var connection: TerminalConnection
    @ViewBuilder var actions: () -> Actions
    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 4) {
                actions()
                Group {
                    Button { connection.toggleKeyboard() } label: {
                        Image(systemName: "keyboard").frame(minWidth: 44, minHeight: 44)
                    }.accessibilityLabel("Show or hide keyboard").accessibilityIdentifier("terminal-input-keyboard").accessibilityShowsLargeContentViewer()
                    Button { connection.key("\r") } label: {
                        Label("Return", systemImage: "return")
                            .font(.system(size: 17, weight: .medium)).frame(minWidth: 76, minHeight: 44)
                    }.accessibilityHint("Send Return to the terminal").accessibilityIdentifier("terminal-return").accessibilityShowsLargeContentViewer()
                    Divider().frame(height: 20).padding(.horizontal, 4)
                    key("esc", name: "Escape", sequence: "\u{1b}")
                    key("tab", name: "Tab", sequence: "\t")
                    Button { connection.controlHeld.toggle() } label: {
                        Text("ctrl").font(.system(size: 17, weight: .medium, design: .monospaced)).frame(minWidth: 48, minHeight: 44)
                            .background(connection.controlHeld ? Color.accentColor.opacity(0.2) : Color.clear, in: RoundedRectangle(cornerRadius: 8))
                    }.accessibilityLabel("Control modifier")
                        .accessibilityValue(connection.controlHeld ? "On" : "Off")
                        .accessibilityAddTraits(connection.controlHeld ? [.isSelected] : [])
                        .accessibilityHint("Applies to the next terminal key. Tap again to turn off.")
                        .accessibilityIdentifier("terminal-control").accessibilityShowsLargeContentViewer()
                    Button { connection.shiftHeld.toggle() } label: {
                        Image(systemName: connection.shiftHeld ? "shift.fill" : "shift")
                            .font(.system(size: 17, weight: .medium)).frame(minWidth: 48, minHeight: 44)
                            .background(connection.shiftHeld ? Color.accentColor.opacity(0.2) : Color.clear, in: RoundedRectangle(cornerRadius: 8))
                    }.accessibilityLabel("Shift modifier")
                        .accessibilityValue(connection.shiftHeld ? "On" : "Off")
                        .accessibilityAddTraits(connection.shiftHeld ? [.isSelected] : [])
                        .accessibilityHint("Applies to the next terminal key, including Return and Tab. Tap again to turn off.")
                        .accessibilityIdentifier("terminal-shift").accessibilityShowsLargeContentViewer()
                    Divider().frame(height: 20).padding(.horizontal, 4)
                    arrow("arrow.up", name: "Up arrow", sequence: "\u{1b}[A")
                    arrow("arrow.down", name: "Down arrow", sequence: "\u{1b}[B")
                    arrow("arrow.left", name: "Left arrow", sequence: "\u{1b}[D")
                    arrow("arrow.right", name: "Right arrow", sequence: "\u{1b}[C")
                    key("^C", name: "Interrupt, Control C", sequence: "\u{03}")
                }.disabled(!connection.connected)
            }.padding(.horizontal, 8).padding(.vertical, 3)
        }.scrollIndicators(.hidden).background(.bar)
    }
    func key(_ title: String, name: String, sequence: String) -> some View {
        Button { connection.key(sequence) } label: { Text(title).font(.system(size: 17, weight: .medium, design: .monospaced)).frame(minWidth: 48, minHeight: 44) }.accessibilityLabel(name).accessibilityShowsLargeContentViewer { Label(name, systemImage: "keyboard") }
    }
    func arrow(_ symbol: String, name: String, sequence: String) -> some View {
        Button { connection.key(sequence) } label: { Image(systemName: symbol).font(.system(size: 17, weight: .medium)).frame(minWidth: 44, minHeight: 44) }.accessibilityLabel(name).accessibilityShowsLargeContentViewer { Label(name, systemImage: "keyboard") }
    }
}

private struct TerminalBarBehavior: ViewModifier {
    @ViewBuilder func body(content: Content) -> some View {
        if #available(iOS 27.1, *) {
            content.toolbarVerticalCompressionBehavior(.prefersToolbarItems)
        } else {
            content
        }
    }
}


/// A standard text editor keeps native keyboard dictation and editing in a local
/// draft. SwiftTerm still owns direct terminal input; only Insert crosses mosh.
private struct TerminalWriter: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.verticalSizeClass) private var verticalSizeClass
    @Bindable var connection: TerminalConnection
    let sessionID: String
    let sessionName: String
    @State private var deliveryError: String?
    @State private var composingText = false
    @State private var pickedPhoto: PhotosPickerItem?
    @State private var photoConsentRequest: SavedComputer?
    @State private var showPhotoPicker = false
    @State private var photoConsentGranted = false
    @State private var photoComputer: Computer?
    @AppStorage(PhotoConsentPreference.storageKey) private var photoConsent = ""
    @State private var photoTask: Task<Void, Never>?
    @State private var photoRequestID: UUID?
    @State private var uploadingPhoto = false
    @State private var photoPreview: UIImage?
    @State private var photoMessage: String?

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: 12) {
                VStack(alignment: .leading, spacing: 6) {
                    Text(sessionName.isEmpty ? "Your session" : sessionName).font(.headline)
                    if !compactTools {
                        Text("Insert adds your draft to the terminal. Return sends it.")
                            .font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                TerminalDraftEditor(text: $connection.draftText, compositionChanged: { composingText = $0 },
                                    onDismantle: { photoTask?.cancel() })
                    .frame(maxWidth: .infinity, minHeight: 80, maxHeight: .infinity)
                    .layoutPriority(1)
                    .padding(8)
                    .scrollContentBackground(.hidden)
                    .background(Color(uiColor: .secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12))
                    .overlay(alignment: .topLeading) {
                        if connection.draftText.isEmpty {
                            Text("Type or dictate…").foregroundStyle(.tertiary).padding(.horizontal, 13).padding(.vertical, 16)
                                .allowsHitTesting(false).accessibilityHidden(true)
                        }
                    }
                    .accessibilityLabel("Terminal draft").accessibilityIdentifier("terminal-draft")
                if let message = deliveryError ?? validationMessage {
                    Text(message).font(.footnote).foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .accessibilityIdentifier("terminal-draft-status")
                    if connection.controlHeld || connection.shiftHeld {
                        Button("Turn off Ctrl and Shift") {
                            connection.controlHeld = false
                            connection.shiftHeld = false
                            deliveryError = nil
                        }.font(.footnote).accessibilityIdentifier("terminal-draft-reset-modifiers")
                    }
                }
                if compactTools {
                    VStack(alignment: .leading, spacing: 8) { photoActions; photoProgress }
                } else {
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: 12) {
                            photoActions.fixedSize(horizontal: true, vertical: false)
                            photoProgress.fixedSize(horizontal: true, vertical: false)
                        }
                        VStack(alignment: .leading, spacing: 8) { photoActions; photoProgress }
                    }
                }
                if let photoMessage {
                    Text(photoMessage).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                        .accessibilityIdentifier("terminal-photo-status")
                }
            }
            .padding(20).background(Color(uiColor: .systemGroupedBackground))
            .navigationTitle("Write to terminal").navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { photoTask?.cancel(); dismiss() }
                        .accessibilityHint(uploadingPhoto ? "Keeps your draft and cancels the unfinished photo upload" : "Keeps your draft")
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Insert", systemImage: "arrow.up.to.line") {
                        guard !composingText, !uploadingPhoto else { return }
                        if connection.insertDraft() { dismiss() }
                        else { deliveryError = "Trill could not insert the text. Your draft is still here. Reconnect and try again." }
                    }
                    .disabled(composingText || uploadingPhoto || !connection.connected || connection.draftValidationMessage != nil)
                    .accessibilityIdentifier("terminal-insert")
                }
            }
        }
        .presentationDetents([.large])
        .onChange(of: connection.draftText) { _, _ in deliveryError = nil }
        .sheet(item: $photoConsentRequest, onDismiss: {
            let granted = photoConsentGranted
            photoConsentGranted = false
            if granted, photoComputer == store.computer, !store.changingComputer, !store.readOnly { showPhotoPicker = true }
        }) { request in
            let computer = request.computer
            SharingConsentView(title: "Add a photo", destination: computer.name,
                message: SharingDisclosure.photo + "\n\nThis permission is remembered for this computer. You still choose every photo. Reset it in Connection details.",
                actionTitle: "Allow photos for this computer", allow: {
                    guard store.computer == computer, !store.changingComputer, !store.readOnly else { return }
                    photoConsent = PhotoConsentPreference.remembering(computer, in: photoConsent)
                    photoConsentGranted = true
                })
        }
        .photosPicker(isPresented: $showPhotoPicker, selection: $pickedPhoto, matching: .images)
        .onChange(of: pickedPhoto) { _, photo in
            guard let photo, let api = store.api, let computer = photoComputer,
                  computer == store.computer, PhotoConsentPreference.contains(computer, in: photoConsent),
                  !store.changingComputer, !store.readOnly else { pickedPhoto = nil; return }
            photoTask?.cancel()
            let requestID = UUID()
            photoRequestID = requestID
            uploadingPhoto = true
            photoMessage = nil
            photoPreview = nil
            photoTask = Task { @MainActor in
                defer {
                    // An older cancelled request must not clear a newer picker selection.
                    if photoRequestID == requestID {
                        uploadingPhoto = false
                        pickedPhoto = nil
                        photoTask = nil
                        photoRequestID = nil
                    }
                }
                do {
                    guard let data = try await photo.loadTransferable(type: Data.self) else {
                        throw TrillError.message("This photo couldn't be loaded. Choose another image.")
                    }
                    try Task.checkCancellation()
                    let jpeg = try TerminalPhoto.jpeg(from: data)
                    photoPreview = UIImage(data: jpeg)
                    guard store.api === api, store.computer == computer, !store.changingComputer, !store.readOnly,
                          PhotoConsentPreference.contains(computer, in: photoConsent) else {
                        throw TrillError.message("Photo permission or the connection changed. Add the photo again when you’re ready.")
                    }
                    let attachment = try await api.uploadPhoto(id: sessionID, jpeg: jpeg)
                    try Task.checkCancellation()
                    guard photoRequestID == requestID, store.api === api, store.computer == computer,
                          !store.changingComputer, !store.readOnly,
                          PhotoConsentPreference.contains(computer, in: photoConsent),
                          let path = attachment.terminalPath else {
                        throw TrillError.message("The connection changed. Reopen Write before adding this photo again.")
                    }
                    connection.draftText += (connection.draftText.isEmpty ? "" : " ") + path
                    photoMessage = "Photo uploaded. Insert adds its path to the terminal; Return sends it."
                } catch is CancellationError { }
                catch {
                    if !Task.isCancelled, photoRequestID == requestID {
                        photoPreview = nil
                        photoMessage = error.localizedDescription
                    }
                }
            }
        }
        // A covering consent sheet or picker can disappear this view without
        // removing its editor. Cancel on Done or editor teardown instead.
    }

    private var compactTools: Bool { dynamicTypeSize.isAccessibilitySize || verticalSizeClass == .compact }

    private var photoActions: some View {
        HStack(spacing: 12) {
            Button(action: choosePhoto) { Label("Add photo", systemImage: "photo.badge.plus") }
                .fixedSize(horizontal: false, vertical: true)
                .buttonStyle(.bordered)
                .disabled(uploadingPhoto || store.changingComputer || store.readOnly || store.api == nil)
                .accessibilityIdentifier("terminal-add-photo")
            Spacer(minLength: 8)
            Menu {
                Text("Insert adds your draft to the terminal. Return sends it.")
                Text("Use the keyboard microphone to dictate.")
                Text("Photos upload to your Mac. Their paths are added to this draft.")
                Text("Done keeps your draft while Trill is open and cancels unfinished photo uploads.")
            } label: {
                Label("Writing help", systemImage: "info.circle").labelStyle(.iconOnly)
                    .frame(minWidth: 44, minHeight: 44)
            }
        }
    }

    @ViewBuilder private var photoProgress: some View {
        if uploadingPhoto || photoPreview != nil {
            HStack(spacing: 8) {
                if uploadingPhoto { ProgressView("Adding photo…").font(.subheadline).fixedSize(horizontal: false, vertical: true) }
                if let photoPreview {
                    Image(uiImage: photoPreview).resizable().scaledToFit().frame(width: 44, height: 44)
                        .clipShape(RoundedRectangle(cornerRadius: 6)).accessibilityLabel("Selected photo")
                }
            }
        }
    }

    private func choosePhoto() {
        guard let computer = store.computer, !store.changingComputer, !store.readOnly, !uploadingPhoto else { return }
        photoComputer = computer
        photoConsentGranted = false
        if PhotoConsentPreference.contains(computer, in: photoConsent) { showPhotoPicker = true }
        else { photoConsentRequest = SavedComputer(id: store.activeComputerID ?? UUID(), computer: computer) }
    }
    private var validationMessage: String? {
        if !connection.connected { return "Reconnect before inserting. You can keep editing this draft." }
        if composingText { return "Finish the current keyboard entry before inserting." }
        if connection.draftText.isEmpty { return nil }
        return connection.draftValidationMessage
    }
}


struct TerminalDraftEditor: UIViewRepresentable {
    @Binding var text: String
    var compositionChanged: (Bool) -> Void = { _ in }
    var onDismantle: () -> Void = {}

    func makeUIView(context: Context) -> UITextView {
        let view = DraftTextView()
        view.delegate = context.coordinator
        view.text = text
        view.backgroundColor = .clear
        view.font = UIFontMetrics(forTextStyle: .body).scaledFont(for: .monospacedSystemFont(ofSize: 17, weight: .regular))
        view.adjustsFontForContentSizeCategory = true
        view.autocorrectionType = .no
        view.autocapitalizationType = .none
        view.spellCheckingType = .no
        view.smartQuotesType = .no
        view.smartDashesType = .no
        view.smartInsertDeleteType = .no
        view.keyboardType = .default
        view.keyboardDismissMode = .none
        view.isScrollEnabled = true
        view.alwaysBounceVertical = true
        view.accessibilityLabel = "Terminal draft"
        view.accessibilityIdentifier = "terminal-draft"
        view.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        view.setContentCompressionResistancePriority(.defaultLow, for: .vertical)
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        context.coordinator.text = $text
        context.coordinator.compositionChanged = compositionChanged
        context.coordinator.onDismantle = onDismantle
        context.coordinator.synchronize(view)
    }
    static func dismantleUIView(_ view: UITextView, coordinator: Coordinator) {
        view.delegate = nil
        coordinator.onDismantle()
    }
    // Size follows the container, never UITextView's changing content size.
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? 320, height: proposal.height ?? 160)
    }
    func makeCoordinator() -> Coordinator {
        Coordinator(text: $text, compositionChanged: compositionChanged, onDismantle: onDismantle)
    }
    final class Coordinator: NSObject, UITextViewDelegate {
        var text: Binding<String>
        var compositionChanged: (Bool) -> Void
        var onDismantle: () -> Void
        private var lastModelText: String
        private var pendingAppend = ""
        private var applyingModel = false
        private var composing = false

        init(text: Binding<String>, compositionChanged: @escaping (Bool) -> Void = { _ in },
             onDismantle: @escaping () -> Void = {}) {
            self.text = text
            self.lastModelText = text.wrappedValue
            self.compositionChanged = compositionChanged
            self.onDismantle = onDismantle
        }

        func synchronize(_ view: UITextView) {
            captureExternalAppend()
            // Replacing text while marked discards the native IME candidate or
            // dictation entry. Keep photo suffixes in the model until it commits.
            guard view.markedTextRange == nil else { return }
            pendingAppend = ""
            apply(text.wrappedValue, to: view)
        }

        private func captureExternalAppend() {
            let model = text.wrappedValue
            if model != lastModelText, model.hasPrefix(lastModelText) {
                pendingAppend += model.dropFirst(lastModelText.count)
            }
            lastModelText = model
        }

        private func apply(_ value: String, to view: UITextView) {
            guard view.text != value else { return }
            let previous = view.text ?? ""
            let selection = view.selectedRange
            let offset = view.contentOffset
            applyingModel = true
            defer { applyingModel = false }
            if value.hasPrefix(previous) {
                let suffix = String(value.dropFirst(previous.count))
                view.textStorage.append(NSAttributedString(string: suffix, attributes: view.typingAttributes))
            } else {
                view.text = value
            }
            // UIKit selections use UTF-16 offsets, not Swift Character counts.
            // An async append must preserve both the caret and any selected phrase.
            let length = (value as NSString).length
            let start = min(selection.location, length)
            view.selectedRange = NSRange(location: start, length: min(selection.length, length - start))
            if value.hasPrefix(previous) { view.setContentOffset(offset, animated: false) }
        }

        func textViewDidChange(_ view: UITextView) {
            guard !applyingModel else { return }
            // The upload can finish before SwiftUI calls updateUIView.
            captureExternalAppend()
            if view.markedTextRange == nil, !pendingAppend.isEmpty {
                let value = (view.text ?? "") + pendingAppend
                pendingAppend = ""
                apply(value, to: view)
            }
            let value = (view.text ?? "") + pendingAppend
            lastModelText = value
            if text.wrappedValue != value { text.wrappedValue = value }
            let nowComposing = view.markedTextRange != nil
            if composing != nowComposing {
                composing = nowComposing
                compositionChanged(nowComposing)
            }
        }
        func textViewDidChangeSelection(_ view: UITextView) { textViewDidChange(view) }
        func textViewDidEndEditing(_ view: UITextView) { textViewDidChange(view) }
    }
    private final class DraftTextView: UITextView {
        private var requestedFocus = false
        override func didMoveToWindow() {
            super.didMoveToWindow()
            guard window != nil, !requestedFocus else { return }
            requestedFocus = true
            DispatchQueue.main.async { [weak self] in
                guard let self, self.window != nil else { return }
                self.becomeFirstResponder()
            }
        }
    }
}

// Downsample before decoding full pixels, apply orientation, and re-encode without
// location/EXIF metadata. The system picker grants access only to selected images.
enum TerminalPhoto {
    static func jpeg(from data: Data) throws -> Data {
        guard data.count <= 40 * 1024 * 1024,
              let source = CGImageSourceCreateWithData(data as CFData, nil),
              let thumbnail = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceThumbnailMaxPixelSize: 2048,
                kCGImageSourceShouldCacheImmediately: true
              ] as CFDictionary),
              let jpeg = UIImage(cgImage: thumbnail).jpegData(compressionQuality: 0.9),
              jpeg.count <= 10 * 1024 * 1024 else {
            throw TrillError.message("Choose a photo smaller than 40 MB that iOS can open.")
        }
        return jpeg
    }
}
