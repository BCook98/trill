import XCTest
@testable import Trill

@MainActor
final class ComputerSwitchingTests: XCTestCase {
    @MainActor private final class Harness {
        var saved: ComputerLibrary?
        var failSave = false
        var offline = Set<String>()
        var saves = 0
        var deleted: [String] = []
        var legacy: Computer?
        var holdHost: String?
        var sessionClients: [APIClient] = []
        var pending: CheckedContinuation<[RemoteSession], Error>?
        var fetchStarted: (() -> Void)?
        var actionStarted: (() -> Void)?
        var pendingAction: CheckedContinuation<Void, Error>?
        var holdAction = false
        func store(load: Bool = false) -> AppStore {
            AppStore(loadSavedPairing: load, savePairing: { _, _ in }, deletePairing: { self.deleted.append($0) },
                loadLibrary: { self.saved }, saveLibrary: {
                    if self.failSave { throw TrillError.message("Keychain locked") }
                    try $0.validate(); self.saved = $0; self.saves += 1
                }, loadPairing: { $0 == "primary" ? self.legacy : nil },
                checkHealth: { api in
                    if self.offline.contains(api.baseURL.host!) { throw URLError(.notConnectedToInternet) }
                    return Health(name: api.computer.name, version: "fixture", readOnly: false)
                }, fetchSessions: { api in
                    self.sessionClients.append(api)
                    if self.holdHost == api.baseURL.host {
                        self.holdHost = nil
                        return try await withCheckedThrowingContinuation { self.pending = $0; self.fetchStarted?() }
                    }
                    return [Self.row(api.computer.name)]
                }, fetchProjects: { _ in [Project(id: "project", name: "Project", path: "/fixture")] },
                performRename: { _, _, _ in
                    if self.holdAction { try await withCheckedThrowingContinuation { self.pendingAction = $0; self.actionStarted?() } }
                })
        }
        static func row(_ name: String) -> RemoteSession {
            var session = RemoteSession(id: "same-session-id", name: name, projectId: "project", projectName: "Project", status: "running", activityState: "working", agent: "shell", branch: "", updatedAt: "", attachable: true, resumable: false)
            var capabilities = SessionCapabilities(); capabilities.rename = true
            session.capabilities = capabilities
            return session
        }
    }
    private func computer(_ name: String) -> Computer {
        Computer(name: name, address: "https://\(name.lowercased()).invalid", token: String(repeating: "a", count: 64), fingerprint: "")
    }

    func testPairingAddsComputersAndDraftsAreScopedAcrossMatchingSessionIDs() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        let old = store.connection(for: "same-session-id"); old.draftText = "A draft"
        let firstPhoto = IdeaPhoto(jpegData: Data([1, 2, 3]))
        old.draftPhotos = [firstPhoto]
        XCTAssertTrue(store.selectSession(id: "same-session-id"))
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        XCTAssertEqual(store.computers.count, 2)
        let current = store.connection(for: "same-session-id")
        XCTAssertFalse(current === old)
        XCTAssertEqual(current.draftText, "")
        XCTAssertTrue(current.draftPhotos.isEmpty)
        current.draftText = "B draft"
        let secondPhoto = IdeaPhoto(jpegData: Data([4, 5, 6]))
        current.draftPhotos = [secondPhoto]
        try await store.selectComputer(id: a)
        XCTAssertEqual(store.connection(for: "same-session-id").draftText, "A draft")
        XCTAssertEqual(store.connection(for: "same-session-id").draftPhotos, [firstPhoto])
        XCTAssertEqual(store.selectedID, "same-session-id")
        XCTAssertEqual(store.sessions.first?.name, "A")
        try await store.selectComputer(id: b)
        XCTAssertEqual(store.connection(for: "same-session-id").draftText, "B draft")
        XCTAssertEqual(store.connection(for: "same-session-id").draftPhotos, [secondPhoto])
        XCTAssertEqual(h.saved?.activeID, b)
    }

    func testUnopenedDraftsSurviveLaterVisitsAndRepairedPairingRetainsIdentity() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        store.connection(for: "one").draftText = "first"
        store.connection(for: "two").draftText = "second"
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        try await store.selectComputer(id: a)
        store.connection(for: "one").draftText = "edited"
        try await store.selectComputer(id: b)
        var repair = computer("A"); repair.token = String(repeating: "b", count: 64)
        try await store.pair(repair)
        XCTAssertEqual(store.activeComputerID, a)
        XCTAssertEqual(store.computers.count, 2)
        XCTAssertEqual(store.connection(for: "one").draftText, "edited")
        XCTAssertEqual(store.connection(for: "two").draftText, "second")
    }

    func testPersistenceFailureLeavesCurrentPairingAndDraftUntouched() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = store.activeComputerID
        let connection = store.connection(for: "same-session-id"); connection.draftText = "keep"
        h.failSave = true
        do { try await store.pair(computer("B")); XCTFail("Expected save failure") } catch {}
        XCTAssertEqual(store.activeComputerID, a)
        XCTAssertEqual(store.computers.count, 1)
        XCTAssertTrue(store.connection(for: "same-session-id") === connection)
        XCTAssertEqual(connection.draftText, "keep")
        XCTAssertFalse(store.busy)
        XCTAssertFalse(store.changingComputer)
    }

    func testOfflineComputerCanBeSelectedWithoutDiscardingOtherPairings() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        h.offline.insert("a.invalid")
        try await store.selectComputer(id: a)
        XCTAssertEqual(store.activeComputerID, a)
        XCTAssertNotNil(store.error)
        XCTAssertTrue(store.sessions.isEmpty)
        try await store.selectComputer(id: b)
        XCTAssertEqual(store.computers.count, 2)
        XCTAssertNil(store.error)
        XCTAssertEqual(store.sessions.first?.name, "B")
    }

    func testForgetInactiveDoesNotDisconnectActiveAndLastForgetPersistsEmptyLibrary() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        let connection = store.connection(for: "same-session-id"); connection.draftText = "B stays"
        try await store.forgetComputer(id: a)
        XCTAssertEqual(store.activeComputerID, b)
        XCTAssertTrue(store.connection(for: "same-session-id") === connection)
        XCTAssertEqual(connection.draftText, "B stays")
        try await store.forgetComputer(id: b)
        XCTAssertNil(store.computer)
        XCTAssertNil(store.api)
        XCTAssertEqual(h.saved?.computers.count, 0)
        h.legacy = computer("Legacy")
        XCTAssertNil(h.store(load: true).computer, "An empty library must not resurrect the old primary pairing")
    }

    func testLegacyMigrationWritesLibraryBeforeRemovingPrimary() throws {
        let h = Harness(); h.legacy = computer("Legacy")
        let store = h.store(load: true)
        XCTAssertEqual(store.computer, h.legacy)
        XCTAssertEqual(store.computers.count, 1)
        XCTAssertEqual(h.saves, 1)
        XCTAssertTrue(h.deleted.contains("primary"))
        let failing = Harness(); failing.legacy = computer("Keep"); failing.failSave = true
        let blocked = failing.store(load: true)
        XCTAssertNil(blocked.computer)
        XCTAssertNotNil(blocked.error)
        XCTAssertFalse(failing.deleted.contains("primary"))
    }

    func testLateRefreshFromPreviousComputerCannotReplaceNewComputerMetadata() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        try await store.selectComputer(id: a)
        let started = expectation(description: "Old poll started")
        h.holdHost = "a.invalid"; h.fetchStarted = { started.fulfill() }
        let oldPoll = Task { await store.refresh() }
        await fulfillment(of: [started], timeout: 2)
        let switching = Task { try await store.selectComputer(id: b) }
        for _ in 0..<100 where !store.changingComputer { await Task.yield() }
        XCTAssertTrue(store.changingComputer)
        h.pending?.resume(returning: [Harness.row("Stale A")]); h.pending = nil
        _ = await oldPoll.value
        try await switching.value
        XCTAssertEqual(store.activeComputerID, b)
        XCTAssertEqual(store.sessions.first?.name, "B")
        XCTAssertEqual(store.health?.name, "B")
    }

    func testFreshPollDuringActivationCannotUsePreviousComputerClient() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        try await store.selectComputer(id: a)
        let previousClient = try XCTUnwrap(store.api)
        let connection = store.connection(for: "same-session-id")
        let started = expectation(description: "Previous computer attach is pending")
        var pendingAttach: CheckedContinuation<TerminalLease, Error>?
        // Disconnect must drain this fake attach. It throws before any transport
        // can be created, so the test never opens a terminal or uses networking.
        let environment = TerminalConnection.Environment(attach: { _, _, _ in
            try await withCheckedThrowingContinuation {
                pendingAttach = $0
                started.fulfill()
            }
        }, release: { _ in }, makeTransport: { _ in fatalError("Cancelled fixture must never create a transport") })
        let opening = Task { await connection.connect(sessionID: "same-session-id", environment: environment) }
        defer { pendingAttach?.resume(throwing: CancellationError()) }
        await fulfillment(of: [started], timeout: 2)
        let switching = Task { try await store.selectComputer(id: b) }
        for _ in 0..<100 where connection.phase != .disconnected { await Task.yield() }
        XCTAssertEqual(connection.phase, .disconnected)
        XCTAssertTrue(store.changingComputer)
        XCTAssertEqual(store.activeComputerID, a)
        XCTAssertTrue(store.api === previousClient)
        let requestCount = h.sessionClients.count

        let refreshed = await store.refresh()
        XCTAssertFalse(refreshed, "Automatic polls must not enter during activation")
        XCTAssertEqual(h.sessionClients.count, requestCount, "No fresh request may capture the previous computer")

        pendingAttach?.resume(throwing: CancellationError()); pendingAttach = nil
        await opening.value
        try await switching.value
        XCTAssertEqual(store.activeComputerID, b)
        XCTAssertFalse(store.api === previousClient)
        XCTAssertTrue(h.sessionClients.last === store.api)
        XCTAssertEqual(h.sessionClients.count, requestCount + 1, "Activation must still poll the new computer")
        XCTAssertEqual(store.sessions.first?.name, "B")
        XCTAssertEqual(store.health?.name, "B")
        XCTAssertNil(store.error)
        XCTAssertFalse(store.changingComputer)
    }

    func testSwitchWaitsForExplicitSessionOperationToFinish() async throws {
        let h = Harness(); let store = h.store()
        try await store.pair(computer("A")); let a = try XCTUnwrap(store.activeComputerID)
        try await store.pair(computer("B")); let b = try XCTUnwrap(store.activeComputerID)
        let started = expectation(description: "Action started")
        h.holdAction = true; h.actionStarted = { started.fulfill() }
        let action = Task { await store.rename(Harness.row("B"), name: "Updated") }
        await fulfillment(of: [started], timeout: 2)
        do { try await store.selectComputer(id: a); XCTFail("Switch must wait") } catch {}
        XCTAssertEqual(store.activeComputerID, b)
        h.pendingAction?.resume(); h.pendingAction = nil
        _ = await action.value
        XCTAssertFalse(store.busy)
    }
}
