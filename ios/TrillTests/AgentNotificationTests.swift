import XCTest
@testable import Trill

@MainActor
final class AgentNotificationTests: XCTestCase {
    private let sessionID = "herdr-" + String(repeating: "a", count: 32)
    private func computer(_ name: String) -> Computer {
        Computer(name: name, address: "https://\(name).invalid", token: String(repeating: "b", count: 64), fingerprint: "")
    }
    private func route(computerID: UUID, sessionID: String? = nil) -> AgentNotificationRoute {
        AgentNotificationRoute(userInfo: ["computerId": computerID.uuidString, "sessionId": sessionID ?? self.sessionID, "eventId": UUID().uuidString])!
    }
    func testRouteRejectsURLsPathsAndMissingIdentity() {
        let valid: [AnyHashable: Any] = ["computerId": UUID().uuidString, "sessionId": sessionID, "eventId": UUID().uuidString]
        XCTAssertNotNil(AgentNotificationRoute(userInfo: valid))
        for field in ["computerId", "sessionId", "eventId"] {
            var invalid = valid; invalid[field] = "https://untrusted.invalid/private/file"
            XCTAssertNil(AgentNotificationRoute(userInfo: invalid))
            invalid.removeValue(forKey: field)
            XCTAssertNil(AgentNotificationRoute(userInfo: invalid))
        }
    }
    func testNotificationSelectsExactComputerAndSessionAndRejectsStaleRoutes() async throws {
        let first = SavedComputer(computer: computer("first"))
        let second = SavedComputer(computer: computer("second"))
        var available = true
        let store = AppStore(loadLibrary: { ComputerLibrary(computers: [first, second], activeID: second.id) }, saveLibrary: { _ in }, loadPairing: { _ in nil },
            checkHealth: { _ in Health(name: "fixture", version: "fixture", readOnly: true) },
            fetchSessions: { _ in available ? [RemoteSession(id: self.sessionID, name: "Fixture", projectId: "project", projectName: "Fixture", status: "running", activityState: "idle", agent: "claude", branch: "", updatedAt: "", attachable: true, resumable: false)] : [] }, fetchProjects: { _ in [] })
        store.connectionDetailsPresented = true
        await store.openNotification(route(computerID: first.id))
        XCTAssertEqual(store.activeComputerID, first.id)
        XCTAssertEqual(store.selectedID, sessionID)
        XCTAssertFalse(store.connectionDetailsPresented)
        XCTAssertNil(store.actionError)
        available = false
        await store.openNotification(route(computerID: second.id))
        XCTAssertNil(store.selectedID)
        XCTAssertTrue(store.actionError?.contains("no longer available") == true)
        await store.openNotification(route(computerID: UUID()))
        XCTAssertEqual(store.activeComputerID, second.id)
        XCTAssertTrue(store.actionError?.contains("no longer saved") == true)
    }
    func testPreferencesAreOptInPerComputerAndForgetRequiresCleanup() async throws {
        let first = SavedComputer(computer: computer("first"))
        let second = SavedComputer(computer: computer("second"))
        var persisted: ComputerLibrary?
        var failDeletion = true
        var deletionHosts: [String] = []
        let store = AppStore(loadLibrary: { ComputerLibrary(computers: [first, second], activeID: second.id) }, saveLibrary: { persisted = $0 }, loadPairing: { _ in nil },
            checkHealth: { _ in Health(name: "fixture", version: "fixture", readOnly: true) }, fetchSessions: { _ in [] }, fetchProjects: { _ in [] },
            deleteNotificationRegistration: { client in
                deletionHosts.append(client.computer.name)
                if failDeletion { throw URLError(.notConnectedToInternet) }
            })
        XCTAssertTrue(store.computers.allSatisfy { $0.notificationPreferences == nil })
        try store.setNotificationPreferences(.init(attention: true, completion: false), computerID: first.id)
        XCTAssertTrue(persisted?.computers.first?.notificationPreferences?.attention == true)
        XCTAssertNil(persisted?.computers.last?.notificationPreferences)
        do { try await store.forgetComputer(id: first.id); XCTFail("Must retain credential for cleanup retry") } catch {}
        XCTAssertEqual(store.computers.count, 2)
        failDeletion = false
        try await store.forgetComputer(id: first.id)
        XCTAssertEqual(store.computers.map(\.id), [second.id])
        XCTAssertEqual(deletionHosts, ["first", "first"])
    }
    func testOldLibraryDecodesWithNotificationsOff() throws {
        let library = ComputerLibrary(computers: [SavedComputer(computer: computer("fixture"))])
        let data = try JSONEncoder().encode(library)
        XCTAssertFalse(String(decoding: data, as: UTF8.self).contains("notificationPreferences"))
        let decoded = try JSONDecoder().decode(ComputerLibrary.self, from: data)
        XCTAssertNil(decoded.computers.first?.notificationPreferences)
    }
}
