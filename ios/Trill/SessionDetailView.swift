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
    @State private var requestedInitialConnection = false
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
                    ZStack(alignment: .center) {
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
                ContentUnavailableView {
                    Label(store.readOnly ? "Read-only access" : session.attachable ? "Disconnected" : "Terminal unavailable", systemImage: store.readOnly ? "eye" : "terminal")
                } description: {
                    if store.readOnly { Text("Enable control in your Mac companion to open terminals.") }
                    else if !session.attachable { Text("This session doesn’t have a running terminal.") }
                    else { Text("Reconnect when you’re ready.") }
                } actions: {
                    if !store.readOnly, session.attachable {
                        Button("Reconnect", action: connect).buttonStyle(.borderedProminent)
                    }
                }

            }
        }
        .navigationTitle(connection.isOpen || isFailed ? "" : session.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { terminalToolbar }
        .modifier(TerminalBarBehavior())
        .onAppear {
            connection.terminalDidAppear(surfaceID: terminalSurfaceID)
            connection.setFontSize(fontSize)
            guard !requestedInitialConnection else { return }
            requestedInitialConnection = true
            if session.attachable, !session.isArchived, !store.readOnly, connection.phase == .disconnected {
                connect()
            }
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
            TerminalWriter(connection: connection, sessionID: session.id, sessionName: session.name, projectID: session.projectId, spaceName: session.projectName)
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
                        LabeledContent("Transport", value: connection.transportLabel)
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
                Button("Write & queue", systemImage: "square.and.pencil", action: openWriter)
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
                ToolbarItem(placement: .primaryAction) {
                    Button("Write & queue", systemImage: "square.and.pencil", action: openWriter)
                        .accessibilityHint("Write a message to insert now or queue for later")
                        .accessibilityIdentifier("terminal-write")
                }.visibilityPriority(.high)
                ToolbarItem(placement: .primaryAction) {
                    Button(showWorkspace ? "Hide files and browser" : "Files and browser", systemImage: "sidebar.right") {
                        toggleWorkspace()
                    }
                        .disabled(store.readOnly).accessibilityIdentifier("session-workspace")
                }.visibilityPriority(.high)
                ToolbarOverflowMenu { sessionActions }
            } else {
                ToolbarItemGroup(placement: .primaryAction) {
                    Button("Write & queue", systemImage: "square.and.pencil", action: openWriter)
                        .accessibilityIdentifier("terminal-write")
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
        Button("Close terminal…", systemImage: "xmark", role: .destructive) { store.requestSessionPrompt(.close, sessionID: session.id) }
            .disabled(store.readOnly || !store.supports("close", session: session))
        ControlGroup {
            Button("Smaller text", systemImage: "textformat.size.smaller") { fontSize = max(10, fontSize - 1) }
            Button("Larger text", systemImage: "textformat.size.larger") { fontSize = min(28, fontSize + 1) }
        }
        if connection.isOpen || isFailed {
            Button("Disconnect", systemImage: "network.slash") { Task { await connection.disconnect() } }
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
            await connection.connect(sessionID: request.sessionID, api: api)
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
                    key("tab", name: "Tab", sequence: "\t", repeats: true)
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
                    repeatingKey("delete.left", name: "Backspace", sequence: "\u{7f}")
                    repeatingKey("arrow.up", name: "Up arrow", sequence: "\u{1b}[A")
                    repeatingKey("arrow.down", name: "Down arrow", sequence: "\u{1b}[B")
                    repeatingKey("arrow.left", name: "Left arrow", sequence: "\u{1b}[D")
                    repeatingKey("arrow.right", name: "Right arrow", sequence: "\u{1b}[C")
                    key("^C", name: "Interrupt, Control C", sequence: "\u{03}")
                }.disabled(!connection.connected)
            }.padding(.horizontal, 8).padding(.vertical, 3)
        }.scrollIndicators(.hidden).background(.bar)
    }
    func key(_ title: String, name: String, sequence: String, repeats: Bool = false) -> some View {
        Button { connection.key(sequence) } label: { Text(title).font(.system(size: 17, weight: .medium, design: .monospaced)).frame(minWidth: 48, minHeight: 44) }.buttonRepeatBehavior(repeats ? .enabled : .disabled).accessibilityLabel(name).accessibilityShowsLargeContentViewer { Label(name, systemImage: "keyboard") }
    }
    func repeatingKey(_ symbol: String, name: String, sequence: String) -> some View {
        Button { connection.key(sequence) } label: { Image(systemName: symbol).font(.system(size: 17, weight: .medium)).frame(minWidth: 44, minHeight: 44) }.buttonRepeatBehavior(.enabled).accessibilityLabel(name).accessibilityIdentifier("terminal-" + symbol.replacingOccurrences(of: ".", with: "-")).accessibilityShowsLargeContentViewer { Label(name, systemImage: "keyboard") }
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
/// draft. SwiftTerm still owns direct terminal input; only Insert sends the draft.
struct TerminalWriter: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Bindable var connection: TerminalConnection
    let sessionID: String
    let sessionName: String
    let projectID: String
    let spaceName: String
    @State private var showingQueue = false
    @State private var deliveryError: String?
    @State private var composingText = false
    @State private var loadingPhoto = false
    @State private var uploadingPhotos = false
    @State private var insertTask: Task<Void, Never>?
    @State private var photoConsentRequest: SavedComputer?
    @State private var photoConsentGranted = false
    @AppStorage(PhotoConsentPreference.storageKey) private var photoConsent = ""

    private var queuedCount: Int {
        store.activeComputerID.map { store.ideasQueue.ideas(computerID: $0, projectID: projectID).filter { !$0.completed }.count } ?? 0
    }
    private var hasContent: Bool {
        !connection.draftText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !connection.draftPhotos.isEmpty
    }

    var body: some View {
        VStack(spacing: 0) {
            composerHeader
            Picker("Write and queue", selection: $showingQueue) {
                Text("Write").tag(false)
                Text("Ideas · \(queuedCount)").tag(true)
            }
            .pickerStyle(.segmented).disabled(uploadingPhotos || loadingPhoto)
            .padding(.horizontal, 20).padding(.bottom, 12)
            Divider()
            if showingQueue, let computerID = store.activeComputerID {
                IdeasQueueView(computerID: computerID, projectID: projectID, spaceName: spaceName,
                               useInDraft: { idea in
                    connection.draftText += (connection.draftText.isEmpty ? "" : "\n\n") + idea.text
                    connection.draftPhotos += (idea.photos ?? []).map { IdeaPhoto(jpegData: $0.jpegData) }
                    showingQueue = false
                }, embedded: true)
            } else {
                GeometryReader { geometry in
                    ScrollView {
                        composer(editorHeight: max(120, min(320, geometry.size.height * 0.45)))
                    }
                    .scrollDismissesKeyboard(.interactively)
                    .frame(width: geometry.size.width, height: geometry.size.height)
                    .clipped()
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(uiColor: .systemGroupedBackground))
        .alert("Couldn't complete action", isPresented: Binding(get: { deliveryError != nil }, set: { if !$0 { deliveryError = nil } })) {
            Button("OK", role: .cancel) { deliveryError = nil }
        } message: { Text(deliveryError ?? "") }
        .presentationDetents([.large])
        .interactiveDismissDisabled(uploadingPhotos)
        .onDisappear { insertTask?.cancel() }
        .sheet(item: $photoConsentRequest, onDismiss: {
            if photoConsentGranted { photoConsentGranted = false; uploadAndInsert() }
        }) { request in
            SharingConsentView(title: "Upload photos", destination: request.computer.name,
                               message: SharingDisclosure.photo, actionTitle: "Allow photos for this computer", allow: {
                guard store.computer == request.computer, !store.changingComputer, !store.readOnly else { return }
                photoConsent = PhotoConsentPreference.remembering(request.computer, in: photoConsent)
                photoConsentGranted = true
            })
        }
    }

    private var composerHeader: some View {
        VStack(spacing: 8) {
            if dynamicTypeSize.isAccessibilitySize {
                Text("Write & queue").font(.headline).frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack(spacing: 12) {
                Button("Done") { insertTask?.cancel(); dismiss() }
                    .disabled(uploadingPhotos)
                    .fixedSize(horizontal: true, vertical: false)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("composer-done")
                if dynamicTypeSize.isAccessibilitySize {
                    Spacer(minLength: 0)
                } else {
                    Text("Write & queue").font(.headline)
                        .frame(maxWidth: .infinity).lineLimit(2)
                }
                if !showingQueue {
                    Button("Insert", action: requestInsertion)
                        .disabled(!hasContent || composingText || loadingPhoto || uploadingPhotos || !connection.connected || store.readOnly || connection.draftInsertionPending)
                        .fixedSize(horizontal: true, vertical: false)
                        .frame(minHeight: 44)
                        .accessibilityIdentifier("terminal-insert")
                }
            }
            .buttonStyle(.bordered)
        }
        .padding(.horizontal, 16).padding(.vertical, 8)
    }

    private func composer(editorHeight: CGFloat) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(sessionName.isEmpty ? spaceName : sessionName).font(.headline)
            Text("Insert into the terminal now, or queue an idea for later.")
                .font(.subheadline).foregroundStyle(.secondary)
            TerminalDraftEditor(text: $connection.draftText, compositionChanged: { composingText = $0 })
                .frame(height: editorHeight)
                .frame(maxWidth: .infinity)
                .padding(8)
                .background(Color(uiColor: .secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12))
                .overlay(alignment: .topLeading) {
                    if connection.draftText.isEmpty {
                        Text("Type or dictate…").foregroundStyle(.tertiary).padding(16).allowsHitTesting(false)
                    }
                }
                .disabled(uploadingPhotos || connection.draftInsertionPending)
            IdeaPhotoAttachments(photos: $connection.draftPhotos, loading: $loadingPhoto)
                .disabled(uploadingPhotos || connection.draftInsertionPending)
            if uploadingPhotos { ProgressView("Uploading photos…") }
            Button("Queue idea", systemImage: "text.badge.plus", action: queueIdea)
                .buttonStyle(.borderedProminent)
                .disabled(!hasContent || composingText || loadingPhoto || uploadingPhotos || connection.draftInsertionPending)
                .accessibilityIdentifier("queue-draft")
            Text("Queued ideas and photos are saved on this device. Insert uploads photos to your Mac; Return sends the message.")
                .font(.footnote).foregroundStyle(.secondary)
            if let message = connection.draftValidationMessage, !connection.draftText.isEmpty {
                Text(message).font(.footnote).foregroundStyle(.secondary)
                if connection.controlHeld || connection.shiftHeld {
                    Button("Turn off Ctrl and Shift") { connection.controlHeld = false; connection.shiftHeld = false }
                        .accessibilityIdentifier("terminal-draft-reset-modifiers")
                }
            }
        }.padding(20)
    }

    private func queueIdea() {
        guard let computerID = store.activeComputerID else { return }
        do {
            let idea = SpaceIdea(text: connection.draftText.trimmingCharacters(in: .whitespacesAndNewlines), photos: connection.draftPhotos)
            try store.ideasQueue.save(store.ideasQueue.ideas(computerID: computerID, projectID: projectID) + [idea], computerID: computerID, projectID: projectID)
            connection.draftText = ""
            connection.draftPhotos = []
            showingQueue = true
        } catch { deliveryError = error.localizedDescription }
    }

    private func requestInsertion() {
        guard !composingText, !loadingPhoto, !uploadingPhotos, !store.readOnly, !store.changingComputer else { return }
        if connection.draftPhotos.contains(where: { $0.uploadedPath == nil }), let computer = store.computer,
           !PhotoConsentPreference.contains(computer, in: photoConsent) {
            photoConsentRequest = SavedComputer(id: store.activeComputerID ?? UUID(), computer: computer)
        } else { uploadAndInsert() }
    }

    private func uploadAndInsert() {
        guard !uploadingPhotos, let computer = store.computer, let api = store.api,
              !store.readOnly, !store.changingComputer, connection.connected else { return }
        uploadingPhotos = true
        insertTask = Task { @MainActor in
            defer { uploadingPhotos = false; insertTask = nil }
            do {
                for photo in connection.draftPhotos where photo.uploadedPath == nil {
                    try Task.checkCancellation()
                    guard store.computer == computer, store.api === api, !store.readOnly, !store.changingComputer,
                          PhotoConsentPreference.contains(computer, in: photoConsent) else {
                        throw TrillError.message("The connection or photo permission changed. Your draft is still here.")
                    }
                    let attachment = try await api.uploadPhoto(id: sessionID, jpeg: photo.jpegData)
                    try Task.checkCancellation()
                    guard let path = attachment.terminalPath else { throw TrillError.message("The uploaded photo has no terminal path.") }
                    if let index = connection.draftPhotos.firstIndex(where: { $0.id == photo.id }) {
                        connection.draftPhotos[index].uploadedPath = path
                    }
                }
                guard store.computer == computer, store.api === api, !store.readOnly, !store.changingComputer else {
                    throw TrillError.message("The connection changed. Your draft is still here.")
                }
                for photo in connection.draftPhotos {
                    if let path = photo.uploadedPath, !connection.draftText.contains(path) {
                        connection.draftText += (connection.draftText.isEmpty ? "" : " ") + path
                    }
                }
                if connection.insertDraft() { dismiss() }
                else { deliveryError = connection.draftValidationMessage ?? "Trill couldn't insert the draft. Your text and photos are still here." }
            } catch is CancellationError { }
            catch { deliveryError = error.localizedDescription }
        }
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
        view.autocorrectionType = .yes
        view.autocapitalizationType = .none
        view.spellCheckingType = .yes
        view.inlinePredictionType = .yes
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
