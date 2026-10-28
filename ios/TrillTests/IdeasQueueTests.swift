import XCTest
import SwiftUI
@testable import Trill

final class IdeasQueueTests: XCTestCase {
    @MainActor func testQueueScreenLayout() async throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let queue = IdeasQueueStore(fileURL: directory.appending(path: "ideas.json"))
        let computerID = UUID()
        try queue.save([
            SpaceIdea(text: "Also check dark mode"),
            SpaceIdea(text: "Could this work offline?"),
            SpaceIdea(text: "Try a shorter onboarding flow"),
            SpaceIdea(text: "Review the new empty states", completed: true)
        ], computerID: computerID, projectID: "trill")
        let store = AppStore(loadSavedPairing: false, ideasQueue: queue)
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let originalWindow = scene.windows.first { $0.isKeyWindow }
        let window = UIWindow(windowScene: scene)
        defer { window.isHidden = true; originalWindow?.makeKey() }
        func capture(_ view: some View, name: String) async throws {
            window.rootViewController = UIHostingController(rootView: view.environment(store))
            window.makeKeyAndVisible()
            try await Task.sleep(for: .milliseconds(500))
            window.layoutIfNeeded()
            let image = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
                window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
            }
            let attachment = XCTAttachment(image: image)
            attachment.name = name
            attachment.lifetime = .keepAlways
            add(attachment)
        }
        try await capture(IdeasQueueView(computerID: computerID, projectID: "trill", spaceName: "Trill", useInDraft: { _ in }), name: "Ideas queue")
        let connection = TerminalConnection()
        connection.draftText = "Try this layout for the Ideas queue"
        let image = UIGraphicsImageRenderer(size: CGSize(width: 160, height: 100)).image { context in
            UIColor.systemIndigo.setFill()
            context.fill(CGRect(x: 0, y: 0, width: 160, height: 100))
        }
        connection.draftPhotos = [IdeaPhoto(jpegData: try XCTUnwrap(image.jpegData(compressionQuality: 0.8)))]
        try await capture(TerminalWriter(connection: connection, sessionID: "terminal", sessionName: "Trill", projectID: "trill", spaceName: "Trill"), name: "Write and queue")
        try await capture(TerminalWriter(connection: connection, sessionID: "terminal", sessionName: "Trill", projectID: "trill", spaceName: "Trill")
            .dynamicTypeSize(.accessibility3), name: "Write and queue large text")
    }

    @MainActor func testQueuesPersistOrderEditsCompletionAndScope() throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let fileURL = directory.appending(path: "ideas.json")
        let store = IdeasQueueStore(fileURL: fileURL)
        let computerID = UUID()
        let otherComputerID = UUID()
        let first = SpaceIdea(text: "Check dark mode")
        var second = SpaceIdea(text: "Try offline")
        try store.save([first, second], computerID: computerID, projectID: "space")
        try store.save([first], computerID: otherComputerID, projectID: "space")
        try store.save([second], computerID: computerID, projectID: "worktree")
        second.text = "Try offline on device"
        second.completed = true
        try store.save([second, first], computerID: computerID, projectID: "space")
        let reloaded = IdeasQueueStore(fileURL: fileURL)
        XCTAssertNil(reloaded.loadError)
        XCTAssertEqual(reloaded.ideas(computerID: computerID, projectID: "space"), [second, first])
        XCTAssertEqual(reloaded.ideas(computerID: otherComputerID, projectID: "space"), [first])
        XCTAssertEqual(reloaded.ideas(computerID: computerID, projectID: "worktree").count, 1)
        try reloaded.save([], computerID: computerID, projectID: "space")
        XCTAssertTrue(IdeasQueueStore(fileURL: fileURL).ideas(computerID: computerID, projectID: "space").isEmpty)
    }

    @MainActor func testCorruptFileCannotBeOverwritten() throws {
        let fileURL = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: fileURL) }
        let corruptData = Data("broken queue".utf8)
        try corruptData.write(to: fileURL)
        let store = IdeasQueueStore(fileURL: fileURL)
        XCTAssertNotNil(store.loadError)
        XCTAssertThrowsError(try store.save([SpaceIdea(text: "New idea")], computerID: UUID(), projectID: "space"))
        XCTAssertEqual(try Data(contentsOf: fileURL), corruptData)
    }

    @MainActor func testFailedSavePreservesExistingQueue() throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let fileURL = directory.appending(path: "ideas.json")
        let store = IdeasQueueStore(fileURL: fileURL)
        let computerID = UUID()
        let idea = SpaceIdea(text: "Keep this")
        try store.save([idea], computerID: computerID, projectID: "space")
        try FileManager.default.removeItem(at: directory)
        try Data().write(to: directory)
        XCTAssertThrowsError(try store.save([], computerID: computerID, projectID: "space"))
        XCTAssertEqual(store.ideas(computerID: computerID, projectID: "space"), [idea])
        XCTAssertThrowsError(try store.save([SpaceIdea(text: "  ")], computerID: computerID, projectID: "space"))
    }
    @MainActor func testPhotoOnlyIdeaPersistsAndLegacyIdeasStillLoad() throws {
        let fileURL = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: fileURL) }
        let computerID = UUID()
        let legacyJSON = """
        [{"computerID":"\(computerID)","projectID":"space","ideas":[{"id":"\(UUID())","text":"Old idea","completed":false}]}]
        """
        try Data(legacyJSON.utf8).write(to: fileURL)
        let store = IdeasQueueStore(fileURL: fileURL)
        XCTAssertNil(store.loadError)
        let legacy = try XCTUnwrap(store.ideas(computerID: computerID, projectID: "space").first)
        XCTAssertNil(legacy.photos)
        let photo = IdeaPhoto(jpegData: Data([1, 2, 3]))
        let photoIdea = SpaceIdea(text: "", photos: [photo])
        try store.save([legacy, photoIdea], computerID: computerID, projectID: "space")
        let reloaded = IdeasQueueStore(fileURL: fileURL)
        XCTAssertEqual(reloaded.ideas(computerID: computerID, projectID: "space"), [legacy, photoIdea])
    }

    @MainActor func testPhotoDraftClearsOnlyAfterTransportHandoff() async throws {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.draftText = "Review /tmp/photo.jpg"
        let photo = IdeaPhoto(jpegData: Data([1]), uploadedPath: "/tmp/photo.jpg")
        connection.draftPhotos = [photo]
        XCTAssertTrue(connection.insertDraft())
        XCTAssertEqual(connection.draftPhotos, [photo])
        var iterator = channel.stream.makeAsyncIterator()
        let firstInput = await iterator.next()
        let firstEnvelope = try XCTUnwrap(firstInput)
        do {
            try await connection.handoff(firstEnvelope) { _ in throw TrillError.message("Disconnected") }
            XCTFail("Expected transport failure")
        } catch { }
        XCTAssertEqual(connection.draftPhotos, [photo])
        XCTAssertEqual(connection.draftText, "Review /tmp/photo.jpg")
        XCTAssertTrue(connection.insertDraft())
        let secondInput = await iterator.next()
        let secondEnvelope = try XCTUnwrap(secondInput)
        try await connection.handoff(secondEnvelope) { _ in }
        XCTAssertTrue(connection.draftPhotos.isEmpty)
        XCTAssertTrue(connection.draftText.isEmpty)
    }

}
