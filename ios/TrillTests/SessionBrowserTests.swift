import XCTest
import SwiftUI
import UIKit
@testable import Trill

final class SessionBrowserTests: XCTestCase {
    @MainActor func testHerdrCapabilitiesDisableManagementWithoutDisablingTerminalAccess() async throws {
        let store = AppStore(loadSavedPairing: false)
        store.health = Health(name: "Herdr lab", version: "1", readOnly: false, sessionManagement: false)
        let row = session("herdr-session")
        store.sessions = [row]
        XCTAssertFalse(store.readOnly)
        XCTAssertFalse(store.sessionManagementAvailable)
        XCTAssertTrue(store.selectSession(id: row.id))
        for action in [SessionPrompt.Action.rename, .stop, .archive] {
            store.requestSessionPrompt(action, sessionID: row.id)
            XCTAssertNil(store.sessionPrompt)
        }
        let stopped = await store.action("stop", session: row)
        XCTAssertFalse(stopped)
        do {
            try await store.create(projectID: "p1", name: "", goal: "", terminalOnly: true, useWorktree: false)
            XCTFail("Unsupported creation must be rejected")
        } catch { XCTAssertTrue(error.localizedDescription.contains("does not support")) }
    }

    func testHealthCapabilityDecodesLegacyAndHerdrResponses() throws {
        let legacy = try JSONDecoder().decode(Health.self, from: Data(#"{"name":"trilld","version":"1","readOnly":false}"#.utf8))
        XCTAssertNil(legacy.sessionManagement)
        let herdr = try JSONDecoder().decode(Health.self, from: Data(#"{"name":"trilld","version":"1","readOnly":false,"sessionManagement":false,"backend":"herdr"}"#.utf8))
        XCTAssertEqual(herdr.sessionManagement, false)
    }

    @MainActor func testMixedProviderCapabilitiesOverrideGlobalManagementFlag() {
        let store = AppStore(loadSavedPairing: false)
        store.health = Health(name: "Mixed", version: "1", readOnly: false, sessionManagement: true)
        var herdr = session("herdr")
        var caps = SessionCapabilities(); caps.rename = true; caps.createTerminal = true
        herdr.capabilities = caps
        store.sessions = [herdr, session("xirp")]
        XCTAssertTrue(store.supports("rename", session: herdr))
        XCTAssertFalse(store.supports("stop", session: herdr))
        XCTAssertFalse(store.supports("archive", session: herdr))
        XCTAssertFalse(store.supports("resume", session: herdr))
        store.requestSessionPrompt(.stop, sessionID: herdr.id)
        XCTAssertNil(store.sessionPrompt)
        store.requestSessionPrompt(.rename, sessionID: herdr.id)
        XCTAssertEqual(store.sessionPrompt?.sessionID, herdr.id)
        store.sessionPrompt = nil
        store.requestSessionPrompt(.stop, sessionID: "xirp")
        XCTAssertEqual(store.sessionPrompt?.sessionID, "xirp")
    }

    @MainActor func testCreationCatalogSupportsHerdrTerminalsWithoutInventingAgentOrWorktreeSupport() {
        let store = AppStore(loadSavedPairing: false)
        var herdr = Project(id: "h", name: "Herdr", path: "")
        var caps = SessionCapabilities(); caps.createTerminal = true
        herdr.capabilities = caps
        var unsupported = Project(id: "none", name: "Unavailable", path: "")
        unsupported.capabilities = SessionCapabilities()
        store.projects = [Project(id: "x", name: "Xirp", path: "/repo"), herdr, unsupported]
        XCTAssertEqual(store.creatableProjects.map(\.id), ["x", "h"])
        XCTAssertFalse(herdr.capabilities!.createAgent)
        XCTAssertFalse(herdr.capabilities!.worktree)
    }

    func testCreationFailureDistinguishesPreflightFromUncertainTransportAndDecoding() {
        for error in [SessionCreationPreflightError.unsupportedProject, .unavailable] {
            let failure = SessionCreationFailure(error)
            XCTAssertFalse(failure.outcomeUncertain)
            XCTAssertEqual(failure.message, error.localizedDescription)
        }
        let uncertainErrors: [Error] = [
            TrillError.message("The connection was lost."),
            URLError(.timedOut),
            DecodingError.dataCorrupted(.init(codingPath: [], debugDescription: "Invalid creation response")),
            APIRequestError(status: 409, message: "operation outcome unconfirmed; refresh sessions before retrying")
        ]
        for error in uncertainErrors {
            let failure = SessionCreationFailure(error)
            XCTAssertTrue(failure.outcomeUncertain)
            XCTAssertTrue(failure.message.contains("Check Sessions before trying again"))
        }
        for status in [400, 403, 404, 409] {
            let error = APIRequestError(status: status, message: "operation unavailable")
            let failure = SessionCreationFailure(error)
            XCTAssertFalse(failure.outcomeUncertain)
            XCTAssertEqual(failure.message, error.message)
        }
    }

    @MainActor func testCreationRevalidatesStaleSelectedProjectAndAllowsAnotherProject() async {
        let store = AppStore(loadSavedPairing: false)
        var herdr = Project(id: "h", name: "Herdr", path: "")
        var caps = SessionCapabilities(); caps.createTerminal = true
        herdr.capabilities = caps
        let xirp = Project(id: "x", name: "Xirp", path: "/repo")
        store.projects = [herdr, xirp]
        XCTAssertTrue(store.canCreate(projectID: "h", terminalOnly: true, useWorktree: false))

        // A successful partial refresh removes the unavailable provider's catalog.
        store.projects = [xirp]
        XCTAssertFalse(store.canCreate(projectID: "h", terminalOnly: true, useWorktree: false))
        do {
            try await store.create(projectID: "h", name: "", goal: "", terminalOnly: true, useWorktree: false)
            XCTFail("A stale selection must be rejected before dispatch")
        } catch {
            XCTAssertEqual(error as? SessionCreationPreflightError, .unsupportedProject)
            XCTAssertFalse(SessionCreationFailure(error).outcomeUncertain)
        }
        XCTAssertFalse(store.busy)
        XCTAssertTrue(store.canCreate(projectID: "x", terminalOnly: true, useWorktree: false))
        XCTAssertFalse(store.canCreate(projectID: "", terminalOnly: true, useWorktree: false))
    }

    @MainActor func testCreationRevalidatesCapabilitiesForSameProjectID() async {
        let store = AppStore(loadSavedPairing: false)
        var project = Project(id: "p", name: "Project", path: "/repo")
        store.projects = [project]
        XCTAssertTrue(store.canCreate(projectID: "p", terminalOnly: false, useWorktree: true))
        XCTAssertTrue(store.canCreate(projectID: "p", terminalOnly: true, useWorktree: false))

        var caps = SessionCapabilities(); caps.createAgent = true
        project.capabilities = caps
        store.projects = [project]
        XCTAssertFalse(store.canCreate(projectID: "p", terminalOnly: true, useWorktree: false))
        XCTAssertFalse(store.canCreate(projectID: "p", terminalOnly: false, useWorktree: true))
        XCTAssertTrue(store.canCreate(projectID: "p", terminalOnly: false, useWorktree: false))
        do {
            try await store.create(projectID: "p", name: "", goal: "Goal", terminalOnly: false, useWorktree: true)
            XCTFail("Unsupported worktrees must be rejected before dispatch")
        } catch { XCTAssertEqual(error as? SessionCreationPreflightError, .unsupportedProject) }

        // A supported request with no API is also a known local failure.
        do {
            try await store.create(projectID: "p", name: "", goal: "Goal", terminalOnly: false, useWorktree: false)
            XCTFail("A missing API must be rejected before dispatch")
        } catch {
            XCTAssertEqual(error as? SessionCreationPreflightError, .unavailable)
            XCTAssertFalse(SessionCreationFailure(error).outcomeUncertain)
        }
        project.capabilities = SessionCapabilities()
        store.projects = [project]
        XCTAssertFalse(store.canCreate(projectID: "p", terminalOnly: false, useWorktree: false))
    }

    private func session(_ id: String, projectID: String = "p1", projectName: String = "Shared name",
                         status: String = "running", attachable: Bool = true, resumable: Bool = false) -> RemoteSession {
        RemoteSession(id: id, name: "Session \(id)", projectId: projectID, projectName: projectName,
                      status: status, activityState: "idle", agent: "codex", branch: "feature/browser",
                      updatedAt: "2026-10-24T00:00:00Z", attachable: attachable, resumable: resumable)
    }

    func testArchiveClassificationMatchesSidecarEndedStatuses() {
        for status in ["completed", "stopped", "failed"] {
            XCTAssertTrue(session(status, status: status, attachable: false).isArchived, status)
        }
        for status in ["running", "idle", "waiting", "finished", "unknown"] {
            XCTAssertFalse(session(status, status: status).isArchived, status)
        }
        XCTAssertFalse(session("finished-without-terminal", status: "finished", attachable: false).isArchived)
        XCTAssertFalse(session("idle-without-terminal", status: "idle", attachable: false).isArchived)
    }

    @MainActor func testCurrentAndArchiveFiltersAreDisjointAndDoNotMutateSnapshot() {
        let store = AppStore(loadSavedPairing: false)
        let snapshot = [session("running"), session("completed", status: "completed", attachable: false, resumable: true),
                        session("finished", status: "finished"), session("failed", status: "failed", attachable: false),
                        session("stopped", status: "stopped", attachable: false), session("idle", status: "idle", attachable: false)]
        store.sessions = snapshot
        XCTAssertEqual(store.currentSessions.map(\.id), ["running", "finished", "idle"])
        XCTAssertEqual(store.archiveSessions.map(\.id), ["completed", "failed", "stopped"])
        XCTAssertTrue(store.archiveSessions.first(where: { $0.id == "failed" })!.needsAttention)
        XCTAssertEqual(store.sessions, snapshot)
    }

    @MainActor func testProjectsUseExactIDsWhenNamesAreIdentical() {
        let store = AppStore(loadSavedPairing: false)
        store.projects = [Project(id: "p1", name: "Shared name", path: "/one"),
                          Project(id: "p2", name: "Shared name", path: "/two")]
        store.sessions = [session("one"), session("two", projectID: "p2"),
                          session("archive-one", status: "completed", attachable: false),
                          session("archive-two", projectID: "p2", status: "stopped", attachable: false)]
        XCTAssertEqual(store.browserProjects.map(\.id), ["p1", "p2"])
        XCTAssertEqual(store.sessions(in: "p1", archived: false).map(\.id), ["one"])
        XCTAssertEqual(store.sessions(in: "p2", archived: false).map(\.id), ["two"])
        XCTAssertEqual(store.sessions(in: "p1", archived: true).map(\.id), ["archive-one"])
        XCTAssertEqual(store.sessions(in: "p2", archived: true).map(\.id), ["archive-two"])
        XCTAssertTrue(store.sessions(in: "p", archived: false).isEmpty)
    }

    @MainActor func testBrowserIncludesEmptyProjectsAndOrphansWithoutReplacingCatalogMetadata() {
        let store = AppStore(loadSavedPairing: false)
        store.projects = [Project(id: "empty", name: "Empty", path: "/empty"),
                          Project(id: "p1", name: "Catalog name", path: "/catalog")]
        store.sessions = [session("known", projectName: "Old name"),
                          session("orphan", projectID: "missing", projectName: "Removed project", status: "completed", attachable: false),
                          session("second-orphan", projectID: "missing", projectName: "Removed project"),
                          session("unnamed", projectID: "unknown-id", projectName: ""),
                          session("unassigned", projectID: "", projectName: "")]
        XCTAssertEqual(Set(store.browserProjects.map(\.id)), Set(["empty", "p1", "missing", "unknown-id", ""]))
        XCTAssertEqual(store.browserProjects.count, 5)
        XCTAssertEqual(store.browserProjects.first(where: { $0.id == "p1" })?.path, "/catalog")
        XCTAssertEqual(store.browserProjects.first(where: { $0.id == "p1" })?.displayName, "Catalog name")
        XCTAssertEqual(store.browserProjects.first(where: { $0.id == "missing" })?.displayName, "Removed project")
        XCTAssertEqual(store.browserProjects.first(where: { $0.id == "unknown-id" })?.displayName, "unknown-id")
        XCTAssertEqual(store.browserProjects.first(where: { $0.id.isEmpty })?.displayName, "Unassigned project")
        XCTAssertTrue(store.sessions(in: "empty", archived: false).isEmpty)
        XCTAssertTrue(store.sessions(in: "empty", archived: true).isEmpty)
    }

    func testSearchIncludesArchivedNamesProjectsAgentsBranchesAndStatus() {
        let archived = session("history", status: "completed", attachable: false)
        for query in ["HISTORY", "shared NAME", "CODEX", "feature/BROWSER", "completed", "  history\n", "  "] {
            XCTAssertTrue(archived.matchesSearch(query), query)
        }
        XCTAssertFalse(archived.matchesSearch("no match"))
    }

    @MainActor func testSelectingCurrentSessionSwitchesTabAndRejectsArchivedOrMissingIDs() {
        let store = AppStore(loadSavedPairing: false)
        store.sessions = [session("current"), session("archive", status: "completed", attachable: false)]
        store.selectedTab = .projects
        XCTAssertFalse(store.selectSession(id: "archive"))
        XCTAssertFalse(store.selectSession(id: "missing"))
        XCTAssertNil(store.selectedID)
        XCTAssertEqual(store.selectedTab, .projects)
        XCTAssertTrue(store.selectSession(id: "current"))
        XCTAssertEqual(store.selectedID, "current")
        XCTAssertEqual(store.selectedTab, .sessions)
    }

    // These fixtures bypass saved pairing, Keychain, and every network operation.
    // The reserved .invalid host is never contacted.
    @MainActor private final class Fixture {
        var rows: [RemoteSession] = []
        var actionRequests: [String] = []
        var readOnly = false
        var failAction = false
        var failRefresh = false
        var fetchCount = 0
        var holdNextFetch = false
        var pendingFetch: CheckedContinuation<[RemoteSession], Error>?
        var onFetchStarted: (() -> Void)?
        var onAction: (() -> Void)?

        func makeStore() async throws -> AppStore {
            let store = AppStore(loadSavedPairing: false, savePairing: { _, _ in }, deletePairing: { _ in }, saveLibrary: { _ in },
                                 checkHealth: { _ in Health(name: "Fixture", version: "1", readOnly: self.readOnly) },
                                 fetchSessions: { _ in
                self.fetchCount += 1
                if self.failRefresh { throw TrillError.message("Fixture refresh failed") }
                if self.holdNextFetch {
                    self.holdNextFetch = false
                    return try await withCheckedThrowingContinuation {
                        self.pendingFetch = $0
                        self.onFetchStarted?()
                    }
                }
                return self.rows
            }, fetchProjects: { _ in [Project(id: "p1", name: "Shared name", path: "/fixture")] },
                                 performAction: { _, action, id in
                self.actionRequests.append("\(action):\(id)")
                if self.failAction { throw TrillError.message("Fixture resume failed") }
                self.onAction?()
            })
            try await store.pair(Computer(name: "Fixture", address: "https://trill-browser.invalid", token: String(repeating: "t", count: 64), fingerprint: ""))
            store.selectedTab = .projects
            return store
        }
    }

    @MainActor func testWorkspaceSurvivesPresentationAndSessionSelectionChanges() async throws {
        let fixture = Fixture()
        fixture.rows = [session("one"), session("two")]
        let store = try await fixture.makeStore()
        let first = try XCTUnwrap(store.workspace(for: "one"))
        let webView = first.browser.webView
        first.projectPath = "src/components"
        first.photosPath = "captures"
        first.selectedFile = WorkspaceEntry(name: "page.html", path: "src/components/page.html", kind: "file", size: 20)
        first.browser.address = "http://localhost:3000/notes?view=grid#recent"
        first.browser.draftAddress = "http://localhost:3000/unfinished"
        _ = store.selectSession(id: "two")
        let second = try XCTUnwrap(store.workspace(for: "two"))
        XCTAssertFalse(first === second)
        XCTAssertEqual(second.path, "")
        _ = await store.refresh()
        _ = store.selectSession(id: "one")
        let reopened = try XCTUnwrap(store.workspace(for: "one"))
        XCTAssertTrue(first === reopened)
        XCTAssertTrue(reopened.browser.webView === webView)
        XCTAssertEqual(reopened.browser.address, "http://localhost:3000/notes?view=grid#recent")
        XCTAssertEqual(reopened.browser.draftAddress, "http://localhost:3000/unfinished")
        XCTAssertEqual(reopened.selectedFile?.path, "src/components/page.html")
        XCTAssertEqual(reopened.path, "src/components")
        reopened.browsingPhotos = true
        XCTAssertEqual(reopened.path, "captures")
        reopened.browsingPhotos = false
        XCTAssertEqual(reopened.path, "src/components")
        XCTAssertTrue(fixture.actionRequests.isEmpty)
    }

    @MainActor func testWorkspaceCannotCarryPagesAcrossComputerChanges() async throws {
        let fixture = Fixture()
        fixture.rows = [session("shared-id")]
        let store = try await fixture.makeStore()
        let first = try XCTUnwrap(store.workspace(for: "shared-id"))
        let originalWebView = first.browser.webView
        first.projectPath = "private-folder"
        first.browser.address = "http://localhost:3000/private"
        try await store.pair(Computer(name: "Second", address: "https://second.invalid", token: String(repeating: "s", count: 64), fingerprint: ""))
        let second = try XCTUnwrap(store.workspace(for: "shared-id"))
        XCTAssertFalse(first === second)
        XCTAssertFalse(first.browser.webView === originalWebView) // old page was closed
        XCTAssertEqual(second.browser.address, "")
        XCTAssertEqual(second.path, "")
        XCTAssertEqual(second.browser.api.baseURL.host, "second.invalid")
        XCTAssertTrue(fixture.actionRequests.isEmpty)
    }

    @MainActor func testBrowsingAndRefreshingDoNotResumeSessions() async throws {
        let fixture = Fixture()
        fixture.rows = [session("archive", status: "completed", attachable: false, resumable: true)]
        let store = try await fixture.makeStore()
        _ = store.browserProjects
        _ = store.sessions(in: "p1", archived: true)
        let refreshed = await store.refresh()
        XCTAssertTrue(refreshed)
        XCTAssertTrue(fixture.actionRequests.isEmpty)
        XCTAssertNil(store.selectedID)
        XCTAssertEqual(store.selectedTab, .projects)
    }

    @MainActor func testSuccessfulReopenLoadsLatestModelBeforeSelectingMainTab() async throws {
        let fixture = Fixture()
        let archived = session("archive", status: "completed", attachable: false, resumable: true)
        fixture.rows = [archived]
        let store = try await fixture.makeStore()
        var reopened = session("archive")
        reopened.name = "Latest name"
        fixture.rows = [reopened]
        let result = await store.action("resume", session: archived)
        XCTAssertTrue(result)
        XCTAssertEqual(fixture.actionRequests, ["resume:archive"])
        XCTAssertEqual(store.currentSessions, [reopened])
        XCTAssertTrue(store.archiveSessions.isEmpty)
        XCTAssertEqual(store.selectedID, "archive")
        XCTAssertEqual(store.selectedTab, .sessions)
        XCTAssertFalse(store.busy)
    }

    @MainActor func testFailedReopenPreservesNavigationAndArchive() async throws {
        let fixture = Fixture()
        let archived = session("archive", status: "completed", attachable: false, resumable: true)
        fixture.rows = [session("current"), archived]
        fixture.failAction = true
        let store = try await fixture.makeStore()
        store.selectedID = "current"
        let result = await store.action("resume", session: archived)
        XCTAssertFalse(result)
        XCTAssertEqual(store.selectedID, "current")
        XCTAssertEqual(store.selectedTab, .projects)
        XCTAssertEqual(store.archiveSessions, [archived])
        XCTAssertEqual(store.error, "Fixture resume failed")
        XCTAssertFalse(store.busy)
    }

    @MainActor func testSessionPromptFreezesIdentityAndRevalidatesBeforeMutation() async throws {
        let fixture = Fixture()
        let current = session("current")
        fixture.rows = [current]
        let store = try await fixture.makeStore()
        store.requestSessionPrompt(.archive, sessionID: current.id)
        let prompt = try XCTUnwrap(store.sessionPrompt)
        XCTAssertTrue(store.canConfirm(prompt))
        fixture.rows[0].name = "Changed remotely"
        _ = await store.refresh()
        XCTAssertEqual(store.sessionPrompt?.sessionName, current.name)
        fixture.readOnly = true
        _ = await store.refresh()
        XCTAssertFalse(store.canConfirm(prompt))
        await store.confirm(prompt, name: "")
        XCTAssertTrue(fixture.actionRequests.isEmpty)
        fixture.readOnly = false
        fixture.rows = []
        _ = await store.refresh()
        XCTAssertFalse(store.canConfirm(prompt))
        await store.confirm(prompt, name: "")
        XCTAssertTrue(fixture.actionRequests.isEmpty)
    }

    @MainActor func testChangingComputerClearsPromptAndCannotRetargetOldConfirmation() async throws {
        let fixture = Fixture()
        let current = session("same-id")
        fixture.rows = [current]
        let store = try await fixture.makeStore()
        store.requestSessionPrompt(.stop, sessionID: current.id)
        let prompt = try XCTUnwrap(store.sessionPrompt)
        store.actionError = "Old computer error"
        try await store.pair(Computer(name: "Other fixture", address: "https://other.invalid", token: String(repeating: "u", count: 64), fingerprint: ""))
        XCTAssertNil(store.sessionPrompt)
        XCTAssertNil(store.actionError)
        XCTAssertFalse(store.canConfirm(prompt))
        await store.confirm(prompt, name: "")
        XCTAssertTrue(fixture.actionRequests.isEmpty)
    }

    @MainActor func testActionFailureSurvivesSuccessfulBackgroundRefresh() async throws {
        let fixture = Fixture()
        let current = session("current")
        fixture.rows = [current]
        fixture.failAction = true
        let store = try await fixture.makeStore()
        store.selectedID = current.id
        let succeeded = await store.action("archive", session: current)
        XCTAssertFalse(succeeded)
        let message = try XCTUnwrap(store.actionError)
        XCTAssertTrue(message.contains(current.name))
        XCTAssertTrue(message.contains("archive"))
        let refreshed = await store.refresh()
        XCTAssertTrue(refreshed)
        XCTAssertNil(store.error)
        XCTAssertEqual(store.actionError, message)
        XCTAssertEqual(store.selectedID, current.id)
    }

    @MainActor func testReopenWithFailedRefreshDoesNotNavigateUsingStaleData() async throws {
        let fixture = Fixture()
        let archived = session("archive", status: "stopped", attachable: false, resumable: true)
        fixture.rows = [archived]
        let store = try await fixture.makeStore()
        fixture.failRefresh = true
        let result = await store.action("resume", session: archived)
        XCTAssertFalse(result)
        XCTAssertNil(store.selectedID)
        XCTAssertEqual(store.selectedTab, .projects)
        XCTAssertEqual(store.archiveSessions, [archived])
        XCTAssertEqual(store.error, "Fixture refresh failed")
        XCTAssertFalse(store.loading)
    }

    @MainActor func testReopenRequiresConfirmedCurrentSession() async throws {
        let fixture = Fixture()
        let archived = session("archive", status: "failed", attachable: false, resumable: true)
        fixture.rows = [archived]
        let store = try await fixture.makeStore()
        let result = await store.action("resume", session: archived)
        XCTAssertFalse(result)
        XCTAssertNil(store.selectedID)
        XCTAssertEqual(store.selectedTab, .projects)
        XCTAssertNotNil(store.error)
        XCTAssertEqual(store.archiveSessions, [archived])
    }

    @MainActor func testReadOnlyReopenDoesNotSendActionOrNavigate() async throws {
        let fixture = Fixture()
        fixture.readOnly = true
        let archived = session("archive", status: "completed", attachable: false, resumable: true)
        fixture.rows = [archived]
        let store = try await fixture.makeStore()
        let result = await store.action("resume", session: archived)
        XCTAssertFalse(result)
        XCTAssertTrue(fixture.actionRequests.isEmpty)
        XCTAssertEqual(store.selectedTab, .projects)
        XCTAssertNil(store.selectedID)
    }

    @MainActor func testReopenWaitsForOldPollThenFetchesResultingState() async throws {
        let fixture = Fixture()
        let archived = session("archive", status: "completed", attachable: false, resumable: true)
        fixture.rows = [archived]
        let store = try await fixture.makeStore()
        fixture.holdNextFetch = true
        let started = expectation(description: "Pre-action poll started")
        fixture.onFetchStarted = { started.fulfill() }
        let poll = Task { await store.refresh() }
        await fulfillment(of: [started], timeout: 2)
        guard fixture.pendingFetch != nil else {
            fixture.holdNextFetch = false
            poll.cancel()
            XCTFail("Pre-action poll did not reach the fixture gate")
            return
        }
        fixture.onAction = {
            fixture.rows = [self.session("archive")]
            fixture.pendingFetch?.resume(returning: [archived])
            fixture.pendingFetch = nil
        }
        let result = await store.action("resume", session: archived)
        _ = await poll.value
        XCTAssertTrue(result)
        XCTAssertEqual(fixture.fetchCount, 3, "Pairing, pre-action poll, and post-action refresh")
        XCTAssertEqual(store.currentSessions.map(\.id), ["archive"])
        XCTAssertTrue(store.archiveSessions.isEmpty)
        XCTAssertEqual(store.selectedTab, .sessions)
        XCTAssertEqual(store.selectedID, "archive")
    }

    @MainActor func testExplicitlyArchivedRunningSessionIsOnlyInProjectArchive() throws {
        var hidden = session("hidden", resumable: true)
        hidden.archived = true
        let decoded = try JSONDecoder().decode(RemoteSession.self, from: JSONEncoder().encode(hidden))
        XCTAssertTrue(decoded.isArchived)
        XCTAssertTrue(decoded.attachable)
        let store = AppStore(loadSavedPairing: false)
        store.sessions = [session("current"), decoded]
        XCTAssertEqual(store.currentSessions.map(\.id), ["current"])
        XCTAssertEqual(store.sessions(in: "p1", archived: true).map(\.id), ["hidden"])
        XCTAssertFalse(store.selectSession(id: "hidden"))
    }

    @MainActor func testArchiveClosesOnlyTrillConnectionAndClearsSelection() async throws {
        let fixture = Fixture()
        let current = session("current")
        fixture.rows = [current]
        let store = try await fixture.makeStore()
        store.selectSession(id: current.id)
        let connection = store.connection(for: current.id)
        connection.draftText = "keep my draft"
        var hidden = current
        hidden.archived = true
        fixture.rows = [hidden]
        let archived = await store.action("archive", session: current)
        XCTAssertTrue(archived)
        XCTAssertEqual(fixture.actionRequests, ["archive:current"])
        XCTAssertNil(store.selectedID)
        XCTAssertEqual(store.archiveSessions, [hidden])
        XCTAssertTrue(store.currentSessions.isEmpty)
        XCTAssertEqual(connection.phase, .disconnected)
        XCTAssertEqual(connection.draftText, "keep my draft")
    }

    @MainActor func testFailedArchiveKeepsCurrentSelectionAndDraft() async throws {
        let fixture = Fixture()
        let current = session("current")
        fixture.rows = [current]
        fixture.failAction = true
        let store = try await fixture.makeStore()
        store.selectSession(id: current.id)
        store.connection(for: current.id).draftText = "unfinished"
        let archived = await store.action("archive", session: current)
        XCTAssertFalse(archived)
        XCTAssertEqual(store.selectedID, current.id)
        XCTAssertEqual(store.currentSessions, [current])
        XCTAssertEqual(store.connection(for: current.id).draftText, "unfinished")
    }

    @MainActor func testReopenHiddenLiveSessionUsesSameIdentity() async throws {
        let fixture = Fixture()
        var hidden = session("hidden", resumable: true)
        hidden.archived = true
        fixture.rows = [hidden]
        let store = try await fixture.makeStore()
        var restored = hidden
        restored.archived = false
        fixture.rows = [restored]
        let reopened = await store.action("resume", session: hidden)
        XCTAssertTrue(reopened)
        XCTAssertEqual(fixture.actionRequests, ["resume:hidden"])
        XCTAssertEqual(store.selectedID, hidden.id)
        XCTAssertEqual(store.selectedTab, .sessions)
        XCTAssertEqual(store.currentSessions, [restored])
    }

}

extension SessionBrowserTests {
    @MainActor func testHerdrSpaceHierarchyKeepsDesktopOrderAndOrphanedWorktrees() {
        let store = AppStore(loadSavedPairing: false)
        var root = Project(id: "root", name: "Zebra", path: "/repo")
        root.provider = "herdr"; root.providerName = "Herdr · default"; root.workspaceOrder = 1
        var child = Project(id: "child", name: "Feature", path: "/checkout")
        child.provider = "herdr"; child.providerName = root.providerName; child.workspaceOrder = 3
        child.parentId = root.id; child.isWorktree = true; child.branch = "feature/search"
        var other = Project(id: "other", name: "Alpha", path: "")
        other.provider = "herdr"; other.providerName = root.providerName; other.workspaceOrder = 2
        var orphan = Project(id: "orphan", name: "Orphan", path: "")
        orphan.parentId = "missing"; orphan.provider = "herdr"; orphan.providerName = root.providerName; orphan.workspaceOrder = 4
        store.projects = [child, orphan, other, root]
        XCTAssertEqual(store.spaceRoots.map(\.id), ["root", "other", "orphan"])
        XCTAssertEqual(store.worktrees(in: root.id).map(\.id), ["child"])
        XCTAssertTrue(child.matchesSearch("feature/search"))
        XCTAssertTrue(root.matchesSearch("zebra"))
        XCTAssertFalse(child.matchesSearch("unrelated"))
    }

    func testHerdrIdleLabelDoesNotChangeLegacyRunningLabels() {
        var row = session("idle")
        XCTAssertEqual(row.statusLabel, "Running")
        row.provider = "herdr"
        XCTAssertEqual(row.statusLabel, "Idle")
    }

    func testProjectHierarchyDecodesOlderCompanionsAndNewHerdrMetadata() throws {
        let legacy = try JSONDecoder().decode(Project.self, from: Data(#"{"id":"old","name":"Legacy","path":"/repo"}"#.utf8))
        XCTAssertNil(legacy.parentId)
        XCTAssertNil(legacy.workspaceOrder)
        let current = try JSONDecoder().decode(Project.self, from: Data(#"{"id":"worktree","name":"Search","path":"/checkout","parentId":"root","branch":"feature/search","isWorktree":true,"focused":true,"workspaceOrder":3}"#.utf8))
        XCTAssertEqual(current.parentId, "root")
        XCTAssertEqual(current.branch, "feature/search")
        XCTAssertEqual(current.isWorktree, true)
        XCTAssertEqual(current.focused, true)
        XCTAssertEqual(current.workspaceOrder, 3)
    }

    @MainActor func testHerdrCreationRejectsStaleProviderAndWorktreeTargetsBeforeSending() async throws {
        let store = try await Fixture().makeStore()
        for worktree in [true, false] {
            do {
                try await store.createHerdrItem(isWorktree: worktree, targetID: "removed", name: "Example", directoryOrBranch: worktree ? "feature/test" : "/tmp")
                XCTFail("A removed target must not be used")
            } catch {
                XCTAssertTrue(error is SessionCreationPreflightError)
            }
        }
        XCTAssertFalse(store.busy)
    }
}


extension SessionBrowserTests {
    @MainActor func testHerdrBrowserAndCreationLayouts() async throws {
        let store = try await Fixture().makeStore()
        var capabilities = SessionCapabilities(); capabilities.createTerminal = true; capabilities.worktree = true
        var root = Project(id: "trill", name: "trill", path: "/sample/trill")
        root.provider = "herdr"; root.providerName = "Herdr · default"; root.branch = "main"; root.workspaceOrder = 1; root.capabilities = capabilities
        var child = Project(id: "feature", name: "calm-meadow", path: "/sample/worktrees/calm-meadow")
        child.provider = "herdr"; child.providerName = root.providerName; child.parentId = root.id; child.isWorktree = true
        child.branch = "worktree/calm-meadow"; child.focused = true; child.workspaceOrder = 2; child.capabilities = capabilities
        store.projects = [root, child]
        store.sessions = [
            RemoteSession(provider: "herdr", id: "main-terminal", name: "Rebase", projectId: root.id, projectName: root.name, status: "running", activityState: "idle", agent: "codex", branch: "main", updatedAt: "", attachable: true, resumable: false),
            RemoteSession(provider: "herdr", id: "feature-terminal", name: "Improve Trill integration with Herdr", projectId: child.id, projectName: child.name, status: "running", activityState: "working", agent: "codex", branch: "worktree/calm-meadow", updatedAt: "", attachable: true, resumable: false)
        ]
        store.spaceProviders = [ProviderStatus(id: "herdr:default", name: "Herdr · default", available: true, message: nil, createSpace: true)]
        let multiComputerStore = try await Fixture().makeStore()
        try await multiComputerStore.pair(Computer(name: "Second", address: "https://second.invalid", token: String(repeating: "s", count: 64), fingerprint: ""))
        multiComputerStore.projects = store.projects; multiComputerStore.sessions = store.sessions
        multiComputerStore.spaceProviders = store.spaceProviders; multiComputerStore.sessionBrowserMode = .agents
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let originalWindow = scene.windows.first { $0.isKeyWindow }
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 402, height: 874)
        defer { window.isHidden = true; originalWindow?.makeKey() }
        for (name, view) in [
            ("Herdr spaces", AnyView(NavigationStack { SessionListView(openProjects: {}) }.environment(store))),
            ("Agents", AnyView(NavigationStack { SessionListView(openProjects: {}) }.environment(store))),
            ("Agents multiple computers", AnyView(NavigationStack { SessionListView(openProjects: {}) }.environment(multiComputerStore))),
            ("Agents empty", AnyView(NavigationStack { SessionListView(openProjects: {}) }.environment(store))),
            ("Agents dark", AnyView(NavigationStack { SessionListView(openProjects: {}) }.environment(store).preferredColorScheme(.dark))),
            ("Agents accessibility", AnyView(NavigationStack { SessionListView(openProjects: {}) }.environment(store).environment(\.dynamicTypeSize, .accessibility3))),
            ("New Herdr space", AnyView(NewHerdrItemView(isWorktree: false).environment(store))),
            ("New Herdr worktree", AnyView(NewHerdrItemView(isWorktree: true).environment(store)))
        ] {
            store.sessionBrowserMode = name.hasPrefix("Agents") ? .agents : .sessions
            let originalSessions = store.sessions
            if name == "Agents empty" {
                store.sessions = [detectedAgent("shell", kind: "Shell")]
            } else if name.hasPrefix("Agents") {
                var waiting = detectedAgent("permission", activity: "waiting_for_input", projectID: root.id)
                waiting.name = "Review the requested changes"
                store.sessions.append(waiting)
            }
            defer { store.sessions = originalSessions }
            window.rootViewController = UIHostingController(rootView: view)
            window.makeKeyAndVisible()
            try await Task.sleep(for: .milliseconds(350))
            window.layoutIfNeeded()
            let renderer = UIGraphicsImageRenderer(bounds: window.bounds)
            var drawn = false
            let screenshot = renderer.image { _ in drawn = window.drawHierarchy(in: window.bounds, afterScreenUpdates: true) }
            XCTAssertTrue(drawn, "Layout failed to render: \(name)")
            let attachment = XCTAttachment(image: screenshot)
            attachment.name = name; attachment.lifetime = .keepAlways
            add(attachment)
        }
    }
}


extension SessionBrowserTests {
    @MainActor func testWorktreeSourcesExcludeLinkedAndOrphanedWorktreesEvenWithStaleCapabilities() async throws {
        let store = try await Fixture().makeStore()
        var capabilities = SessionCapabilities(); capabilities.worktree = true; capabilities.createTerminal = true
        var primary = Project(id: "primary", name: "Repository", path: "/repo")
        primary.provider = "herdr"; primary.capabilities = capabilities
        var linked = Project(id: "linked", name: "Feature", path: "/checkout")
        linked.provider = "herdr"; linked.capabilities = capabilities; linked.isWorktree = true; linked.parentId = primary.id
        var orphan = Project(id: "orphan", name: "Other feature", path: "/orphan")
        orphan.provider = "herdr"; orphan.capabilities = capabilities; orphan.isWorktree = true
        store.projects = [linked, orphan, primary]
        XCTAssertEqual(store.worktreeProjects.map(\.id), [primary.id])
        XCTAssertTrue(store.creatableProjects.contains { $0.id == linked.id })
        for sourceID in [linked.id, orphan.id] {
            do {
                try await store.createHerdrItem(isWorktree: true, targetID: sourceID, name: "New work", directoryOrBranch: "feature/new")
                XCTFail("A stale linked-worktree selection must be rejected")
            } catch { XCTAssertEqual(error as? SessionCreationPreflightError, .unsupportedProject) }
        }
        store.projects.removeAll { $0.id == primary.id }
        XCTAssertTrue(store.worktreeProjects.isEmpty)
        XCTAssertFalse(store.busy)
    }
}


extension SessionBrowserTests {
    private func detectedAgent(_ id: String, kind: String = "codex", activity: String = "idle", status: String = "running", projectID: String = "p1") -> RemoteSession {
        RemoteSession(provider: "herdr", providerName: "Herdr · default", id: id, name: "Agent \(id)", projectId: projectID, projectName: "Workspace", status: status, activityState: activity, agent: kind, branch: "feature/agents", updatedAt: "", attachable: true, resumable: false)
    }

    func testAgentDetectionExcludesShellsAndSupportsCustomAgentKinds() {
        for shellKind in ["", " ", "Shell", " terminal ", "NONE", "unknown", "bash", "zsh", "fish", "pwsh"] {
            XCTAssertFalse(detectedAgent("shell", kind: shellKind).hasDetectedAgent, shellKind)
        }
        for agentKind in ["codex", "claude", "cursor", "Custom coding agent"] {
            XCTAssertTrue(detectedAgent("agent", kind: agentKind).hasDetectedAgent, agentKind)
        }
    }

    @MainActor func testAgentsViewFiltersArchivesAndSortsAttentionBeforeWorkingAndIdle() {
        let store = AppStore(loadSavedPairing: false)
        var hidden = detectedAgent("hidden"); hidden.archived = true
        store.sessions = [
            detectedAgent("idle"), detectedAgent("shell", kind: "Shell"),
            detectedAgent("working", activity: "working"), detectedAgent("unknown", activity: "unknown"),
            detectedAgent("waiting", activity: "waiting_for_input"), detectedAgent("error", activity: "error"),
            detectedAgent("ended", status: "completed"), hidden
        ]
        XCTAssertEqual(store.agentSessions.map(\.id), ["error", "waiting", "working", "idle", "unknown"])
        XCTAssertEqual(store.agentSessions.map(\.agentActivityGroup), [.needsAttention, .needsAttention, .working, .idle, .other])
        store.sessions = [detectedAgent("working", kind: "Shell")]
        XCTAssertTrue(store.agentSessions.isEmpty, "An exited agent must disappear when its terminal becomes a shell")
    }

    @MainActor func testAgentsViewSearchIncludesParentSpaceAndKeepsStableSessionIdentities() {
        let store = AppStore(loadSavedPairing: false)
        let primary = Project(id: "primary", name: "Trill", path: "/repo")
        var worktree = Project(id: "feature", name: "Calm meadow", path: "/checkout")
        worktree.parentId = primary.id; worktree.isWorktree = true
        store.projects = [primary, worktree]
        var first = detectedAgent("first", projectID: worktree.id)
        var second = detectedAgent("second", projectID: worktree.id)
        first.name = "Same name"; second.name = "Same name"
        store.sessions = [second, first]
        XCTAssertEqual(store.agentSessions.map(\.id), ["first", "second"])
        XCTAssertEqual(store.agentLocation(for: first), "Trill / Calm meadow")
        for query in ["trill", "MEADOW", "codex", "feature/agents", "same name", "herdr", "  "] {
            XCTAssertTrue(store.agentMatchesSearch(first, search: query), query)
        }
        XCTAssertFalse(store.agentMatchesSearch(first, search: "unrelated"))
        store.projects = []
        XCTAssertEqual(store.agentLocation(for: first), first.projectName, "Partial catalogs retain session context")
    }

    @MainActor func testSelectingAgentKeepsAgentsModeAndDoesNotConnectAutomatically() {
        let store = AppStore(loadSavedPairing: false)
        let agent = detectedAgent("current")
        store.sessions = [agent]
        store.sessionBrowserMode = .agents
        XCTAssertTrue(store.selectSession(id: agent.id))
        XCTAssertEqual(store.selectedID, agent.id)
        XCTAssertEqual(store.sessionBrowserMode, .agents)
        XCTAssertFalse(store.connection(for: agent.id).isOpen)
        XCTAssertFalse(store.selectSession(id: "removed"))
    }
}
