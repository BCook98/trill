import SwiftUI
import UIKit

enum AppTab: Hashable { case sessions, projects }
enum SessionBrowserMode: Hashable { case sessions, agents }

@MainActor @Observable
final class AppStore {
    var computer: Computer?
    private var library = ComputerLibrary()
    private var libraryAvailable = true
    private var backgroundHandoffToken: UUID?
    private var backgroundHandoffTaskID = UIBackgroundTaskIdentifier.invalid
    var computers: [SavedComputer] { library.computers }
    var activeComputerID: UUID? { library.activeID }
    private(set) var changingComputer = false
    var computersPresented = false
    private var drafts: [UUID: [String: String]] = [:]
    private var selections: [UUID: String] = [:]
    var sessionBrowserMode: SessionBrowserMode = .sessions
    var sessions: [RemoteSession] = []
    var projects: [Project] = []
    var providerWarnings: [String] = []
    var spaceProviders: [ProviderStatus] = []
    var newSpacePresented = false
    var newWorktreePresented = false
    var worktreeProjects: [Project] {
        spaceRoots.filter { $0.provider == "herdr" && $0.isWorktree != true && $0.parentId == nil && $0.capabilities?.worktree == true }
    }
    var hasHerdr: Bool { projects.contains { $0.provider == "herdr" } || !spaceProviders.isEmpty }
    var creatableProjects: [Project] { projects.filter(\.canCreate) }
    var sessionManagementAvailable: Bool { health?.sessionManagement ?? true }
    func supports(_ action: String, session: RemoteSession) -> Bool {
        guard let caps = session.capabilities else { return sessionManagementAvailable }
        switch action {
        case "rename": return caps.rename
        case "stop": return caps.stop
        case "archive": return caps.archive
        case "resume": return caps.resume
        default: return false
        }
    }
    var health: Health?
    var loading = false
    var busy = false
    var error: String?
    // Action failures survive successful background refreshes and open terminals.
    var actionError: String?
    var sessionPrompt: SessionPrompt?
    var renameDraft = ""
    var refreshedAt: Date?
    var selectedID: String?
    var selectedTab = AppTab.sessions
    var pendingEnrollment: Computer?
    private(set) var pendingEnrollmentSaved = false
    var pairingLink: String?
    var pairingPresented = false
    var newSessionPresented = false
    var connectionDetailsPresented = false
    private(set) var api: APIClient?
    private var connections: [String: TerminalConnection] = [:]
    @ObservationIgnored private var workspaces: [String: WorkspaceState] = [:]
    private var refreshGeneration = UUID()
    private var refreshTask: Task<Bool, Never>?

    private let saveLibrary: (ComputerLibrary) throws -> Void
    private let savePairing: (Computer, String) throws -> Void
    private let deletePairing: (String) throws -> Void
    private let checkHealth: (APIClient) async throws -> Health
    private let fetchSessions: (APIClient) async throws -> [RemoteSession]
    private let fetchProjects: (APIClient) async throws -> [Project]
    private let performAction: (APIClient, String, String) async throws -> Void

    init(loadSavedPairing: Bool = true,
         savePairing: @escaping (Computer, String) throws -> Void = { try PairingStore.save($0, account: $1) },
         deletePairing: @escaping (String) throws -> Void = { try PairingStore.delete(account: $0) },
         loadLibrary: () throws -> ComputerLibrary? = { try ComputerLibraryStore.load() },
         saveLibrary: @escaping (ComputerLibrary) throws -> Void = { try ComputerLibraryStore.save($0) },
         loadPairing: (String) -> Computer? = { PairingStore.load(account: $0) },
         checkHealth: @escaping (APIClient) async throws -> Health = { try await $0.health() },
         fetchSessions: @escaping (APIClient) async throws -> [RemoteSession] = { try await $0.sessions() },
         fetchProjects: @escaping (APIClient) async throws -> [Project] = { try await $0.projects() },
         performAction: @escaping (APIClient, String, String) async throws -> Void = { try await $0.action($1, id: $2) }) {
        self.saveLibrary = saveLibrary
        self.savePairing = savePairing
        self.deletePairing = deletePairing
        self.checkHealth = checkHealth
        self.fetchSessions = fetchSessions
        self.fetchProjects = fetchProjects
        self.performAction = performAction
        guard loadSavedPairing else { return }
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("--test-onboarding") { return }
        #endif
        do {
            if let saved = try loadLibrary() {
                try saved.validate(); library = saved
            } else if let previous = loadPairing("primary") {
                var migrated = ComputerLibrary()
                _ = migrated.upsert(previous)
                try saveLibrary(migrated)
                library = migrated
            }
            computer = library.selected?.computer
            // The library is authoritative only after its atomic save succeeds.
            // An empty library also prevents a forgotten legacy pairing returning.
            if !library.computers.isEmpty { try? deletePairing("primary") }
        } catch {
            libraryAvailable = false
            self.error = "Saved computers could not be opened. Unlock your device and reopen Trill. " + error.localizedDescription
        }
        pendingEnrollment = loadPairing("pending-enrollment")
        if pendingEnrollment == computer {
            pendingEnrollment = nil
            try? deletePairing("pending-enrollment")
        }
        pendingEnrollmentSaved = pendingEnrollment != nil
        pairingPresented = pendingEnrollment != nil
        if let computer { api = try? APIClient(computer: computer) }
    }
    var readOnly: Bool { health?.readOnly ?? true }
    var currentSessions: [RemoteSession] { sessions.filter { !$0.isArchived } }
    var archiveSessions: [RemoteSession] { sessions.filter(\.isArchived) }

    var agentSessions: [RemoteSession] {
        currentSessions.filter(\.hasDetectedAgent).sorted { first, second in
            if first.agentActivityGroup != second.agentActivityGroup {
                return first.agentActivityGroup.rawValue < second.agentActivityGroup.rawValue
            }
            let firstLocation = agentLocation(for: first)
            let secondLocation = agentLocation(for: second)
            let locationOrder = firstLocation.localizedStandardCompare(secondLocation)
            if locationOrder != .orderedSame { return locationOrder == .orderedAscending }
            let nameOrder = first.name.localizedStandardCompare(second.name)
            return nameOrder == .orderedSame ? first.id < second.id : nameOrder == .orderedAscending
        }
    }
    func agentLocation(for session: RemoteSession) -> String {
        guard let project = projects.first(where: { $0.id == session.projectId }) else { return session.projectName }
        if let parentID = project.parentId, let parent = projects.first(where: { $0.id == parentID }) {
            return "\(parent.displayName) / \(project.displayName)"
        }
        return project.displayName
    }
    func agentMatchesSearch(_ session: RemoteSession, search: String) -> Bool {
        let query = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return session.matchesSearch(query) || agentLocation(for: session).localizedCaseInsensitiveContains(query)
            || (session.providerName?.localizedCaseInsensitiveContains(query) ?? false)
    }

    // Keep catalog projects with no sessions, and retain access to orphaned sessions.
    // Identity always comes from projectId, never the display name.
    var browserProjects: [Project] {
        var result = projects
        var ids = Set(projects.map(\.id))
        for session in sessions where ids.insert(session.projectId).inserted {
            result.append(Project(id: session.projectId, name: session.projectName, path: ""))
        }
        return result.sorted {
            let order = $0.displayName.localizedStandardCompare($1.displayName)
            return order == .orderedSame ? $0.id < $1.id : order == .orderedAscending
        }
    }
    var spaceRoots: [Project] {
        let catalog = browserProjects
        let ids = Set(catalog.map(\.id))
        return catalog.filter { project in
            guard let parentID = project.parentId else { return true }
            return !ids.contains(parentID)
        }.sorted(by: spaceOrder)
    }
    func worktrees(in projectID: String) -> [Project] {
        browserProjects.filter { $0.parentId == projectID }.sorted(by: spaceOrder)
    }
    private func spaceOrder(_ first: Project, _ second: Project) -> Bool {
        if first.provider == "herdr", second.provider == "herdr", first.providerName == second.providerName,
           let firstOrder = first.workspaceOrder, let secondOrder = second.workspaceOrder, firstOrder != secondOrder {
            return firstOrder < secondOrder
        }
        let firstProvider = first.providerName ?? ""
        let secondProvider = second.providerName ?? ""
        if firstProvider != secondProvider { return firstProvider.localizedStandardCompare(secondProvider) == .orderedAscending }
        let comparison = first.displayName.localizedStandardCompare(second.displayName)
        return comparison == .orderedSame ? first.id < second.id : comparison == .orderedAscending
    }
    func createHerdrItem(isWorktree: Bool, targetID: String, name: String, directoryOrBranch: String) async throws {
        guard let api, !busy, !readOnly, !changingComputer else { throw SessionCreationPreflightError.unavailable }
        if isWorktree {
            guard worktreeProjects.contains(where: { $0.id == targetID }) else { throw SessionCreationPreflightError.unsupportedProject }
        } else {
            guard spaceProviders.contains(where: { $0.id == targetID }) else { throw SessionCreationPreflightError.unsupportedProject }
        }
        busy = true; defer { busy = false }
        if isWorktree { try await api.createWorktree(projectID: targetID, name: name, branch: directoryOrBranch) }
        else { try await api.createSpace(providerID: targetID, name: name, directory: directoryOrBranch) }
        if let refreshTask { _ = await refreshTask.value }
        guard await refresh() else { throw TrillError.message("Created on your computer, but the list could not refresh. Refresh Spaces before trying again.") }
    }
    func sessions(in projectID: String, archived: Bool) -> [RemoteSession] {
        sessions.filter { $0.projectId == projectID && $0.isArchived == archived }
    }
    @discardableResult
    func selectSession(id: String) -> Bool {
        guard currentSessions.contains(where: { $0.id == id }) else { return false }
        selectedID = id
        selectedTab = .sessions
        return true
    }
    func connection(for id: String) -> TerminalConnection {
        if let existing = connections[id] { return existing }
        let new = TerminalConnection()
        if let activeComputerID { new.draftText = drafts[activeComputerID]?[id] ?? "" }
        connections[id] = new
        return new
    }
    func workspace(for id: String) -> WorkspaceState? {
        guard let api, !changingComputer else { return nil }
        if let existing = workspaces[id] { return existing }
        let state = WorkspaceState(api: api, sessionID: id)
        workspaces[id] = state
        return state
    }
    private func beginComputerChange() throws {
        guard libraryAvailable else { throw TrillError.message("Saved computers are unavailable. Unlock your device and reopen Trill before changing pairing.") }
        guard !busy else { throw TrillError.message("Wait for the current operation to finish before changing computers.") }
        busy = true; changingComputer = true
    }
    func pair(_ computer: Computer) async throws {
        try beginComputerChange()
        defer { busy = false; changingComputer = false }
        let client = try APIClient(computer: computer)
        let health = try await checkHealth(client)
        var next = library
        let pairedID = next.upsert(computer)
        try saveLibrary(next)
        if let previous = library.computers.first(where: { $0.id == pairedID }) {
            TerminalConsentPreference.clear(previous.computer)
            PhotoConsentPreference.clear(previous.computer)
        }
        if let paired = next.selected { TerminalConsentPreference.clear(paired.computer); PhotoConsentPreference.clear(paired.computer) }
        await activate(next, client: client, health: health)
    }
    func selectComputer(id: UUID) async throws {
        guard library.activeID != id else { return }
        try beginComputerChange()
        defer { busy = false; changingComputer = false }
        guard let selected = library.computers.first(where: { $0.id == id }) else { throw TrillError.message("This computer is no longer saved.") }
        let client = try APIClient(computer: selected.computer)
        var next = library; next.activeID = id
        try saveLibrary(next)
        // Selecting an offline computer is still allowed. Refresh explains the
        // connection failure, and the other saved computers remain available.
        await activate(next, client: client, health: nil)
    }
    func forgetComputer(id: UUID) async throws {
        try beginComputerChange()
        defer { busy = false; changingComputer = false }
        guard let forgotten = library.computers.first(where: { $0.id == id }) else { return }
        var next = library; next.remove(id: id)
        let client = try next.selected.map { try APIClient(computer: $0.computer) }
        try saveLibrary(next)
        TerminalConsentPreference.clear(forgotten.computer)
        PhotoConsentPreference.clear(forgotten.computer)
        if library.activeID == id { await activate(next, client: client, health: nil) }
        else { library = next }
        drafts[id] = nil; selections[id] = nil
    }
    private func activate(_ next: ComputerLibrary, client: APIClient?, health: Health?) async {
        refreshGeneration = UUID()
        for workspace in workspaces.values { workspace.close() }
        workspaces.removeAll()
        sessionPrompt = nil; renameDraft = ""; actionError = nil
        refreshTask?.cancel()
        if let oldID = library.activeID {
            selections[oldID] = selectedID
            for (id, connection) in connections { drafts[oldID, default: [:]][id] = connection.draftText }
        }
        await closeConnections()
        if let refreshTask { _ = await refreshTask.value }
        library = next
        computer = next.selected?.computer; api = client; self.health = health
        sessions = []; projects = []; providerWarnings = []; spaceProviders = []; newSpacePresented = false; newWorktreePresented = false; selectedID = nil; selectedTab = .sessions
        refreshedAt = nil; error = nil; actionError = nil
        // Other callers remain paused until activation and its initial poll finish.
        _ = await refreshCurrentComputer()
        if let id = next.activeID, let selection = selections[id], currentSessions.contains(where: { $0.id == selection }) {
            selectedID = selection
        }
    }
    func pairEnrollment(_ computer: Computer) async throws {
        guard !busy else { throw TrillError.message("Wait for the current operation to finish before pairing another computer.") }
        // Keep the issued credential recoverable even if Keychain or the next
        // network request fails. The currently paired computer changes only once
        // its replacement passes health verification and primary persistence.
        if pendingEnrollment != computer { pendingEnrollmentSaved = false }
        pendingEnrollment = computer
        try savePairing(computer, "pending-enrollment")
        pendingEnrollmentSaved = true
        try await pair(computer)
        try? deletePairing("pending-enrollment")
        pendingEnrollment = nil
        pendingEnrollmentSaved = false
    }
    func discardPendingEnrollment() throws {
        try deletePairing("pending-enrollment")
        pendingEnrollment = nil
        pendingEnrollmentSaved = false
    }
    func forget() async {
        guard let activeComputerID else { return }
        do { try await forgetComputer(id: activeComputerID) }
        catch { self.error = error.localizedDescription }
    }
    @discardableResult
    func refresh() async -> Bool {
        // A poll started during teardown would capture the previous API client
        // with the next generation. Only activation can poll until it completes.
        guard !changingComputer else { return false }
        return await refreshCurrentComputer()
    }
    private func refreshCurrentComputer() async -> Bool {
        if let refreshTask { return await refreshTask.value }
        guard let api else { return false }
        let generation = refreshGeneration
        loading = true
        let task = Task { @MainActor in
            defer { loading = false; refreshTask = nil }
            do {
                async let h = checkHealth(api)
                async let s = fetchSessions(api)
                async let p = fetchProjects(api)
                let result = try await (h, s, p)
                guard generation == refreshGeneration, self.api === api else { return false }
                health = result.0; sessions = result.1; projects = result.2; providerWarnings = api.providerWarnings; spaceProviders = api.spaceProviders
                refreshedAt = .now; error = nil
                return true
            } catch {
                guard generation == refreshGeneration, self.api === api else { return false }
                if !(error is CancellationError) { self.error = error.localizedDescription }
                return false
            }
        }
        refreshTask = task
        return await task.value
    }
    func requestSessionPrompt(_ action: SessionPrompt.Action, sessionID: String) {
        guard let latest = sessions.first(where: { $0.id == sessionID }) else { return }
        let prompt = SessionPrompt(action: action, sessionID: sessionID, computerID: activeComputerID,
                                   sessionName: latest.name, computerName: computer?.name ?? "your computer")
        guard canConfirm(prompt) else { return }
        renameDraft = latest.name
        sessionPrompt = prompt
    }
    func canConfirm(_ prompt: SessionPrompt) -> Bool {
        guard prompt.computerID == activeComputerID, !busy, !readOnly, !changingComputer,
              let latest = sessions.first(where: { $0.id == prompt.sessionID }), !latest.isArchived, supports(prompt.action.rawValue, session: latest) else { return false }
        return prompt.action != .stop || latest.attachable
    }
    func confirm(_ prompt: SessionPrompt, name: String) async {
        guard canConfirm(prompt), let latest = sessions.first(where: { $0.id == prompt.sessionID }) else { return }
        if prompt.action == .rename {
            let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else { return }
            await rename(latest, name: trimmed)
        } else { await action(prompt.action.rawValue, session: latest) }
    }

    @discardableResult
    func action(_ action: String, session: RemoteSession) async -> Bool {
        guard supports(action, session: session), let api, !busy, !readOnly, !changingComputer else { return false }
        let generation = refreshGeneration
        busy = true; defer { busy = false }
        do {
            try await performAction(api, action, session.id)
            guard generation == refreshGeneration else { return false }
            if action == "stop" || action == "archive" { await connections[session.id]?.disconnect() }
            if action == "archive", selectedID == session.id { selectedID = nil }
            // Do not mistake a poll started before the action for its resulting state.
            if let refreshTask { _ = await refreshTask.value }
            guard generation == refreshGeneration else { return false }
            let refreshed = await refresh()
            guard generation == refreshGeneration else { return false }
            guard refreshed else {
                actionError = "The \(action) request for “\(session.name)” was sent, but Trill couldn’t refresh its status. " + (error ?? "Check the connection and refresh Sessions.")
                return false
            }
            if action == "resume", !selectSession(id: session.id) {
                error = "The reopened session has not appeared in Sessions yet. Refresh Projects to check it."
                return false
            }
            return true
        } catch {
            if generation == refreshGeneration {
                self.error = error.localizedDescription
                actionError = "Couldn’t \(action) “\(session.name)”. " + error.localizedDescription
            }
            return false
        }
    }
    func canCreate(projectID: String, terminalOnly: Bool, useWorktree: Bool) -> Bool {
        guard let project = projects.first(where: { $0.id == projectID }), project.canCreate else { return false }
        return (terminalOnly ? (project.capabilities?.createTerminal ?? true) : (project.capabilities?.createAgent ?? true)) &&
            (terminalOnly || !useWorktree || (project.capabilities?.worktree ?? true))
    }
    func create(projectID: String, name: String, goal: String, terminalOnly: Bool, useWorktree: Bool) async throws {
        guard canCreate(projectID: projectID, terminalOnly: terminalOnly, useWorktree: useWorktree) else {
            throw SessionCreationPreflightError.unsupportedProject
        }
        guard let api, !busy, !readOnly, !changingComputer else { throw SessionCreationPreflightError.unavailable }
        busy = true; defer { busy = false }
        try await api.create(projectId: projectID, name: name, goal: goal, terminalOnly: terminalOnly, useWorktree: useWorktree)
        if let refreshTask { _ = await refreshTask.value }
        await refresh()
    }
    func rename(_ session: RemoteSession, name: String) async {
        guard supports("rename", session: session), let api, !busy, !readOnly, !changingComputer else { return }
        let generation = refreshGeneration
        busy = true; defer { busy = false }
        do {
            try await api.rename(id: session.id, name: name)
            guard generation == refreshGeneration else { return }
            let refreshed = await refresh()
            if generation == refreshGeneration, !refreshed {
                actionError = "The rename request for “\(session.name)” was sent, but Trill couldn’t refresh its status. " + (error ?? "Check the connection and refresh Sessions.")
            }
        } catch {
            guard generation == refreshGeneration else { return }
            self.error = error.localizedDescription
            actionError = "Couldn’t rename “\(session.name)”. " + error.localizedDescription
        }
    }
    func closeConnections() async { for c in connections.values { await c.disconnect() }; connections = [:] }
    func background() async {
        let existingConnections = Array(connections.values)
        let budgetToken = existingConnections.contains(where: \.returnsControlWhenHidden) ? beginBackgroundHandoff() : nil
        defer { if let budgetToken { endBackgroundHandoff(token: budgetToken) } }
        // Record every connection's pause even if another connection is still
        // waiting for an attach response or a controller-release request.
        await withTaskGroup(of: Void.self) { group in
            for connection in existingConnections {
                group.addTask { await connection.background() }
            }
        }
    }
    private func beginBackgroundHandoff() -> UUID {
        if let previousToken = backgroundHandoffToken { endBackgroundHandoff(token: previousToken) }
        let token = UUID()
        backgroundHandoffToken = token
        backgroundHandoffTaskID = UIApplication.shared.beginBackgroundTask(withName: "Return Herdr control") { [weak self] in
            Task { @MainActor [weak self] in self?.endBackgroundHandoff(token: token) }
        }
        return token
    }
    private func endBackgroundHandoff(token: UUID) {
        guard backgroundHandoffToken == token else { return }
        backgroundHandoffToken = nil
        let taskID = backgroundHandoffTaskID
        backgroundHandoffTaskID = .invalid
        if taskID != .invalid { UIApplication.shared.endBackgroundTask(taskID) }
    }
    func foreground() async { for c in connections.values { await c.foreground() } }
}

@main
struct TrillApp: App {
    @State private var store = AppStore()
    @Environment(\.scenePhase) private var scenePhase
    var body: some Scene {
        WindowGroup {
            RootView().environment(store).tint(Color("AccentColor"))
                .task {
                    while !Task.isCancelled {
                        await store.refresh()
                        do { try await Task.sleep(for: .seconds(10)) } catch { break }
                    }
                }
                .onOpenURL { url in
                    guard url.scheme == "trill", url.host == "pair" else { return }
                    store.pairingLink = url.absoluteString
                    store.pairingPresented = true
                }
                .onChange(of: scenePhase) { _, phase in
                    Task {
                        if phase == .background { await store.background() }
                        if phase == .active { await store.foreground(); await store.refresh() }
                    }
                }
        }
    }
}
