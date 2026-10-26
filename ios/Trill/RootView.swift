import SwiftUI

struct RootView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @State private var preferredSessionColumn = NavigationSplitViewColumn.sidebar
    @State private var sessionColumns = NavigationSplitViewVisibility.all
    @State private var showWorkspace = false
    @State private var showProjects = false
    @State private var workspaceTab: WorkspaceTab = .files
    @State private var previewAddress: String?
    var body: some View {
        @Bindable var store = store
        Group {
            if store.computer == nil {
                NavigationStack { SessionListView(openProjects: openProjects) }
            } else {
                NavigationSplitView(columnVisibility: $sessionColumns, preferredCompactColumn: $preferredSessionColumn) {
                    SessionListView(openProjects: openProjects)
                        .id(store.activeComputerID)
                        .navigationSplitViewColumnWidth(ideal: 320)
                } detail: {
                    if let id = store.selectedID, let session = store.currentSessions.first(where: { $0.id == id }) {
                        SessionDetailView(session: session, connection: store.connection(for: id), showWorkspace: $showWorkspace, workspaceTab: $workspaceTab, previewAddress: $previewAddress)
                            .id("\(store.activeComputerID?.uuidString ?? "unpaired"):\(id)")
                    } else {
                        ContentUnavailableView("Choose a session", systemImage: "terminal", description: Text("Select a session to open its terminal."))
                    }
                }
                .navigationSplitViewStyle(.balanced)
                .inspector(isPresented: Binding(
                    get: { showWorkspace && store.selectedID != nil },
                    set: { showWorkspace = $0 && store.selectedID != nil }
                )) {
                    if showWorkspace, let api = store.api, let id = store.selectedID,
                       let session = store.currentSessions.first(where: { $0.id == id }),
                       let workspace = store.workspace(for: id) {
                        WorkspacePanel(api: api, session: session, tab: $workspaceTab, presented: $showWorkspace, initialAddress: $previewAddress, state: workspace)
                            .id("\(store.activeComputerID?.uuidString ?? "unpaired"):\(id)")
                    }
                }
                .onChange(of: showWorkspace) { _, shown in
                    if shown { withAnimation { sessionColumns = .detailOnly } }
                }
            }
        }
        .task(id: horizontalSizeClass) {
            // A fold transition may need to make room for the inspector, but
            // dismissing it must not change the user's session-sidebar choice.
            await Task.yield()
            if showWorkspace && store.selectedID != nil { sessionColumns = .detailOnly }
            else if store.selectedID == nil { sessionColumns = .all }
        }
        .onChange(of: store.activeComputerID) { _, _ in
            showProjects = false; showWorkspace = false; previewAddress = nil; store.actionError = nil
        }
        .modifier(SessionAlerts(enabled: !showProjects))
        .disabled(store.changingComputer)
        .onChange(of: store.selectedID) { _, id in
            showWorkspace = false; previewAddress = nil
            preferredSessionColumn = id == nil ? .sidebar : .detail
            if id == nil { sessionColumns = .all }
        }
        .onChange(of: store.selectedTab) { _, tab in
            // Selecting or reopening even the already-selected session returns to detail.
            // Done/swipe dismiss first, so they preserve the session-list position instead.
            if tab == .sessions && showProjects {
                if store.selectedID != nil { preferredSessionColumn = .detail }
                showProjects = false
            }
        }
        .sheet(isPresented: $showProjects, onDismiss: { store.selectedTab = .sessions }) {
            NavigationStack {
                ProjectListView()
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { showProjects = false }
                                .accessibilityIdentifier("close-projects")
                        }
                    }
            }
            .id(store.activeComputerID)
            .modifier(SessionAlerts())
            .disabled(store.changingComputer)
        }
        .sheet(isPresented: Binding(get: { store.pairingPresented && !store.connectionDetailsPresented }, set: { store.pairingPresented = $0 })) { PairComputerView() }
        .sheet(isPresented: $store.connectionDetailsPresented) {
            NavigationStack {
                ComputerView()
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { store.connectionDetailsPresented = false } } }
            }
        }
        .sheet(isPresented: $store.newSessionPresented) { NewSessionView() }
        .sheet(isPresented: $store.newSpacePresented) { NewHerdrItemView(isWorktree: false).id(store.activeComputerID) }
        .sheet(isPresented: $store.newWorktreePresented) { NewHerdrItemView(isWorktree: true).id(store.activeComputerID) }
        .sheet(isPresented: $store.computersPresented) { ComputersView() }
    }

    private func openProjects() {
        showWorkspace = false
        store.selectedTab = .projects
        showProjects = true
    }
}

// Keep confirmations on the visible presentation, including the Projects sheet.
private struct SessionAlerts: ViewModifier {
    @Environment(AppStore.self) private var store
    var enabled = true

    func body(content: Content) -> some View {
        @Bindable var store = store
        content
            .alert(store.sessionPrompt?.title ?? "Session", isPresented: Binding(
                get: { enabled && store.sessionPrompt != nil }, set: { if enabled && !$0 { store.sessionPrompt = nil } }
            ), presenting: store.sessionPrompt) { prompt in
                if prompt.action == .rename { TextField("Session name", text: $store.renameDraft) }
                Button("Cancel", role: .cancel) { store.sessionPrompt = nil }
                Button(prompt.button, role: prompt.action == .stop ? .destructive : nil) {
                    let name = store.renameDraft
                    Task { await store.confirm(prompt, name: name) }
                }
                .disabled(!store.canConfirm(prompt) || (prompt.action == .rename && store.renameDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
            } message: { prompt in Text(prompt.message) }
            .alert("Couldn't complete action", isPresented: Binding(
                get: { enabled && store.actionError != nil }, set: { if enabled && !$0 { store.actionError = nil } }
            )) {
                Button("OK", role: .cancel) { store.actionError = nil }
            } message: { Text(store.actionError ?? "") }
    }
}

struct SessionListView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var search = ""
    let openProjects: () -> Void
    var visible: [RemoteSession] {
        store.currentSessions.filter { $0.matchesSearch(search) }
    }
    private var visibleAgents: [RemoteSession] {
        store.agentSessions.filter { store.agentMatchesSearch($0, search: search) }
    }
    private var showingAgents: Bool { store.sessionBrowserMode == .agents }
    private var visibleRoots: [Project] {
        store.spaceRoots.filter { project in
            matches(project) || store.worktrees(in: project.id).contains(where: matches)
        }
    }
    private func matches(_ project: Project) -> Bool {
        (project.provider == "herdr" && project.matchesSearch(search)) || visible.contains { $0.projectId == project.id }
    }
    var body: some View {
        @Bindable var store = store
        Group {
            if store.computer == nil {
                WelcomeView { store.pairingPresented = true }
            } else {
                List(selection: $store.selectedID) {
                    ForEach(store.providerWarnings, id: \.self) { message in
                        Section { Label(message, systemImage: "exclamationmark.triangle").font(.subheadline).foregroundStyle(.secondary) }
                    }
                    if store.loading && store.sessions.isEmpty {
                        Section { ProgressView("Loading sessions…") }
                    }
                    if store.readOnly && store.health != nil {
                        Section { Label("Read-only access", systemImage: "eye").foregroundStyle(.secondary) }
                    }
                    if let error = store.error {
                        Section {
                            Label(error, systemImage: "wifi.exclamationmark").font(.subheadline).foregroundStyle(.secondary)
                            Button("Try again") { Task { await store.refresh() } }
                        } header: { Text("Couldn't refresh") } footer: { Text("The sessions shown here may be out of date.") }
                    }
                    if showingAgents {
                        ForEach(AgentActivityGroup.allCases) { group in
                            let agents = visibleAgents.filter { $0.agentActivityGroup == group }
                            if !agents.isEmpty {
                                Section {
                                    ForEach(agents) { session in
                                        NavigationLink(value: session.id) {
                                            SessionRow(session: session, location: store.agentLocation(for: session))
                                        }
                                            .modifier(SessionRowContextMenu(sessionID: session.id))
                                            .accessibilityIdentifier("agent-session-\(session.id)")
                                    }
                                } header: {
                                    HStack {
                                        Text(group.title)
                                        Spacer()
                                        Text("\(agents.count)").monospacedDigit()
                                    }
                                }
                            }
                        }
                        if visibleAgents.isEmpty && !store.loading && store.error == nil {
                            ContentUnavailableView(search.isEmpty ? "No active agents" : "No matching agents", systemImage: "person.2", description: Text(search.isEmpty ? "Coding agents detected on this computer appear here. Open a session to start one in its terminal." : "Search by agent, session, space, or branch."))
                                .listRowBackground(Color.clear)
                        }
                    } else {
                        ForEach(visibleRoots) { project in
                            if project.provider == "herdr" {
                                HerdrSpaceSections(project: project, search: search)
                            } else {
                                Section {
                                    ForEach(visible.filter { $0.projectId == project.id }) { session in
                                        NavigationLink(value: session.id) { SessionRow(session: session) }
                                            .modifier(SessionRowContextMenu(sessionID: session.id))
                                    }
                                } header: {
                                    SpaceLabel(project: project)
                                }
                            }
                        }
                        if visibleRoots.isEmpty && !store.loading && store.error == nil {
                            ContentUnavailableView(search.isEmpty ? "No current sessions" : "No matching sessions", systemImage: "terminal", description: Text("Use More → Projects and archive to find archived sessions."))
                                .listRowBackground(Color.clear)
                        }
                    }
                }
                .listStyle(.plain)
                .textCase(nil)
                .listSectionSeparator(.hidden)
                .scrollContentBackground(.hidden)
                .background(Color(uiColor: .systemBackground))
                .searchable(text: $search, prompt: showingAgents ? "Agents, spaces, or branches" : "Sessions, projects, or branches")
                .modifier(BrowserSearchScrollEdge())
                .scrollDismissesKeyboard(.interactively)
                .refreshable { await store.refresh() }
                .safeAreaInset(edge: .top, spacing: 0) {
                    VStack(alignment: .leading, spacing: 8) {
                        if store.computers.count > 1 {
                            computerPicker.pickerStyle(.menu).labelsHidden()
                                .frame(minHeight: 44, alignment: .leading)
                        } else if let computer = store.computer {
                            Text(computer.name)
                                .font(.subheadline).foregroundStyle(.secondary)
                                .padding(.vertical, 4)
                        }
                        if dynamicTypeSize.isAccessibilitySize {
                            browserModePicker.pickerStyle(.menu)
                        } else {
                            browserModePicker.pickerStyle(.segmented)
                        }
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 8)
                    .background(.bar)
                }
            }
        }
        .navigationTitle(showingAgents && store.computer != nil ? "Agents" : "Sessions")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button("New session", systemImage: "terminal") { store.newSessionPresented = true }
                        .disabled(store.creatableProjects.isEmpty)
                    if !store.spaceProviders.isEmpty {
                        Button("New space", systemImage: "folder.badge.plus") { store.newSpacePresented = true }
                    }
                    if !store.worktreeProjects.isEmpty {
                        Button("New worktree", systemImage: "arrow.triangle.branch") { store.newWorktreePresented = true }
                    }
                } label: { Label("Create", systemImage: "plus") }
                .disabled(store.computer == nil || store.readOnly || store.busy || (store.creatableProjects.isEmpty && store.spaceProviders.isEmpty))
            }
            if store.computer != nil {
                if #available(iOS 27.0, *) {
                    ToolbarOverflowMenu { browserMenuActions }
                } else {
                    ToolbarItem(placement: .primaryAction) {
                        Menu { browserMenuActions } label: { Label("More", systemImage: "ellipsis.circle") }
                    }
                }
            }
        }
    }
    @ViewBuilder private var browserMenuActions: some View {
        Button("Projects and archive", systemImage: "archivebox", action: openProjects)
            .accessibilityIdentifier("open-projects")
        Button("Computers", systemImage: "laptopcomputer") { store.computersPresented = true }
            .accessibilityIdentifier("saved-computers")
        Button("Connection details", systemImage: "network") { store.connectionDetailsPresented = true }
            .accessibilityIdentifier("connection-details")
    }
    private var browserModePicker: some View {
        @Bindable var store = store
        return Picker("Browse", selection: $store.sessionBrowserMode) {
            Text("Agents").tag(SessionBrowserMode.agents)
            Text("Sessions").tag(SessionBrowserMode.sessions)
        }
        .accessibilityIdentifier("session-browser-mode")
    }
    private var computerPicker: some View {
        Picker("Computer", selection: Binding(
            get: { store.activeComputerID },
            set: { computerID in
                guard let computerID else { return }
                Task {
                    do { try await store.selectComputer(id: computerID) }
                    catch { store.actionError = error.localizedDescription }
                }
            }
        )) {
            ForEach(store.computers) { savedComputer in
                Text(savedComputer.computer.name).tag(Optional(savedComputer.id))
            }
        }
        .disabled(store.busy || store.changingComputer)
        .accessibilityIdentifier("session-computer-picker")
    }

}

private struct BrowserSearchScrollEdge: ViewModifier {
    func body(content: Content) -> some View {
        if #available(iOS 26.0, *) {
            content.scrollEdgeEffectStyle(.hard, for: .bottom)
        } else {
            content
        }
    }
}

private struct HerdrSpaceSections: View {
    @Environment(AppStore.self) private var store
    let project: Project
    let search: String
    private var children: [Project] { store.worktrees(in: project.id).filter { child in
        project.matchesSearch(search) || child.matchesSearch(search) || store.sessions(in: child.id, archived: false).contains { $0.matchesSearch(search) }
    } }
    var body: some View {
        Section {
            terminals(in: project, matchesAncestor: project.matchesSearch(search))
        } header: {
            SpaceLabel(project: project)
        }
        ForEach(children) { child in
            Section {
                terminals(in: child, matchesAncestor: project.matchesSearch(search) || child.matchesSearch(search))
            } header: {
                SpaceLabel(project: child, parentName: project.displayName)
            }
        }
    }
    @ViewBuilder private func terminals(in project: Project, matchesAncestor: Bool) -> some View {
        let sessions = store.sessions(in: project.id, archived: false).filter { matchesAncestor || $0.matchesSearch(search) }
        ForEach(sessions) { session in
            NavigationLink(value: session.id) { SessionRow(session: session, showsBranch: false) }
                .modifier(SessionRowContextMenu(sessionID: session.id))
        }
        if sessions.isEmpty && search.isEmpty { Text("No sessions").font(.subheadline).foregroundStyle(.secondary) }
    }
}

private struct SpaceLabel: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let project: Project
    var parentName: String = ""
    var body: some View {
        let headingLayout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 6))
            : AnyLayout(HStackLayout(alignment: .top, spacing: 10))
        headingLayout {
            VStack(alignment: .leading, spacing: 4) {
                Text(parentName.isEmpty ? project.displayName : "\(parentName) / \(project.displayName)")
                    .font(.headline).foregroundStyle(Color.primary)
                if let branch = project.branch, !branch.isEmpty {
                    Text(branch).font(.footnote.monospaced()).foregroundStyle(Color.secondary)
                }
            }
            if project.focused == true {
                if !dynamicTypeSize.isAccessibilitySize { Spacer(minLength: 4) }
                Text("Focused on Mac").font(.footnote).foregroundStyle(Color.secondary)
            }
        }
        .padding(.vertical, 5)
    }
}

struct ProjectListView: View {
    @Environment(AppStore.self) private var store
    @State private var search = ""
    @State private var openedProject: Project?

    private var visibleProjects: [Project] {
        let query = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return store.browserProjects.filter { project in
            query.isEmpty || "\(project.displayName) \(project.path) \(project.id)".localizedCaseInsensitiveContains(query) ||
            store.sessions.contains { $0.projectId == project.id && $0.matchesSearch(query) }
        }
    }

    var body: some View {
        Group {
            if store.computer == nil {
                WelcomeView { store.pairingPresented = true }
            } else {
                List {
                    BrowserStatusSection()
                    Section {
                        ForEach(visibleProjects) { project in
                            NavigationLink(value: project.id) {
                                VStack(alignment: .leading, spacing: 6) {
                                    Label(project.displayName, systemImage: "folder").font(.headline)
                                    if !project.path.isEmpty {
                                        Text(project.path)
                                            .font(.caption).foregroundStyle(.secondary)
                                    }
                                    Text("\(store.sessions(in: project.id, archived: false).count) current · \(store.sessions(in: project.id, archived: true).count) archived")
                                        .font(.subheadline).foregroundStyle(.secondary)
                                }.padding(.vertical, 4)
                            }
                            .contextMenu {
                                Button("Open project", systemImage: "folder") { openedProject = project }
                            }
                        }
                    }
                    if visibleProjects.isEmpty && !store.loading && store.error == nil {
                        ContentUnavailableView(search.isEmpty ? "No projects yet" : "No matching projects", systemImage: "folder", description: Text(search.isEmpty ? "Your projects and Herdr workspaces appear here, even if they have no sessions." : "Search by project, session, agent, or branch."))
                            .listRowBackground(Color.clear)
                    }
                }
                .listStyle(.insetGrouped)
                .searchable(text: $search, prompt: "Projects, sessions, or branches")
                .refreshable { await store.refresh() }
                .navigationDestination(for: String.self) { ProjectSessionsView(projectID: $0) }
                .navigationDestination(item: $openedProject) { ProjectSessionsView(projectID: $0.id) }
            }
        }
        .navigationTitle("Projects")
    }
}

struct ProjectSessionsView: View {
    @Environment(AppStore.self) private var store
    let projectID: String
    @State private var search = ""

    private var project: Project? { store.browserProjects.first { $0.id == projectID } }
    private var current: [RemoteSession] { store.sessions(in: projectID, archived: false).filter { $0.matchesSearch(search) } }
    private var archived: [RemoteSession] { store.sessions(in: projectID, archived: true).filter { $0.matchesSearch(search) } }

    var body: some View {
        List {
            BrowserStatusSection()
            if let project {
                Section {
                    LabeledContent("Project", value: project.displayName)
                    if !project.path.isEmpty { Text(project.path).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled) }
                }
            }
            Section("Current") {
                ForEach(current) { session in
                    Button {
                        store.selectSession(id: session.id)
                    } label: {
                        SessionRow(session: session).frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint("View this session in Sessions")
                    .modifier(SessionRowContextMenu(sessionID: session.id))
                }
                if current.isEmpty && !store.loading && store.error == nil {
                    Text(search.isEmpty ? "No current sessions" : "No matching current sessions").foregroundStyle(.secondary)
                }
            }
            Section {
                ForEach(archived) { session in
                    // Metadata only: the terminal surface lives exclusively in Sessions.
                    DisclosureGroup {
                        ArchivedSessionPreview(sessionID: session.id)
                    } label: { SessionRow(session: session) }
                    .modifier(SessionRowContextMenu(sessionID: session.id))
                }
                if archived.isEmpty && !store.loading && store.error == nil {
                    Text(search.isEmpty ? "No archived sessions" : "No matching archived sessions").foregroundStyle(.secondary)
                }
            } header: { Text("Archived") } footer: {
                Text("Reopen a session to move it back to Sessions. Choose Open terminal when you want to connect.")
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle(project?.displayName ?? "Project")
        .searchable(text: $search, prompt: "Sessions, agents, or branches")
        .refreshable { await store.refresh() }
    }
}

private struct SessionRowContextMenu: ViewModifier {
    @Environment(AppStore.self) private var store
    let sessionID: String
    private var session: RemoteSession? { store.sessions.first { $0.id == sessionID } }
    private var mutationsDisabled: Bool { store.readOnly || store.busy || store.changingComputer }

    func body(content: Content) -> some View {
        content.contextMenu {
            if let session {
                if session.isArchived {
                    if session.resumable && store.supports("resume", session: session) {
                        Button("Reopen session", systemImage: "arrow.counterclockwise") { resume() }.disabled(mutationsDisabled)
                    }
                } else {
                    Button("Open session", systemImage: "terminal") { store.selectSession(id: sessionID) }.disabled(store.changingComputer)
                    Button("Rename…", systemImage: "pencil") { store.requestSessionPrompt(.rename, sessionID: sessionID) }.disabled(mutationsDisabled || !store.supports("rename", session: session))
                    Button("Archive session…", systemImage: "archivebox") { store.requestSessionPrompt(.archive, sessionID: sessionID) }.disabled(mutationsDisabled || !store.supports("archive", session: session))
                    if session.attachable && store.supports("stop", session: session) {
                        Button("Stop session…", systemImage: "stop.circle", role: .destructive) { store.requestSessionPrompt(.stop, sessionID: sessionID) }.disabled(mutationsDisabled)
                    } else if session.resumable && store.supports("resume", session: session) {
                        Button("Resume session", systemImage: "play") { resume() }.disabled(mutationsDisabled)
                    }
                }
            }
        }
    }
    private func resume() {
        let owner = store.activeComputerID
        Task {
            guard owner == store.activeComputerID, !mutationsDisabled, let session, session.resumable else { return }
            await store.action("resume", session: session)
        }
    }
}

private struct ArchivedSessionPreview: View {
    @Environment(AppStore.self) private var store
    let sessionID: String

    var body: some View {
        // Resolve by ID on every refresh; never act on a navigation snapshot.
        if let session = store.sessions.first(where: { $0.id == sessionID }), session.isArchived {
            LabeledContent("Status", value: session.statusLabel)
            LabeledContent("Agent", value: session.displayAgent)
            if !session.branch.isEmpty { LabeledContent("Branch", value: session.branch) }
            if let updated = ISO8601DateFormatter().date(from: session.updatedAt) {
                LabeledContent("Updated") { Text(updated, format: .dateTime.day().month().year().hour().minute()) }
            }
            if session.resumable && store.supports("resume", session: session) {
                Button {
                    Task { await store.action("resume", session: session) }
                } label: {
                    Label("Reopen session", systemImage: "arrow.counterclockwise")
                }
                .disabled(store.readOnly || store.busy)
                .accessibilityIdentifier("reopen-session-\(session.id)")
            } else {
                Text("This session has no saved agent history to resume.").font(.footnote).foregroundStyle(.secondary)
            }
        }
    }
}

private struct BrowserStatusSection: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        ForEach(store.providerWarnings, id: \.self) { message in
            Section { Label(message, systemImage: "exclamationmark.triangle").font(.subheadline).foregroundStyle(.secondary) }
        }
        if store.loading {
            Section { HStack(spacing: 10) { ProgressView(); Text("Refreshing projects and sessions…").foregroundStyle(.secondary) } }
        }
        if store.readOnly {
            Section {
                Label("Read-only access", systemImage: "eye")
                Text("You can browse all sessions. Enable control on your computer to reopen archived sessions.")
                    .font(.footnote).foregroundStyle(.secondary)
            }
        }
        if let error = store.error {
            Section {
                Label(error, systemImage: "wifi.exclamationmark").font(.subheadline).foregroundStyle(.secondary)
                Button("Try again") { Task { await store.refresh() } }.disabled(store.loading)
            } header: { Text("Couldn't complete request") } footer: { Text("The projects and sessions shown here may be out of date.") }
        }
    }
}

// Presentation only: terminal availability and archive membership do not imply activity.
private struct SessionRowStatus {
    let label: String
    let color: Color
    let isFilled: Bool

    init(session: RemoteSession) {
        if ["error", "failed"].contains(session.status) {
            (label, color, isFilled) = (session.status.capitalized, .red, true)
        } else if ["completed", "stopped"].contains(session.status) {
            // Ended sessions can retain stale activity from their last live update.
            (label, color, isFilled) = (session.status.capitalized, .secondary, false)
        } else if ["error", "failed"].contains(session.activityState) {
            (label, color, isFilled) = (session.activityState.capitalized, .red, true)
        } else if session.needsAttention {
            (label, color, isFilled) = (session.statusLabel, .orange, true)
        } else if session.status == "running" && session.activityState == "working" {
            (label, color, isFilled) = (session.statusLabel, .green, true)
        } else if (session.provider == "herdr" || session.hasDetectedAgent) && session.status == "running" && session.agentActivityGroup == .idle {
            (label, color, isFilled) = ("Idle", .green, false)
        } else if session.status == "running" {
            (label, color, isFilled) = (session.statusLabel, .blue, true)
        } else {
            // In particular, "finished" is not necessarily an ended session.
            (label, color, isFilled) = (session.statusLabel, .secondary, false)
        }
    }
}

struct SessionRow: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @ScaledMetric(relativeTo: .headline) private var statusDotTopPadding = 6
    let session: RemoteSession
    var showsBranch = true
    var location: String = ""
    var body: some View {
        let status = SessionRowStatus(session: session)
        HStack(alignment: .top, spacing: 12) {
            Circle()
                .strokeBorder(status.color, lineWidth: 1.5)
                .background(Circle().fill(status.isFilled ? status.color : .clear))
                .frame(width: 9, height: 9)
                .padding(.top, statusDotTopPadding)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 6) {
                Text(session.name.isEmpty ? "Untitled session" : session.name)
                    .font(.headline)
                    .foregroundStyle(.primary)
                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : 2)
                if !location.isEmpty || (showsBranch && !session.branch.isEmpty) {
                    let contextLayout = dynamicTypeSize.isAccessibilitySize
                        ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4))
                        : AnyLayout(HStackLayout(spacing: 6))
                    contextLayout {
                        if !location.isEmpty {
                            Text(location)
                        }
                        if showsBranch && !session.branch.isEmpty {
                            if !location.isEmpty && !dynamicTypeSize.isAccessibilitySize {
                                Text("/").accessibilityHidden(true)
                            }
                            Text(session.branch).font(.footnote.monospaced())
                                .truncationMode(.middle)
                        }
                    }
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : 1)
                }
                Text(metadata(status: status))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .frame(minHeight: 44, alignment: .leading)
        .padding(.vertical, 8)
        .accessibilityElement(children: .combine)
    }
    private func metadata(status: SessionRowStatus) -> String {
        var components = [status.label, session.displayAgent]
        if session.provider != "herdr", let provider = session.providerName {
            components.append(provider)
        }
        return components.joined(separator: " · ")
    }
}

struct WelcomeView: View {
    let pair: () -> Void
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 28) {
                Image("TrillWave").resizable().scaledToFit().frame(width: 48, height: 32).foregroundStyle(Color.accentColor)
                    .frame(width: 80, height: 80).background(Color(uiColor: .secondarySystemBackground), in: RoundedRectangle(cornerRadius: 24)).accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 12) {
                    Text("Your Mac's terminal.\nOn your iPhone.").font(.largeTitle.bold())
                    Text("Open your Xirp and Herdr sessions from your phone. Connect to the terminal already running on your Mac.")
                        .font(.body).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                VStack(alignment: .leading, spacing: 20) {
                    FeatureRow(symbol: "terminal", title: "Your existing terminal", text: "Keep working in the terminal already running on your Mac.")
                    FeatureRow(symbol: "antenna.radiowaves.left.and.right", title: "Reconnect with Mosh", text: "Mosh reconnects when you return to the app.")
                    FeatureRow(symbol: "lock.shield", title: "Direct to your computer", text: "Connect over your local network or a VPN you have set up.")
                }
                Button(action: pair) { Text("Pair a computer").frame(maxWidth: .infinity).padding(.vertical, 6) }
                    .buttonStyle(.borderedProminent).controlSize(.large).accessibilityIdentifier("pair-computer")
                Text("Run Trill setup on your Mac to create a pairing code.")
                    .font(.footnote).foregroundStyle(.secondary)
                HStack(spacing: 24) {
                    NavigationLink("Privacy policy") { PrivacyPolicyView() }.accessibilityIdentifier("privacy-policy")
                    NavigationLink("Support") { SupportView() }.accessibilityIdentifier("support")
                }.font(.footnote)
            }.padding(28).frame(maxWidth: 500, alignment: .leading).frame(maxWidth: .infinity)
        }
    }
}
struct FeatureRow: View {
    let symbol: String; let title: String; let text: String
    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: symbol).font(.title3).foregroundStyle(Color.accentColor).frame(width: 26)
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.headline)
                Text(text).font(.subheadline).foregroundStyle(.secondary)
            }
        }.accessibilityElement(children: .combine)
    }
}
