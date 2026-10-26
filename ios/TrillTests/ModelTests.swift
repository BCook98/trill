import XCTest
import SwiftUI
import MoshCore
import UIKit
import ImageIO
import UniformTypeIdentifiers
import WebKit
import AVKit
import Network
@testable import Trill

final class ModelTests: XCTestCase {
    let token = String(repeating: "a", count: 64)
    func testRealVideoTransferReportsProgressAndUsesBearerAuth() async throws {
        let received = expectation(description: "authenticated video request")
        let progress = expectation(description: "real URLSession progress callback")
        progress.assertForOverFulfill = false
        let payload = Data(repeating: 7, count: 128 * 1024)
        let server = try VideoDownloadTestServer(body: payload, declaredSize: payload.count) { request in
            XCTAssertTrue(request.contains("GET /v1/sessions/s1/video?path=folder/clip.mp4 HTTP/1.1"))
            XCTAssertTrue(request.lowercased().contains("authorization: bearer " + String(repeating: "a", count: 64)))
            received.fulfill()
        }
        defer { server.stop() }
        let address = try await server.start()
        let api = try APIClient(computer: Computer(name: "Video test", address: address.absoluteString, token: token, fingerprint: ""))
        var file: WorkspaceVideoFile? = try await api.workspaceVideo(sessionID: "s1", path: "folder/clip.mp4") { value in
            if value != nil { progress.fulfill() }
        }
        await fulfillment(of: [received, progress], timeout: 3)
        let url = try XCTUnwrap(file?.url)
        XCTAssertEqual(try Data(contentsOf: url), payload)
        file = nil
        XCTAssertFalse(FileManager.default.fileExists(atPath: url.path))
    }
    func testRealVideoTransferCancelsBeforeOversizedResponseFinishes() async throws {
        let server = try VideoDownloadTestServer(body: Data(repeating: 0, count: 65536), declaredSize: Int(WorkspaceVideoFile.maximumSize + 1))
        defer { server.stop() }
        let address = try await server.start()
        let delegate = WorkspaceVideoDownloadDelegate { _ in }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 3
        do {
            _ = try await delegate.download(request: URLRequest(url: address), configuration: configuration)
            XCTFail("Oversized transfer succeeded")
        } catch {
            XCTAssertTrue(delegate.exceededLimit)
            XCTAssertEqual(error.localizedDescription, WorkspaceVideoFile.sizeMessage)
        }
    }
    func testRealVideoTransferCancellationAndRedirectRejection() async throws {
        let progress = expectation(description: "download has started")
        progress.assertForOverFulfill = false
        let server = try VideoDownloadTestServer(body: Data(repeating: 0, count: 65536), declaredSize: 1024 * 1024)
        defer { server.stop() }
        let address = try await server.start()
        let delegate = WorkspaceVideoDownloadDelegate { _ in progress.fulfill() }
        let task = Task { try await delegate.download(request: URLRequest(url: address), configuration: .ephemeral) }
        await fulfillment(of: [progress], timeout: 3)
        task.cancel()
        do { _ = try await task.value; XCTFail("Cancelled download succeeded") }
        catch { XCTAssertTrue(error is CancellationError) }
        let redirect = try VideoDownloadTestServer(body: Data(), declaredSize: 0, status: "302 Found", extraHeaders: "Location: https://outside.invalid/video\r\n")
        defer { redirect.stop() }
        let redirectURL = try await redirect.start()
        do {
            _ = try await WorkspaceVideoDownloadDelegate { _ in }.download(request: URLRequest(url: redirectURL), configuration: .ephemeral)
            XCTFail("Redirect was accepted")
        } catch { XCTAssertEqual((error as? APIRequestError)?.status, 302) }
    }
    func testVideoEntriesUseNativePreviewOnlyForSupportedFiles() {
        for name in ["clip.mp4", "clip.MOV", "clip.m4v"] {
            XCTAssertTrue(WorkspaceEntry(name: name, path: name, kind: "file", size: 12).isVideo)
            XCTAssertFalse(WorkspaceEntry(name: name, path: name, kind: "directory", size: 0).isVideo)
        }
        for name in ["clip.webm", "clip.m3u8", "clip.mp4.html"] {
            XCTAssertFalse(WorkspaceEntry(name: name, path: name, kind: "file", size: 12).isVideo)
        }
    }
    func testVideoDownloadOwnsPrivateFileAndDeletesOnRelease() throws {
        let input = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: input) }
        try Data([0, 1, 2, 3]).write(to: input)
        let response = HTTPURLResponse(url: URL(string: "https://mac.test/video")!, statusCode: 200,
                                       httpVersion: nil, headerFields: ["Content-Type": "video/quicktime", "Content-Length": "4"])!
        var file: WorkspaceVideoFile? = try WorkspaceVideoFile(download: input, response: response)
        let url = try XCTUnwrap(file?.url)
        XCTAssertEqual(url.pathExtension, "mov")
        XCTAssertFalse(FileManager.default.fileExists(atPath: input.path))
        XCTAssertEqual(try Data(contentsOf: url), Data([0, 1, 2, 3]))
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: url.path)[.posixPermissions] as? Int, 0o600)
        file = nil
        XCTAssertFalse(FileManager.default.fileExists(atPath: url.deletingLastPathComponent().path))
    }
    func testVideoDownloadRejectsWrongTypeTruncationEmptyAndOversize() throws {
        let input = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: input) }
        func response(_ type: String, _ length: Int64) -> HTTPURLResponse {
            HTTPURLResponse(url: URL(string: "https://mac.test/video")!, statusCode: 200,
                            httpVersion: nil, headerFields: ["Content-Type": type, "Content-Length": String(length)])!
        }
        try Data([0, 1, 2, 3]).write(to: input)
        XCTAssertThrowsError(try WorkspaceVideoFile(download: input, response: response("text/html", 4)))
        XCTAssertThrowsError(try WorkspaceVideoFile(download: input, response: response("video/mp4", 5)))
        try Data().write(to: input)
        XCTAssertThrowsError(try WorkspaceVideoFile(download: input, response: response("video/mp4", 0)))
        let handle = try FileHandle(forWritingTo: input)
        try handle.truncate(atOffset: UInt64(WorkspaceVideoFile.maximumSize + 1))
        try handle.close()
        XCTAssertThrowsError(try WorkspaceVideoFile(download: input, response: response("video/mp4", WorkspaceVideoFile.maximumSize + 1)))
        XCTAssertTrue(FileManager.default.fileExists(atPath: input.path)) // caller still owns rejected input
    }
    func testVideoDownloadCancelsAtLimitAndRefusesRedirects() {
        let delegate = WorkspaceVideoDownloadDelegate { _ in }
        let session = URLSession(configuration: .ephemeral)
        defer { session.invalidateAndCancel() }
        let request = URLRequest(url: URL(string: "https://mac.test/video")!)
        let task = session.downloadTask(with: request)
        delegate.urlSession(session, downloadTask: task, didWriteData: 1,
                            totalBytesWritten: 1, totalBytesExpectedToWrite: WorkspaceVideoFile.maximumSize + 1)
        XCTAssertTrue(delegate.exceededLimit)
        XCTAssertEqual(task.state, .canceling)
        let redirect = HTTPURLResponse(url: request.url!, statusCode: 302, httpVersion: nil, headerFields: nil)!
        delegate.urlSession(session, task: task, willPerformHTTPRedirection: redirect,
                            newRequest: URLRequest(url: URL(string: "https://other.test/")!)) { XCTAssertNil($0) }
    }
    @MainActor func testVideoAssetCannotReferenceOtherFilesOrNetwork() async throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("nonexistent-video.mp4")
        let asset = WorkspaceVideoPlayer.asset(for: url)
        XCTAssertEqual(asset.referenceRestrictions, .forbidAll)
        let player = WorkspaceVideoPlayer()
        player.close()
        XCTAssertNil(player.player)
    }

    func testTerminalLinkRoutingKeepsLocalURLsOnComputer() throws {
        let computer = URL(string: "https://mac.example.ts.net:7443")!
        for host in ["localhost", "LOCALHOST", "127.0.0.1", "[::1]", "0.0.0.0", "[::]", "mac.example.ts.net"] {
            let url = try XCTUnwrap(TerminalLinkRoute.webURL("http://\(host):5173/a%20b?q=a%26b#section"))
            XCTAssertEqual(TerminalLinkRoute.destination(for: url, computer: computer),
                           .preview(URL(string: "http://localhost:5173/a%20b?q=a%26b#section")!), host)
        }
        for text in ["https://github.com/BCook98/trill/issues/1", "https://example.com", "http://localhost.evil.test:3000", "http://192.168.1.99:3000", "http://mac.example.ts.net:7443"] {
            let url = try XCTUnwrap(TerminalLinkRoute.webURL(text))
            XCTAssertEqual(TerminalLinkRoute.destination(for: url, computer: computer), .external(url), text)
        }
        let secureLocal = URL(string: "https://localhost:5173/")!
        XCTAssertEqual(TerminalLinkRoute.destination(for: secureLocal, computer: computer), .preview(secureLocal))
        XCTAssertThrowsError(try WorkspaceAddress.parse(secureLocal.absoluteString)) // no TLS downgrade
    }

    func testTerminalLinksRejectCredentialsAndExecutableSchemes() {
        for text in ["javascript:alert(1)", "file:///etc/passwd", "ssh://mac", "github://repo", "https://user:secret@github.com/repo", "https:///", "https://example.com:65536", "https://example.com:0", "https://example.com/\nnext", String(repeating: "a", count: 8193)] {
            XCTAssertNil(TerminalLinkRoute.webURL(text), text)
        }
    }

    @MainActor func testSingleTapOpensImplicitAndExplicitLinksWithoutHover() throws {
        for (text, expected) in [
            ("http://localhost:5173/demo", "http://localhost:5173/demo"),
            ("https://github.com/BCook98/trill", "https://github.com/BCook98/trill"),
            ("\u{1b}]8;;https://github.com/BCook98/trill\u{1b}\\Repository\u{1b}]8;;\u{1b}\\", "https://github.com/BCook98/trill")
        ] {
            let connection = TerminalConnection()
            let view = connection.terminal
            view.feed(text: text)
            XCTAssertNil(connection.pendingLink, "Output must never automatically open a link")
            let dimensions = view.terminalDimensions
            let frame = view.getOptimalFrameSize()
            let tap = TerminalLinkTestTap()
            tap.point = CGPoint(x: frame.width / CGFloat(dimensions.cols) * 3.5,
                                y: frame.height / CGFloat(dimensions.rows) * 0.5)
            view.addGestureRecognizer(tap)
            _ = view.perform(NSSelectorFromString("singleTap:"), with: tap)
            XCTAssertEqual(connection.pendingLink?.absoluteString, expected)
        }
    }

    @MainActor func testTerminalLinkTapFollowsWrappedRowsAndScrollback() throws {
        let connection = TerminalConnection()
        let view = connection.terminal
        view.resize(cols: 24, rows: 6)
        let url = "https://github.com/BCook98/trill/issues/123"
        view.feed(text: url + "\r\n" + String(repeating: "filler\r\n", count: 12))
        view.scrollTo(row: 0)
        let frame = view.getOptimalFrameSize()
        let tap = TerminalLinkTestTap()
        tap.point = CGPoint(x: frame.width / 24 * 5.5, y: frame.height / 6 * 1.5)
        view.addGestureRecognizer(tap)
        _ = view.perform(NSSelectorFromString("singleTap:"), with: tap)
        XCTAssertEqual(connection.pendingLink?.absoluteString, url)
    }

    @MainActor func testTerminalTapRejectsExecutableOSCLink() {
        let connection = TerminalConnection()
        let view = connection.terminal
        view.feed(text: "\u{1b}]8;;javascript:alert(1)\u{1b}\\https://github.com\u{1b}]8;;\u{1b}\\")
        let frame = view.getOptimalFrameSize()
        let tap = TerminalLinkTestTap()
        tap.point = CGPoint(x: frame.width / CGFloat(view.terminalDimensions.cols) * 3.5,
                            y: frame.height / CGFloat(view.terminalDimensions.rows) * 0.5)
        view.addGestureRecognizer(tap)
        _ = view.perform(NSSelectorFromString("singleTap:"), with: tap)
        XCTAssertNil(connection.pendingLink)
    }

    func testWorkspaceAddressesAndProxyPreserveEncodedPaths() throws {
        for address in ["localhost:3000", "http://127.0.0.1:8080", "http://[::1]:5173"] {
            XCTAssertNotNil(try WorkspaceAddress.parse(address).localPort)
        }
        let source = try WorkspaceAddress.parse("localhost:3000/a%20b/index.html?name=a%26b#section")
        let routed = try XCTUnwrap(source.through(URL(string: "https://mac.example:7443")!))
        XCTAssertEqual(routed.absoluteString, "https://mac.example:7443/a%20b/index.html?name=a%26b#section")
        XCTAssertNil(try WorkspaceAddress.parse("https://example.com/page").localPort)
        XCTAssertEqual(try WorkspaceAddress.parse("example.com").url.scheme, "https")
    }
    func testLoopbackNavigationOnlyAllowsExactAuthenticatedProxyOrigin() throws {
        let proxy = try XCTUnwrap(URL(string: "https://localhost:19443"))
        let page = try XCTUnwrap(URL(string: "https://localhost:19443/index.html?view=grid"))
        XCTAssertFalse(WorkspaceAddress.isDirectLoopback(page, authenticatedProxy: proxy))
        XCTAssertTrue(WorkspaceAddress.isDirectLoopback(page, authenticatedProxy: nil))
        for address in ["http://localhost:19443", "https://localhost:19444", "http://localhost:18080", "https://127.0.0.1:19443", "http://[::1]:18080"] {
            XCTAssertTrue(WorkspaceAddress.isDirectLoopback(try XCTUnwrap(URL(string: address)), authenticatedProxy: proxy), address)
        }
    }

    func testWorkspaceRejectsUnsafeAndAmbiguousAddresses() {
        for address in ["", "javascript:alert(1)", "file:///etc/passwd", "https://user:secret@example.com", "http://localhost", "https://localhost:3000", "localhost:80", "localhost:65536"] {
            XCTAssertThrowsError(try WorkspaceAddress.parse(address), address)
        }
    }
    func testStaticDocumentMapsOnlyPresentationAssetsInsideSelectedFolder() throws {
        let entry = WorkspaceEntry(name: "hello world.html", path: "public/demo/hello world.html", kind: "file", size: 10)
        let document = try WorkspaceDocument(entry: entry)
        XCTAssertEqual(try document.resource(document.url).path, entry.path)
        let style = try XCTUnwrap(URL(string: "assets/site.css?v=2", relativeTo: document.url)?.absoluteURL)
        XCTAssertEqual(try document.resource(style).path, "public/demo/assets/site.css")
        XCTAssertEqual(try document.resource(style).mime, "text/css")
        let rootImage = try XCTUnwrap(URL(string: "/photo.png", relativeTo: document.url)?.absoluteURL)
        XCTAssertEqual(try document.resource(rootImage).path, "public/demo/photo.png")
        XCTAssertTrue(WorkspaceEntry(name: "diagram.SVG", path: "diagram.SVG", kind: "file", size: 10).canRender)
        XCTAssertFalse(WorkspaceEntry(name: "page.html", path: "page.html", kind: "directory", size: 10).canRender)
    }
    func testStaticDocumentRejectsSecretsTraversalAndForeignOrigins() throws {
        let document = try WorkspaceDocument(entry: WorkspaceEntry(name: "index.html", path: "public/index.html", kind: "file", size: 1))
        let origin = "trill-file://" + document.url.host!
        for suffix in ["/.env", "/.git/config", "/../private.html", "/%2e%2e/private.html", "/a%2f..%2fprivate.html", "/a%5cb.html", "/key.pem", "/settings.json", "/a//b.html", "/a%00b.html"] {
            XCTAssertThrowsError(try document.resource(XCTUnwrap(URL(string: origin + suffix))), suffix)
        }
        for address in ["https://example.com/index.html", "file:///index.html", "trill-file://other/index.html", origin + ":7443/index.html", "trill-file://user@" + document.url.host! + "/index.html"] {
            XCTAssertThrowsError(try document.resource(XCTUnwrap(URL(string: address))), address)
        }
        for path in ["../index.html", "/index.html", ".private/index.html"] {
            XCTAssertThrowsError(try WorkspaceDocument(entry: WorkspaceEntry(name: "index.html", path: path, kind: "file", size: 1)))
        }
    }
    // WebKit's protocol is nonisolated; this fake is called only by the
    // main-actor scheme handler. Check that isolation at runtime as well.
    @MainActor private final class StaticSchemeTask: NSObject, @preconcurrency WKURLSchemeTask {
        let request: URLRequest
        var callbacks = 0
        init(url: URL) { request = URLRequest(url: url) }
        func didReceive(_ response: URLResponse) { callbacks += 1 }
        func didReceive(_ data: Data) { callbacks += 1 }
        func didFinish() { callbacks += 1 }
        func didFailWithError(_ error: Error) { callbacks += 1 }
    }
    @MainActor func testStaticPreviewCancellationNeverCallsStoppedWebKitTask() async throws {
        let document = try WorkspaceDocument(entry: WorkspaceEntry(name: "index.html", path: "public/index.html", kind: "file", size: 1))
        let started = expectation(description: "Authenticated asset read started")
        var pending: CheckedContinuation<WorkspaceFile, Error>?
        let handler = WorkspaceFileHandler(document: document) { path in
            XCTAssertEqual(path, "index.html")
            return try await withCheckedThrowingContinuation { pending = $0; started.fulfill() }
        }
        let web = WKWebView()
        let schemeTask = StaticSchemeTask(url: document.url)
        handler.webView(web, start: schemeTask)
        await fulfillment(of: [started], timeout: 2)
        handler.webView(web, stop: schemeTask)
        pending?.resume(returning: WorkspaceFile(data: Data("<p>late</p>".utf8), contentType: "text/plain"))
        for _ in 0..<20 { await Task.yield() }
        XCTAssertEqual(schemeTask.callbacks, 0, "WebKit forbids callbacks after stop")
        handler.close()
        let next = StaticSchemeTask(url: document.url)
        handler.webView(web, start: next)
        XCTAssertEqual(next.callbacks, 1, "Closed previews must fail without reading more files")
    }
    func testPreviewCookieIsPrivateAndExpires() throws {
        let base = URL(string: "https://mac.example:7443")!
        let grant = PreviewGrant(id: "test", token: token, expiresAt: "2099-01-02T03:04:05Z")
        let cookie = try XCTUnwrap(grant.cookie(for: base))
        XCTAssertTrue(cookie.isSecure)
        XCTAssertTrue(cookie.isHTTPOnly)
        XCTAssertEqual(cookie.sameSitePolicy, .sameSiteStrict)
        XCTAssertEqual(cookie.domain, "mac.example")
        XCTAssertEqual(cookie.path, "/")
        XCTAssertEqual(cookie.name, "trill_preview")
        XCTAssertNil(PreviewGrant(id: "old", token: token, expiresAt: "2020-01-01T00:00:00Z").cookie(for: base))
        XCTAssertNil(PreviewGrant(id: "bad", token: "secret", expiresAt: grant.expiresAt).cookie(for: base))
    }
    func testWorkspaceListingAndGrantDecode() throws {
        let listing = try JSONDecoder().decode(WorkspaceListing.self, from: Data(#"{"path":"src","entries":[{"name":"main.swift","path":"src/main.swift","kind":"file","size":12}],"truncated":false}"#.utf8))
        XCTAssertEqual(listing.entries.first?.id, "src/main.swift")
        XCTAssertEqual(listing.entries.first?.size, 12)
        let grant = PreviewGrant(id: "test", token: token, expiresAt: "2027-01-02T03:04:05.123456789Z")
        XCTAssertNotNil(grant.expiry)
    }
    func testPairingRejectsInsecureRemoteAndCredentialURLs() {
        for address in ["http://mac.local:7443", "https://user:pass@mac.local", "https://mac.local/a", "https://mac.local?token=123", "file:///tmp/key"] {
            XCTAssertThrowsError(try Computer.validate(address: address, token: token, fingerprint: "", allowLoopback: true), address)
        }
    }
    func testHTTPSAndExplicitLoopback() throws {
        XCTAssertEqual(try Computer.validate(address: "https://mac.local:7443", token: token, fingerprint: "").host, "mac.local")
        XCTAssertThrowsError(try Computer.validate(address: "http://127.0.0.1:7443", token: token, fingerprint: ""))
        XCTAssertNoThrow(try Computer.validate(address: "http://127.0.0.1:7443", token: token, fingerprint: "", allowLoopback: true))
    }
    func testInvalidTokenAndPinFailBeforeNetworking() {
        XCTAssertThrowsError(try Computer.validate(address: "https://mac.local", token: "short", fingerprint: ""))
        XCTAssertThrowsError(try Computer.validate(address: "https://mac.local", token: token, fingerprint: "wrong-pin"))
    }
    func testKeychainRoundTripAndUpdate() throws {
        let account = "test-" + UUID().uuidString
        defer { try? PairingStore.delete(account: account) }
        var computer = Computer(name: "Test computer", address: "https://example.invalid", token: token, fingerprint: "")
        try PairingStore.save(computer, account: account)
        XCTAssertEqual(PairingStore.load(account: account)?.token, token)
        computer.name = "Updated computer"
        try PairingStore.save(computer, account: account)
        XCTAssertEqual(PairingStore.load(account: account)?.name, "Updated computer")
        try PairingStore.delete(account: account)
        XCTAssertNil(PairingStore.load(account: account))
    }
    func testSessionContractAndAttention() throws {
        let json = #"{"id":"one","name":"Example","projectId":"p","projectName":"Project","status":"running","activityState":"waiting_for_input","agent":"codex","branch":"feature","updatedAt":"2026-10-23T10:00:00Z","attachable":true,"resumable":false}"#
        let session = try JSONDecoder().decode(RemoteSession.self, from: Data(json.utf8))
        XCTAssertTrue(session.needsAttention)
        XCTAssertEqual(session.statusLabel, "Needs attention")
        XCTAssertTrue(session.attachable)
    }
    func testTerminalViewportAvoidsActiveDivision() {
        let bounds = CGRect(x: 0, y: 0, width: 800, height: 600)
        let bookFold = CGRect(x: 390, y: 0, width: 20, height: 600)
        XCTAssertEqual(TerminalViewportLayout.visibleRect(in: bounds, excluding: [bookFold]),
                       CGRect(x: 410, y: 0, width: 390, height: 600))
        let tabletopFold = CGRect(x: 0, y: 290, width: 800, height: 20)
        XCTAssertEqual(TerminalViewportLayout.visibleRect(in: bounds, excluding: [tabletopFold]),
                       CGRect(x: 0, y: 0, width: 800, height: 290))
    }
    func testTabletopKeepsOutputAboveFoldAndActionsAboveKeyboard() {
        let fold = CGRect(x: 0, y: 390, width: 640, height: 20)
        let full = TerminalViewportLayout.interactionRegions(in: CGRect(x: 0, y: 0, width: 640, height: 900),
            divisions: [fold], occlusions: [], controlsHeight: 50)
        let keyboard = TerminalViewportLayout.interactionRegions(in: CGRect(x: 0, y: 0, width: 640, height: 580),
            divisions: [fold], occlusions: [], controlsHeight: 50)
        XCTAssertEqual(full.output, CGRect(x: 0, y: 0, width: 640, height: 390))
        XCTAssertEqual(keyboard.output, full.output)
        XCTAssertEqual(full.controls, CGRect(x: 0, y: 850, width: 640, height: 50))
        XCTAssertEqual(keyboard.controls, CGRect(x: 0, y: 530, width: 640, height: 50))
        XCTAssertGreaterThanOrEqual(keyboard.controls.minY, fold.maxY)
    }
    func testTabletopKeyboardReservingLowerDisplayKeepsActionsBelowFold() {
        let bounds = CGRect(x: 0, y: 0, width: 669, height: 752)
        let fold = CGRect(x: 0, y: 339, width: 669, height: 40)
        let regions = TerminalViewportLayout.interactionRegions(in: bounds, divisions: [fold],
            occlusions: [], controlsHeight: 50, keyboardTop: 339)
        XCTAssertEqual(regions.output, CGRect(x: 0, y: 0, width: 669, height: 339))
        XCTAssertEqual(regions.controls, CGRect(x: 0, y: 379, width: 669, height: 50))
    }
    func testOrdinaryKeyboardKeepsControlsAboveItsFrame() {
        let regions = TerminalViewportLayout.interactionRegions(in: CGRect(x: 0, y: 0, width: 400, height: 800),
            divisions: [], occlusions: [], controlsHeight: 50, keyboardTop: 500)
        XCTAssertEqual(regions.output, CGRect(x: 0, y: 0, width: 400, height: 450))
        XCTAssertEqual(regions.controls, CGRect(x: 0, y: 450, width: 400, height: 50))
    }
    func testZeroHeightControlsPreserveOutput() {
        let bounds = CGRect(x: 0, y: 0, width: 669, height: 339)
        let fold = CGRect(x: 0, y: 339, width: 669, height: 40)
        let regions = TerminalViewportLayout.interactionRegions(in: bounds, divisions: [fold], occlusions: [], controlsHeight: 0)
        XCTAssertEqual(regions.output, bounds)
        XCTAssertEqual(regions.controls, .zero)
    }
    func testControlsStayReachableWhenLowerRegionIsTooShort() {
        let bounds = CGRect(x: 0, y: 0, width: 640, height: 440)
        let fold = CGRect(x: 0, y: 390, width: 640, height: 20)
        let regions = TerminalViewportLayout.interactionRegions(in: bounds, divisions: [fold], occlusions: [], controlsHeight: 70)
        XCTAssertEqual(regions.controls.height, 70)
        XCTAssertLessThanOrEqual(regions.controls.maxY, fold.minY)
        XCTAssertFalse(regions.output.intersects(regions.controls))
    }

    func testTerminalControlsRespectBookFoldAndCamera() {
        let bounds = CGRect(x: 0, y: 0, width: 800, height: 600)
        let fold = CGRect(x: 390, y: 0, width: 20, height: 600)
        let camera = CGRect(x: 770, y: 500, width: 30, height: 100)
        let regions = TerminalViewportLayout.interactionRegions(in: bounds, divisions: [fold],
            occlusions: [camera], controlsHeight: 50)
        XCTAssertFalse(regions.output.intersects(fold))
        XCTAssertFalse(regions.controls.intersects(fold))
        XCTAssertFalse(regions.controls.intersects(camera))
        XCTAssertFalse(regions.output.intersects(regions.controls))
        let flat = TerminalViewportLayout.interactionRegions(in: bounds, divisions: [], occlusions: [], controlsHeight: 50)
        XCTAssertEqual(flat.output, CGRect(x: 0, y: 0, width: 800, height: 550))
        XCTAssertEqual(flat.controls, CGRect(x: 0, y: 550, width: 800, height: 50))
    }

    func testTerminalViewportUsesAllSpaceWhenFlatAndAvoidsCamera() {
        let bounds = CGRect(x: 0, y: 0, width: 800, height: 600)
        XCTAssertEqual(TerminalViewportLayout.visibleRect(in: bounds, excluding: []), bounds)
        XCTAssertEqual(TerminalViewportLayout.visibleRect(in: bounds, excluding: [CGRect(x: 900, y: 0, width: 20, height: 600)]), bounds)
        let camera = CGRect(x: 370, y: 0, width: 60, height: 40)
        let visible = TerminalViewportLayout.visibleRect(in: bounds, excluding: [camera])
        XCTAssertEqual(visible, CGRect(x: 0, y: 40, width: 800, height: 560))
        XCTAssertFalse(visible.intersects(camera.insetBy(dx: 1, dy: 1)))
    }

    func testInvitationValidationAndLinkRoundTrip() throws {
        let expiry = Date().addingTimeInterval(300)
        let invitation = PairingInvitation(version: 1, address: "https://mac.example:7443", name: "My Mac", fingerprint: "", code: String(repeating: "b", count: 64), expiresAt: expiry)
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.outputFormatting = .withoutEscapingSlashes
        let data = try encoder.encode(invitation)
        let json = String(data: data, encoding: .utf8)!
        XCTAssertEqual(try PairingInvitation.parse(json).name, "My Mac")
        let payload = data.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
        XCTAssertEqual(try PairingInvitation.parse("trill://pair?data=" + payload).address, invitation.address)
        XCTAssertThrowsError(try PairingInvitation.parse(json, now: expiry.addingTimeInterval(1)))
        XCTAssertThrowsError(try PairingInvitation.parse(json.replacingOccurrences(of: "https://", with: "http://")))
        XCTAssertThrowsError(try PairingInvitation.parse("trill://pair?data=" + payload + "&data=" + payload))
        XCTAssertThrowsError(try PairingInvitation.parse("https://example.com/?data=" + payload))
        XCTAssertThrowsError(try PairingInvitation.parse(json.replacingOccurrences(of: invitation.code, with: "short")))
    }
    func testOpenSSLFingerprintAndTLSDiagnostics() throws {
        let fingerprint = "SHA256 Fingerprint=" + Array(repeating: "AB", count: 32).joined(separator: ":") + "\n"
        XCTAssertEqual(CertificateFingerprint.normalized(fingerprint), String(repeating: "ab", count: 32))
        XCTAssertNoThrow(try Computer.validate(address: "https://example.com", token: token, fingerprint: fingerprint))
        let missing = ConnectionDiagnostic.message(for: URLError(.serverCertificateUntrusted), hasPin: false)
        XCTAssertTrue(missing.contains("fingerprint"))
        XCTAssertEqual(ConnectionDiagnostic.message(for: URLError(.cancelled), hasPin: true, pinFailure: "Mismatch"), "Mismatch")
        XCTAssertTrue(ConnectionDiagnostic.message(for: URLError(.secureConnectionFailed), hasPin: false).contains("-1200"))
    }
    func testConfiguredCompanionTLSWithoutAccessingSessions() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let address = env["TRILL_PROBE_URL"], let url = URL(string: address) else { throw XCTSkip("Opt-in TLS probe URL not configured") }
        let delegate = PinnedSessionDelegate(fingerprint: env["TRILL_PROBE_PIN"] ?? "")
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 10
        let session = URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        do {
            let (_, response) = try await session.data(from: url.appendingPathComponent("v1/health"))
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 401, "No credentials are sent; authenticated routes must reject this probe")
        } catch {
            XCTFail(ConnectionDiagnostic.message(for: error, hasPin: !(env["TRILL_PROBE_PIN"] ?? "").isEmpty, pinFailure: delegate.pinFailure))
        }
    }

    func testConfiguredCompanionRejectsWrongPin() async throws {
        guard let address = ProcessInfo.processInfo.environment["TRILL_PROBE_URL"], let url = URL(string: address) else { throw XCTSkip("Opt-in TLS probe URL not configured") }
        let delegate = PinnedSessionDelegate(fingerprint: String(repeating: "0", count: 64))
        let config = URLSessionConfiguration.ephemeral; config.timeoutIntervalForRequest = 10
        let session = URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        do { _ = try await session.data(from: url.appendingPathComponent("v1/health")); XCTFail("Mismatched certificate was accepted") }
        catch { XCTAssertNotNil(delegate.pinFailure, "The pin check must reject the certificate") }
    }

    @MainActor func testPendingEnrollmentSurvivesPersistenceAndNetworkFailures() async throws {
        let current = Computer(name: "Current", address: "https://current.example", token: token, fingerprint: "")
        let next = Computer(name: "Next", address: "https://next.example", token: token, fingerprint: "")
        var records = ["primary": current]
        var saves = 0
        var healthChecks = 0
        let store = AppStore(loadSavedPairing: false, savePairing: { computer, account in
            saves += 1
            if saves == 1 { throw TrillError.message("Keychain temporarily unavailable") }
            records[account] = computer
        }, deletePairing: { records.removeValue(forKey: $0) }, checkHealth: { _ in
            healthChecks += 1
            throw URLError(.notConnectedToInternet)
        })
        store.computer = current
        do { try await store.pairEnrollment(next); XCTFail("Expected persistence failure") } catch {}
        XCTAssertEqual(store.pendingEnrollment, next)
        XCTAssertFalse(store.pendingEnrollmentSaved)
        XCTAssertEqual(healthChecks, 0)
        do { try await store.pairEnrollment(next); XCTFail("Expected connection failure") } catch {}
        XCTAssertEqual(records["pending-enrollment"], next)
        XCTAssertTrue(store.pendingEnrollmentSaved)
        XCTAssertEqual(records["primary"], current)
        XCTAssertEqual(store.computer, current)
        XCTAssertEqual(healthChecks, 1)
        try store.discardPendingEnrollment()
        XCTAssertNil(store.pendingEnrollment)
        XCTAssertNil(records["pending-enrollment"])
        XCTAssertEqual(records["primary"], current)
    }

    @MainActor private func collectedInput(
        _ channel: (stream: AsyncStream<TerminalConnection.InputEnvelope>, continuation: AsyncStream<TerminalConnection.InputEnvelope>.Continuation),
        handoffTo connection: TerminalConnection? = nil
    ) async -> [MoshClientOp] {
        channel.continuation.finish()
        var operations: [MoshClientOp] = []
        for await envelope in channel.stream {
            operations.append(envelope.operation)
            if let connection {
                do { try await connection.handoff(envelope) { _ in } }
                catch { XCTFail("Unexpected handoff failure: \(error)") }
            }
        }
        return operations
    }

    @MainActor func testDraftInsertsExactUnicodeWithoutReturnAndClearsOnlyOnce() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        XCTAssertEqual(connection.draftText, "")
        connection.draftText = "echo café 日本語 🙂"
        XCTAssertNil(connection.draftValidationMessage)
        XCTAssertTrue(connection.insertDraft())
        XCTAssertEqual(connection.draftText, "echo café 日本語 🙂")
        XCTAssertTrue(connection.draftInsertionPending)
        XCTAssertNotNil(connection.draftValidationMessage)
        XCTAssertFalse(connection.insertDraft())
        let operations = await collectedInput(channel, handoffTo: connection)
        XCTAssertEqual(connection.draftText, "")
        XCTAssertFalse(connection.draftInsertionPending)
        XCTAssertFalse(connection.insertDraft())
        XCTAssertEqual(operations, [.keystrokes(Data("echo café 日本語 🙂".utf8))])
    }

    @MainActor func testDraftBracketedPasteIsOneOperationAndNormalizesCRLF() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.terminal.feed(text: "\u{1b}[?2004h")
        connection.draftText = "say\tこんにちは\r\nnext\n"
        XCTAssertNil(connection.draftValidationMessage)
        XCTAssertTrue(connection.insertDraft())
        XCTAssertEqual(connection.draftText, "say\tこんにちは\r\nnext\n")
        let operations = await collectedInput(channel, handoffTo: connection)
        XCTAssertEqual(connection.draftText, "")
        XCTAssertEqual(operations, [.keystrokes(Data("\u{1b}[200~say\tこんにちは\nnext\n\u{1b}[201~".utf8))])
    }

    @MainActor func testDraftRejectsUnbracketedTabsAndNewlinesWithoutChangingText() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        for text in ["echo one\necho two", "echo one\r\necho two", "echo\tfile", "\n", "\t"] {
            connection.draftText = text
            XCTAssertNotNil(connection.draftValidationMessage, text.debugDescription)
            XCTAssertFalse(connection.insertDraft(), text.debugDescription)
            XCTAssertEqual(connection.draftText, text)
        }
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [])
    }

    @MainActor func testDraftReadsPasteModeAgainAtInsertion() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.draftText = "first\nsecond"
        XCTAssertNotNil(connection.draftValidationMessage)
        connection.terminal.feed(text: "\u{1b}[?2004h")
        XCTAssertNil(connection.draftValidationMessage)
        connection.terminal.feed(text: "\u{1b}[?2004l")
        XCTAssertFalse(connection.insertDraft())
        XCTAssertEqual(connection.draftText, "first\nsecond")
        connection.terminal.feed(text: "\u{1b}[?2004h")
        XCTAssertTrue(connection.insertDraft())
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [.keystrokes(Data("\u{1b}[200~first\nsecond\u{1b}[201~".utf8))])
    }

    @MainActor func testDraftRejectsAllC0DELAndC1ControlsEvenWhenBracketed() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        let controls = Array(0...0x1f).filter { $0 != 0x09 && $0 != 0x0a } + Array(0x7f...0x9f)
        let payloads = controls.map { "before" + String(UnicodeScalar($0)!) + "after" }
            + ["\u{1b}[201~echo injected", "\u{1b}]52;c;payload\u{07}", "\r", "one\r\ntwo\r"]
        for mode in ["\u{1b}[?2004l", "\u{1b}[?2004h"] {
            connection.terminal.feed(text: mode)
            for text in payloads {
                connection.draftText = text
                XCTAssertNotNil(connection.draftValidationMessage, text.debugDescription)
                XCTAssertFalse(connection.insertDraft(), text.debugDescription)
                XCTAssertEqual(connection.draftText, text)
            }
        }
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [])
    }

    @MainActor func testDraftEnforcesOriginalUTF8ByteLimitAndPreservesRejectedDrafts() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.terminal.feed(text: "\u{1b}[?2004h")
        for text in ["", String(repeating: "a", count: 65_537), String(repeating: "🙂", count: 16_385),
                     String(repeating: "\r\n", count: 32_769)] {
            connection.draftText = text
            XCTAssertNotNil(connection.draftValidationMessage)
            XCTAssertFalse(connection.insertDraft())
            XCTAssertEqual(connection.draftText, text)
        }
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [])
    }

    @MainActor func testDraftAcceptsExactly64KiBWithFramingOutsideLimit() async {
        for bracketed in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            let text = bracketed ? String(repeating: "🙂", count: 16_384) : String(repeating: "a", count: 65_536)
            if bracketed { connection.terminal.feed(text: "\u{1b}[?2004h") }
            connection.draftText = text
            XCTAssertNil(connection.draftValidationMessage)
            XCTAssertTrue(connection.insertDraft())
            XCTAssertEqual(connection.draftText, text)
            let operations = await collectedInput(channel, handoffTo: connection)
            XCTAssertEqual(connection.draftText, "")
            let expected = bracketed ? "\u{1b}[200~" + text + "\u{1b}[201~" : text
            XCTAssertEqual(operations, [.keystrokes(Data(expected.utf8))])
        }
    }

    @MainActor func testDraftRefusesHeldControlWithoutConsumingModifier() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.controlHeld = true
        connection.draftText = "c"
        for mode in ["\u{1b}[?2004l", "\u{1b}[?2004h"] {
            connection.terminal.feed(text: mode)
            XCTAssertNotNil(connection.draftValidationMessage)
            XCTAssertFalse(connection.insertDraft())
            XCTAssertEqual(connection.draftText, "c")
            XCTAssertTrue(connection.controlHeld)
        }
        connection.controlHeld = false
        XCTAssertNil(connection.draftValidationMessage)
        XCTAssertTrue(connection.insertDraft())
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [.keystrokes(Data("\u{1b}[200~c\u{1b}[201~".utf8))])
    }

    @MainActor func testDraftRequiresConnectedPhaseAndInputContinuation() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.draftText = "keep this"
        for phase: TerminalConnection.Phase in [.disconnected, .connecting, .suspended, .failed("offline")] {
            connection.phase = phase
            XCTAssertFalse(connection.insertDraft())
            XCTAssertEqual(connection.draftText, "keep this")
        }
        let noInput = TerminalConnection()
        noInput.phase = .connected
        noInput.draftText = "no continuation"
        XCTAssertFalse(noInput.insertDraft())
        XCTAssertEqual(noInput.draftText, "no continuation")
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [])
    }

    @MainActor func testDraftPreservesTextWhenContinuationIsTerminatedOrDropsInput() async {
        let terminated = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        terminated.continuation.finish()
        let closed = TerminalConnection(inputContinuation: terminated.continuation)
        closed.phase = .connected
        closed.draftText = "closed stream"
        XCTAssertFalse(closed.insertDraft())
        XCTAssertEqual(closed.draftText, "closed stream")
        XCTAssertFalse(closed.draftInsertionPending)
        let dropped = AsyncStream<TerminalConnection.InputEnvelope>.makeStream(bufferingPolicy: .bufferingOldest(0))
        let full = TerminalConnection(inputContinuation: dropped.continuation)
        full.phase = .connected
        full.draftText = "full stream"
        XCTAssertFalse(full.insertDraft())
        XCTAssertEqual(full.draftText, "full stream")
        XCTAssertFalse(full.draftInsertionPending)
        let closedOperations = await collectedInput(terminated)
        let droppedOperations = await collectedInput(dropped)
        XCTAssertEqual(closedOperations, [])
        XCTAssertEqual(droppedOperations, [])
    }

    @MainActor func testDraftSurvivesBackgroundAndDisconnectWithoutEmittingInput() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.draftText = "keep across lifecycle changes"
        await connection.background()
        XCTAssertEqual(connection.phase, .suspended)
        XCTAssertFalse(connection.insertDraft())
        XCTAssertEqual(connection.draftText, "keep across lifecycle changes")
        await connection.disconnect()
        XCTAssertEqual(connection.phase, .disconnected)
        XCTAssertFalse(connection.insertDraft())
        XCTAssertEqual(connection.draftText, "keep across lifecycle changes")
        // Even a phase change cannot revive the continuation closed by stopIO().
        connection.phase = .connected
        XCTAssertFalse(connection.insertDraft())
        XCTAssertEqual(connection.draftText, "keep across lifecycle changes")
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, [])
    }

    @MainActor func testDraftsAndInputStayWithTheirOwningConnection() async {
        let firstChannel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let secondChannel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let first = TerminalConnection(inputContinuation: firstChannel.continuation)
        let second = TerminalConnection(inputContinuation: secondChannel.continuation)
        first.phase = .connected
        second.phase = .connected
        first.draftText = "first terminal"
        second.draftText = "second terminal"
        XCTAssertTrue(first.insertDraft())
        XCTAssertEqual(first.draftText, "first terminal")
        XCTAssertEqual(second.draftText, "second terminal")
        await first.disconnect()
        XCTAssertEqual(first.draftText, "first terminal")
        XCTAssertFalse(first.draftInsertionPending)
        XCTAssertEqual(second.phase, .connected)
        XCTAssertEqual(second.draftText, "second terminal")
        XCTAssertTrue(second.insertDraft())
        let firstOperations = await collectedInput(firstChannel)
        let secondOperations = await collectedInput(secondChannel, handoffTo: second)
        XCTAssertEqual(second.draftText, "")
        XCTAssertEqual(first.draftText, "first terminal")
        XCTAssertEqual(firstOperations, [.keystrokes(Data("first terminal".utf8))])
        XCTAssertEqual(secondOperations, [.keystrokes(Data("second terminal".utf8))])
    }

    @MainActor func testInsertedDraftSurvivesCancellationBeforeHandoffWithoutResending() async throws {
        for background in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            connection.draftText = "retain this draft"
            XCTAssertTrue(connection.insertDraft())
            if background { await connection.background() }
            else { await connection.disconnect() }
            XCTAssertEqual(connection.draftText, "retain this draft")
            XCTAssertFalse(connection.draftInsertionPending)
            var handoffs = 0
            for await envelope in channel.stream {
                try await connection.handoff(envelope) { _ in handoffs += 1 }
            }
            await connection.foreground()
            connection.phase = .connected
            XCTAssertFalse(connection.insertDraft(), "A closed stream cannot be revived by a phase change")
            XCTAssertEqual(handoffs, 0)
            XCTAssertEqual(connection.draftText, "retain this draft")
        }
    }

    @MainActor func testFailedHandoffRetainsDraftAndRequiresExplicitRetry() async throws {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.draftText = "retain after failure"
        XCTAssertTrue(connection.insertDraft())
        var iterator = channel.stream.makeAsyncIterator()
        let next = await iterator.next()
        let envelope = try XCTUnwrap(next)
        var handoffs = 0
        do {
            try await connection.handoff(envelope) { _ in
                handoffs += 1
                throw TrillError.message("Fixture enqueue failure")
            }
            XCTFail("Expected enqueue failure")
        } catch { XCTAssertEqual(error.localizedDescription, "Fixture enqueue failure") }
        XCTAssertEqual(connection.draftText, "retain after failure")
        XCTAssertFalse(connection.draftInsertionPending)
        XCTAssertNil(connection.draftValidationMessage)
        // A stale envelope cannot retry itself or clear a subsequent insertion.
        try await connection.handoff(envelope) { _ in handoffs += 1 }
        XCTAssertEqual(handoffs, 1)
        XCTAssertTrue(connection.insertDraft())
        XCTAssertFalse(connection.insertDraft())
        try await connection.handoff(envelope) { _ in handoffs += 1 }
        XCTAssertTrue(connection.draftInsertionPending)
        XCTAssertEqual(handoffs, 1)
        let nextRetry = await iterator.next()
        let retry = try XCTUnwrap(nextRetry)
        try await connection.handoff(retry) { _ in handoffs += 1 }
        XCTAssertEqual(handoffs, 2)
        XCTAssertEqual(connection.draftText, "")
        XCTAssertFalse(connection.draftInsertionPending)
        channel.continuation.finish()
    }

    @MainActor func testHandoffNeverClearsInterveningEditsEvenWhenTextIsRestored() async throws {
        for restoreOriginal in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            connection.draftText = "original"
            XCTAssertTrue(connection.insertDraft())
            var iterator = channel.stream.makeAsyncIterator()
            let next = await iterator.next()
            let envelope = try XCTUnwrap(next)
            try await connection.handoff(envelope) { operation in
                XCTAssertEqual(operation, .keystrokes(Data("original".utf8)))
                connection.draftText = "edited while pending"
                if restoreOriginal { connection.draftText = "original" }
                XCTAssertFalse(connection.insertDraft())
                XCTAssertNotNil(connection.draftValidationMessage)
            }
            XCTAssertEqual(connection.draftText, restoreOriginal ? "original" : "edited while pending")
            XCTAssertFalse(connection.draftInsertionPending)
            XCTAssertNil(connection.draftValidationMessage)
            channel.continuation.finish()
        }
    }

    @MainActor func testFailedHandoffAndCancellationPreserveInterveningEdits() async throws {
        for cancel in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            connection.draftText = "original"
            XCTAssertTrue(connection.insertDraft())
            var iterator = channel.stream.makeAsyncIterator()
            let next = await iterator.next()
            let envelope = try XCTUnwrap(next)
            do {
                try await connection.handoff(envelope) { _ in
                    connection.draftText = "keep my edits"
                    if cancel { await connection.background() }
                    else { throw TrillError.message("Fixture enqueue failure") }
                }
                XCTAssertTrue(cancel, "Expected enqueue failure")
            } catch { XCTAssertFalse(cancel) }
            XCTAssertEqual(connection.draftText, "keep my edits")
            XCTAssertFalse(connection.draftInsertionPending)
            var retries = 0
            try await connection.handoff(envelope) { _ in retries += 1 }
            XCTAssertEqual(retries, 0)
            channel.continuation.finish()
        }
    }

    @MainActor func testOtherInputHandoffCannotCompletePendingDraft() async throws {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.key("x")
        connection.draftText = "pending draft"
        XCTAssertTrue(connection.insertDraft())
        var iterator = channel.stream.makeAsyncIterator()
        let nextKey = await iterator.next()
        let key = try XCTUnwrap(nextKey)
        try await connection.handoff(key) { operation in
            XCTAssertEqual(operation, .keystrokes(Data("x".utf8)))
        }
        XCTAssertTrue(connection.draftInsertionPending)
        XCTAssertEqual(connection.draftText, "pending draft")
        let nextDraft = await iterator.next()
        let draft = try XCTUnwrap(nextDraft)
        try await connection.handoff(draft) { _ in }
        XCTAssertFalse(connection.draftInsertionPending)
        XCTAssertEqual(connection.draftText, "")
        channel.continuation.finish()
    }

    @MainActor func testShiftReturnIsCSIuWithoutCarriageReturnAndAppliesOnlyOnce() async {
        for useDelegate in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
        connection.terminal.feed(text: "\u{1b}[>1u")
            connection.shiftHeld = true
            if useDelegate { connection.send(source: connection.terminal, data: [UInt8(13)][...]) }
            else { connection.key("\r") }
            XCTAssertFalse(connection.shiftHeld)
            XCTAssertFalse(connection.controlHeld)
            connection.key("\r")
            let operations = await collectedInput(channel)
            XCTAssertEqual(operations, [.keystrokes(Data("\u{1b}[13;2u".utf8)), .keystrokes(Data([13]))])
            if case .keystrokes(let modifiedReturn)? = operations.first {
                XCTAssertFalse(modifiedReturn.contains(13), "Shift+Return must never submit a bare Return")
            }
        }
    }

    @MainActor func testEmulatorCursorReportPreservesShiftForFollowingReturn() async throws {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.terminal.feed(text: "\u{1b}[>1u")
        var operations: [MoshClientOp] = []
        let collector = Task { @MainActor in
            for await envelope in channel.stream { operations.append(envelope.operation) }
        }
        defer { channel.continuation.finish(); collector.cancel() }

        connection.shiftHeld = true
        // Feed the real emulator, including an explicit cursor position. Its terminal-core
        // reply is dispatched asynchronously and must bypass user modifier handling.
        connection.terminal.feed(text: "\u{1b}[4;7H\u{1b}[6n")
        try await eventually("Emulator cursor report delivered through the asynchronous callback") {
            !operations.isEmpty
        }
        XCTAssertEqual(operations, [.keystrokes(Data("\u{1b}[4;7R".utf8))])
        XCTAssertTrue(connection.shiftHeld, "A host query must not consume the user's Shift modifier")

        connection.key("\r")
        try await eventually("Shift+Return delivered after host reply") { operations.count >= 2 }
        XCTAssertEqual(operations, [.keystrokes(Data("\u{1b}[4;7R".utf8)),
                                   .keystrokes(Data("\u{1b}[13;2u".utf8))])
        XCTAssertFalse(connection.shiftHeld)
        for operation in operations {
            if case .keystrokes(let bytes) = operation {
                XCTAssertFalse(bytes.contains(13), "The host query must not turn Shift+Return into submit")
            }
        }
        channel.continuation.finish()
        await collector.value
    }

    @MainActor func testShiftTabAndModifiedArrowsUseExactSequencesInBothCursorModes() async {
        let cases: [(input: String, control: Bool, expected: String)] = [
            ("\t", false, "\u{1b}[Z"),
            ("\u{1b}[A", false, "\u{1b}[1;2A"),
            ("\u{1b}OA", false, "\u{1b}[1;2A"),
            ("\u{1b}[A", true, "\u{1b}[1;6A"),
            ("\u{1b}OA", true, "\u{1b}[1;6A"),
            ("\u{1b}[B", false, "\u{1b}[1;2B"),
            ("\u{1b}[C", false, "\u{1b}[1;2C"),
            ("\u{1b}[D", false, "\u{1b}[1;2D")
        ]
        for applicationCursor in [false, true] {
            for useDelegate in [false, true] {
                let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
                let connection = TerminalConnection(inputContinuation: channel.continuation)
                connection.phase = .connected
                connection.terminal.feed(text: applicationCursor ? "\u{1b}[?1h" : "\u{1b}[?1l")
                for item in cases {
                    connection.shiftHeld = true
                    connection.controlHeld = item.control
                    if useDelegate { connection.send(source: connection.terminal, data: Array(item.input.utf8)[...]) }
                    else { connection.key(item.input) }
                    XCTAssertFalse(connection.shiftHeld, item.expected)
                    XCTAssertFalse(connection.controlHeld, item.expected)
                }
                connection.key("a")
                let operations = await collectedInput(channel)
                XCTAssertEqual(operations, cases.map { .keystrokes(Data($0.expected.utf8)) }
                               + [.keystrokes(Data("a".utf8))])
            }
        }
    }

    @MainActor func testUnmodifiedArrowsRespectDefaultAndApplicationCursorModes() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        XCTAssertEqual(connection.terminal.terminalInputStateSnapshot()?.applicationCursor, false)
        var expected: [MoshClientOp] = []
        for applicationCursor in [false, true, false] {
            connection.terminal.feed(text: applicationCursor ? "\u{1b}[?1h" : "\u{1b}[?1l")
            for final in ["A", "B", "C", "D"] {
                // Accessory keys supply CSI; SwiftTerm may supply SS3 in application mode.
                connection.key("\u{1b}[\(final)")
                connection.send(source: connection.terminal, data: Array("\u{1b}O\(final)".utf8)[...])
                let sequence = (applicationCursor ? "\u{1b}O" : "\u{1b}[") + final
                expected += Array(repeating: .keystrokes(Data(sequence.utf8)), count: 2)
            }
        }
        connection.key("\t")
        connection.key("\r")
        expected += [.keystrokes(Data([9])), .keystrokes(Data([13]))]
        let operations = await collectedInput(channel)
        XCTAssertEqual(operations, expected)
    }

    @MainActor func testOneShotShiftUppercasesLettersAndMapsSymbolsThroughBothInputPaths() async {
        let cases = [("a", "A"), ("z", "Z"), ("1", "!"), ("0", ")"),
                     ("-", "_"), ("[", "{"), ("/", "?"), ("é", "É")]
        for useDelegate in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            var expected: [MoshClientOp] = []
            for (input, shifted) in cases {
                connection.shiftHeld = true
                if useDelegate { connection.send(source: connection.terminal, data: Array(input.utf8)[...]) }
                else { connection.key(input) }
                XCTAssertFalse(connection.shiftHeld)
                connection.key(input)
                expected += [.keystrokes(Data(shifted.utf8)), .keystrokes(Data(input.utf8))]
            }
            let operations = await collectedInput(channel)
            XCTAssertEqual(operations, expected)
        }
    }

    @MainActor func testDraftRejectsShiftWithoutConsumingModifierOrChangingText() async {
        for bracketed in [false, true] {
            for control in [false, true] {
                let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
                let connection = TerminalConnection(inputContinuation: channel.continuation)
                connection.phase = .connected
                connection.terminal.feed(text: bracketed ? "\u{1b}[?2004h" : "\u{1b}[?2004l")
                connection.draftText = "keep café 1"
                connection.shiftHeld = true
                connection.controlHeld = control
                XCTAssertNotNil(connection.draftValidationMessage)
                XCTAssertFalse(connection.insertDraft())
                XCTAssertEqual(connection.draftText, "keep café 1")
                XCTAssertTrue(connection.shiftHeld)
                XCTAssertEqual(connection.controlHeld, control)
                XCTAssertFalse(connection.draftInsertionPending)
                let operations = await collectedInput(channel)
                XCTAssertEqual(operations, [])
            }
        }
    }

    @MainActor func testBackgroundAndDisconnectClearBothModifiersWithoutSendingInput() async {
        for background in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            connection.controlHeld = true
            connection.shiftHeld = true
            if background { await connection.background() }
            else { await connection.disconnect() }
            XCTAssertFalse(connection.controlHeld)
            XCTAssertFalse(connection.shiftHeld)
            let operations = await collectedInput(channel)
            XCTAssertEqual(operations, [])
        }
    }

    @MainActor private func eventually(
        _ message: String, file: StaticString = #filePath, line: UInt = #line,
        _ condition: () async -> Bool
    ) async throws {
        for _ in 0..<200 {
            if await condition() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTFail(message, file: file, line: line)
        throw TerminalLifecycleTestError.timedOut
    }

    @MainActor private func lifecycleEnvironment(
        _ fixture: TerminalLifecycleFixture, transports: [TerminalLifecycleTransport],
        sleeper: TerminalRetrySleeper, responsePollLimit: Int = 1_000, returnsControlWhenHidden: Bool = false
    ) -> TerminalConnection.Environment {
        TerminalConnection.Environment(
            attach: { try await fixture.attach($0, cols: $1, rows: $2) },
            release: { try await fixture.release($0) },
            makeTransport: { lease in
                let index = (Int(lease.leaseId.dropFirst("lease-".count)) ?? 0) - 1
                // Unexpected duplicate attaches must fail assertions, not crash the fixture.
                return transports.indices.contains(index) ? transports[index] : TerminalLifecycleTransport()
            },
            retrySleep: { try await sleeper.sleep($0) },
            monitorInterval: .milliseconds(10), responsePollLimit: responsePollLimit, resumePollLimit: 10,
            returnsControlWhenHidden: returnsControlWhenHidden)
    }

    @MainActor func testForegroundReplacesUnresponsivePeerEvenWhenOldTransportReportsRunning() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport(remainsRunningInBackground: true)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "original-session", environment:
            lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
        try await eventually("Initial peer response") { connection.connected }

        await connection.background()
        try await eventually("Old transport was paused") { await old.pauseCount == 1 }
        let oldState = await old.state
        XCTAssertEqual(oldState, .running)
        await connection.foreground()
        try await eventually("Fresh foreground connection") { connection.connected }

        let requests = await fixture.requests
        let released = await fixture.released
        let oldStops = await old.stopCount
        let freshStarts = await fresh.startCount
        XCTAssertEqual(requests.map(\.sessionID), ["original-session", "original-session"])
        XCTAssertEqual(released, ["lease-1"])
        XCTAssertEqual(oldStops, 1)
        XCTAssertEqual(freshStarts, 1)
    }

    @MainActor func testForegroundResumesIdlePeerWithoutNewLeaseOrTerminalReset() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let transport = TerminalLifecycleTransport(respondsOnResume: true)
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "warm", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        connection.terminal.feed(text: "\u{1b}[?2004h")
        connection.draftText = "Unsent text"
        for _ in 0..<3 {
            await connection.background()
            await connection.foreground()
            await connection.foreground()
            try await eventually("Existing peer resumed on ACK without new output") { connection.connected }
        }
        let requests = await fixture.requests.count
        let releases = await fixture.released
        let resumes = await transport.resumeCount
        let stops = await transport.stopCount
        let keys = await transport.keystrokes
        XCTAssertEqual(requests, 1)
        XCTAssertEqual(releases, [])
        XCTAssertEqual(resumes, 3)
        XCTAssertEqual(stops, 0)
        XCTAssertEqual(keys, [])
        XCTAssertEqual(connection.draftText, "Unsent text")
        XCTAssertEqual(connection.terminal.terminalInputStateSnapshot()?.bracketedPasteMode, true)
        XCTAssertNil(connection.recoveryMessage)
    }

    @MainActor func testResumeDoesNotTrustStaleActivityOrAcceptInputBeforeNewAcknowledgement() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let transport = TerminalLifecycleTransport(resumeGate: gate)
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "warm", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await transport.acknowledge() // A response from before backgrounding isn't enough.
        await connection.background()
        await connection.foreground()
        try await eventually("Resume is pending") { await gate.arrivals == 1 }
        XCTAssertEqual(connection.phase, .resuming)
        connection.draftText = "Keep local"
        XCTAssertFalse(connection.insertDraft())
        connection.key("must not send")
        await gate.release()
        await transport.acknowledge()
        try await eventually("New response confirms resume") { connection.connected }
        let requests = await fixture.requests.count
        let keys = await transport.keystrokes
        XCTAssertEqual(requests, 1)
        XCTAssertEqual(keys, [])
        XCTAssertEqual(connection.draftText, "Keep local")
    }

    @MainActor func testBackgroundDuringResumeKeepsInputPausedUntilNextForeground() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let transport = TerminalLifecycleTransport(respondsOnResume: true, resumeGate: gate)
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "warm", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await connection.background()
        await connection.foreground()
        try await eventually("Resume pending") { await gate.arrivals == 1 }
        await connection.background()
        try await eventually("Pending resume aborted on background") { await transport.state == .stopped }
        XCTAssertEqual(connection.phase, .suspended)
        connection.key("ignored while locked")
        await connection.foreground()
        try await eventually("Next foreground resumes") { connection.connected }
        let requests = await fixture.requests.count
        let keys = await transport.keystrokes
        XCTAssertEqual(requests, 2)
        XCTAssertEqual(keys, [])
    }

    @MainActor func testDisconnectDuringResumeCannotReviveOrReattach() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let transport = TerminalLifecycleTransport(respondsOnResume: true, resumeGate: gate)
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "warm", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await connection.background()
        await connection.foreground()
        try await eventually("Resume is pending") { await gate.arrivals == 1 }
        let closing = Task { await connection.disconnect() }
        try await eventually("Disconnect recorded") { connection.phase == .disconnected }
        await closing.value
        await connection.foreground()
        let requests = await fixture.requests.count
        let releases = await fixture.released
        XCTAssertEqual(requests, 1)
        XCTAssertEqual(releases, ["lease-1"])
        XCTAssertEqual(connection.phase, .disconnected)
    }

    @MainActor func testStalledResumeStartupAbortsAndFreshAttaches() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let old = TerminalLifecycleTransport(resumeGate: gate)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        var environment = lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper)
        environment.resumeStartupTimeout = .milliseconds(40)
        await connection.connect(sessionID: "stalled", environment: environment)
        try await eventually("Initial connection") { connection.connected }
        await connection.background()
        await connection.foreground()
        try await eventually("Pending startup is aborted and replaced") { await fixture.requests.count == 2 && connection.connected }
        let resumes = await old.resumeCount
        let released = await fixture.released
        XCTAssertEqual(resumes, 1)
        XCTAssertEqual(released, ["lease-1"])
    }

    @MainActor func testTimeoutInvalidatesCompletedResumeBeforeItCanRestartStoppedClient() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        // Model successful resume whose caller only resumes after watchdog stop.
        let old = TerminalLifecycleTransport(resumeGate: gate, returnsCompletedResumeAfterStop: true)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        var environment = lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper)
        environment.resumeStartupTimeout = .milliseconds(40)
        await connection.connect(sessionID: "expired", environment: environment)
        try await eventually("Initial connection") { connection.connected }
        await connection.background()
        await connection.foreground()
        try await eventually("Expired resume replaced") { await fixture.requests.count == 2 && connection.connected }
        let starts = await old.startCount
        XCTAssertEqual(starts, 1, "activate must never restart a completed resume after timeout")
    }

    @MainActor func testDisconnectDuringResumeBaselineReadDoesNotStartUDP() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let transport = TerminalLifecycleTransport(respondsOnResume: true)
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "baseline", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await transport.blockResponses(on: gate)
        await connection.background()
        await connection.foreground()
        try await eventually("Baseline read pending") { await gate.arrivals == 1 }
        let closing = Task { await connection.disconnect() }
        try await eventually("Disconnect intent") { connection.phase == .disconnected }
        await gate.release()
        await closing.value
        let resumes = await transport.resumeCount
        XCTAssertEqual(resumes, 0)
    }

    @MainActor func testOutputDrainedAtBackgroundBoundaryIsAppliedBeforeResumedOutput() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let transport = TerminalLifecycleTransport(respondsOnResume: true)
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "drained", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await transport.blockNextOutput(on: gate, text: "\u{1b}[?2004h")
        try await eventually("Reader owns output batch") { await gate.arrivals == 1 }
        await connection.background()
        await connection.foreground()
        await gate.release()
        try await eventually("Resumed with pending output applied") { connection.connected }
        XCTAssertEqual(connection.terminal.terminalInputStateSnapshot()?.bracketedPasteMode, true)
        let requests = await fixture.requests.count
        XCTAssertEqual(requests, 1)
        // A subsequent ordered delta must still apply after that retained batch.
        await transport.blockNextOutput(on: gate, text: "\u{1b}[?2004l")
        try await eventually("Subsequent parser change applied") { connection.terminal.terminalInputStateSnapshot()?.bracketedPasteMode == false }
    }

    @MainActor func testForegroundDoesNotAutoOpenAndExplicitDisconnectClearsConnectionIntent() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let transport = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.foreground()
        await connection.background()
        await connection.foreground()
        XCTAssertEqual(connection.phase, .disconnected)
        XCTAssertFalse(connection.isOpen)

        await connection.connect(sessionID: "chosen-session", environment:
            lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Explicitly opened connection") { connection.connected }
        await connection.disconnect()
        await connection.background()
        await connection.foreground()
        await connection.foreground()
        let requests = await fixture.requests
        let released = await fixture.released
        XCTAssertEqual(requests.map(\.sessionID), ["chosen-session"])
        XCTAssertEqual(released, ["lease-1"])
        XCTAssertEqual(connection.phase, .disconnected)
    }

    @MainActor func testTransientAttachFailuresRetryAutomatically() async throws {
        for failure in [TerminalAttachOutcome.transient, .http(408), .http(429), .http(500), .http(503)] {
            let fixture = TerminalLifecycleFixture(outcomes: [failure, .success])
            let sleeper = TerminalRetrySleeper()
            let transport = TerminalLifecycleTransport()
            let connection = TerminalConnection()
            addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
            await connection.connect(sessionID: "retry-session", environment:
                lifecycleEnvironment(fixture, transports: [transport, transport], sleeper: sleeper))
            try await eventually("Retry scheduled for \(failure)") { await sleeper.delays.count == 1 }
            XCTAssertEqual(connection.phase, .reconnecting)
            let delays = await sleeper.delays
            XCTAssertEqual(delays, [.seconds(1)])
            await sleeper.resume(0)
            try await eventually("Retry connected for \(failure)") { connection.connected }
            let requests = await fixture.requests
            XCTAssertEqual(requests.map(\.sessionID), ["retry-session", "retry-session"])
            await connection.disconnect()
        }
    }

    @MainActor func testRepeatedTransientFailuresBackOffAndCapDelay() async throws {
        let fixture = TerminalLifecycleFixture(outcomes: Array(repeating: .transient, count: 7) + [.success])
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "backoff-session", environment:
            lifecycleEnvironment(fixture, transports: [], sleeper: sleeper))
        let expected: [Duration] = [1, 2, 4, 8, 15, 30, 30].map { .seconds($0) }
        for index in expected.indices {
            try await eventually("Backoff step \(index)") { await sleeper.delays.count == index + 1 }
            let delays = await sleeper.delays
            let requestCount = await fixture.requests.count
            XCTAssertEqual(delays, Array(expected.prefix(index + 1)))
            XCTAssertEqual(requestCount, index + 1)
            await sleeper.resume(index)
        }
        try await eventually("Eventually recovered") { connection.connected }
        let requestCount = await fixture.requests.count
        XCTAssertEqual(requestCount, 8)
    }

    @MainActor func testPermanentAttachErrorsDoNotRetryOrReviveOnForeground() async throws {
        for status in [401, 403, 404, 409] {
            let fixture = TerminalLifecycleFixture(outcomes: [.http(status), .success])
            let sleeper = TerminalRetrySleeper()
            let connection = TerminalConnection()
            addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
            let environment = lifecycleEnvironment(fixture, transports: [], sleeper: sleeper)
            await connection.connect(sessionID: "permanent-session", environment: environment)
            guard case .failed = connection.phase else {
                XCTFail("HTTP \(status) must require user action")
                continue
            }
            XCTAssertFalse(connection.canRetryRecovery)
            XCTAssertEqual(connection.recoveryMessage, "Fixture HTTP \(status)")
            await connection.retryRecoveryNow()
            await connection.background()
            await connection.foreground()
            let requests = await fixture.requests
            let delays = await sleeper.delays
            XCTAssertEqual(requests.count, 1, "HTTP \(status)")
            XCTAssertEqual(delays, [], "HTTP \(status)")
            XCTAssertFalse(connection.isOpen)
            // A fresh explicit request remains allowed after the user fixes the cause.
            await connection.connect(sessionID: "permanent-session", environment: environment)
            try await eventually("Explicit recovery after HTTP \(status)") { connection.connected }
            await connection.disconnect()
        }
    }

    @MainActor func testDisconnectCancelsPendingRetryEvenIfSleepCompletesLate() async throws {
        let fixture = TerminalLifecycleFixture(outcomes: [.transient, .success])
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "cancel-session", environment:
            lifecycleEnvironment(fixture, transports: [], sleeper: sleeper))
        try await eventually("Retry sleeping") { await sleeper.delays.count == 1 }
        await connection.disconnect()
        await sleeper.resume(0)
        try await eventually("Canceled retry observed cancellation") { await sleeper.canceled == [0] }
        await connection.background()
        await connection.foreground()
        let requestCount = await fixture.requests.count
        XCTAssertEqual(requestCount, 1)
        XCTAssertEqual(connection.phase, .disconnected)
    }

    @MainActor func testBackgroundCancelsRetryAndForegroundRecoversWithoutWaitingForBackoff() async throws {
        let fixture = TerminalLifecycleFixture(outcomes: [.transient, .success])
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "background-session", environment:
            lifecycleEnvironment(fixture, transports: [], sleeper: sleeper))
        try await eventually("Retry sleeping") { await sleeper.delays.count == 1 }
        await connection.background()
        XCTAssertEqual(connection.phase, .suspended)
        let backgroundRequests = await fixture.requests.count
        XCTAssertEqual(backgroundRequests, 1)
        // Leave the canceled sleeper blocked: foreground must recover independently.
        await connection.foreground()
        try await eventually("Immediate foreground recovery") { connection.connected }
        await sleeper.resume(0)
        try await eventually("Old retry canceled") { await sleeper.canceled == [0] }
        let requestCount = await fixture.requests.count
        let delays = await sleeper.delays
        XCTAssertEqual(requestCount, 2)
        XCTAssertEqual(delays, [.seconds(1)])
    }

    @MainActor func testLateAttachAfterDisconnectReleasesLeaseWithoutStartingTransport() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(attachGate: gate, gatedAttempt: 1)
        let sleeper = TerminalRetrySleeper()
        let transport = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        let environment = lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper)
        let opening = Task { await connection.connect(sessionID: "late-session", environment: environment) }
        try await eventually("Attach blocked") { await gate.arrivals == 1 }
        let closing = Task { await connection.disconnect() }
        try await eventually("Disconnect intent recorded before attach returns") { connection.phase == .disconnected }
        await gate.release()
        await opening.value
        await closing.value
        let released = await fixture.released
        let starts = await transport.startCount
        let delays = await sleeper.delays
        XCTAssertEqual(released, ["lease-1"])
        XCTAssertEqual(starts, 0)
        XCTAssertEqual(delays, [])
        XCTAssertEqual(connection.phase, .disconnected)
    }

    @MainActor func testDuplicateForegroundDoesNotDuplicatePendingOrCompletedAttach() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(attachGate: gate, gatedAttempt: 2)
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "duplicate-session", environment:
            lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await connection.background()
        await connection.foreground()
        try await eventually("Foreground attach blocked") { await gate.arrivals == 1 }
        await connection.foreground()
        await connection.foreground()
        await gate.release()
        try await eventually("Foreground connected") { connection.connected }
        await connection.foreground()
        let requestCount = await fixture.requests.count
        let starts = await fresh.startCount
        XCTAssertEqual(requestCount, 2)
        XCTAssertEqual(starts, 1)
    }

    @MainActor func testInitialResponseTimeoutRetriesTransportThatRemainsRunning() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let quiet = TerminalLifecycleTransport(responds: false)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "quiet-session", environment:
            lifecycleEnvironment(fixture, transports: [quiet, fresh], sleeper: sleeper, responsePollLimit: 3))
        XCTAssertFalse(connection.connected)
        try await eventually("Silent running transport timed out") { await sleeper.delays.count == 1 }
        let statesAtStop = await quiet.statesAtStop
        let released = await fixture.released
        XCTAssertEqual(statesAtStop, [.running])
        XCTAssertEqual(released, ["lease-1"])
        XCTAssertEqual(connection.phase, .reconnecting)
        await sleeper.resume(0)
        try await eventually("Replacement responded") { connection.connected }
    }

    @MainActor func testFailedAndStoppedTransportsAutomaticallyRetrySameTarget() async throws {
        for state in [MoshSessionState.failed(.transportFailure("fixture")), .stopped] {
            let fixture = TerminalLifecycleFixture()
            let sleeper = TerminalRetrySleeper()
            let old = TerminalLifecycleTransport()
            let fresh = TerminalLifecycleTransport()
            let connection = TerminalConnection()
            addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
            await connection.connect(sessionID: "failed-transport-session", environment:
                lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
            try await eventually("Initial connection") { connection.connected }
            connection.controlHeld = true
            connection.shiftHeld = true
            await old.setState(state)
            try await eventually("Transport failure scheduled retry") { await sleeper.delays.count == 1 }
            XCTAssertFalse(connection.controlHeld)
            XCTAssertFalse(connection.shiftHeld)
            await sleeper.resume(0)
            try await eventually("Fresh transport connected") { connection.connected }
            let requests = await fixture.requests
            let released = await fixture.released
            XCTAssertEqual(requests.map(\.sessionID), ["failed-transport-session", "failed-transport-session"])
            XCTAssertEqual(released, ["lease-1"])
            await connection.disconnect()
        }
    }

    @MainActor func testRetryNowSkipsOnlyBackoffAndCannotDuplicateBootstrap() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(outcomes: [.transient, .success], attachGate: gate, gatedAttempt: 2)
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "retry-now", environment:
            lifecycleEnvironment(fixture, transports: [], sleeper: sleeper))
        try await eventually("Retry wait exposed to UI") { await sleeper.delays.count == 1 }
        XCTAssertTrue(connection.canRetryRecovery)
        XCTAssertTrue(connection.recoveryMessage?.contains("Waiting 1 second") == true)
        let interruption = URLError(.notConnectedToInternet).localizedDescription
        XCTAssertTrue(connection.recoveryMessage?.contains(interruption) == true)
        connection.draftText = "keep local text"

        let manualRetry = Task { await connection.retryRecoveryNow() }
        try await eventually("Manual retry entered bootstrap") { await gate.arrivals == 1 }
        XCTAssertFalse(connection.canRetryRecovery)
        XCTAssertTrue(connection.recoveryMessage?.contains("Contacting your Mac") == true)
        XCTAssertTrue(connection.recoveryMessage?.contains(interruption) == true)
        await connection.retryRecoveryNow()
        await sleeper.resume(0)
        try await eventually("Old automatic retry canceled") { await sleeper.canceled == [0] }
        let inFlightRequests = await fixture.requests.count
        XCTAssertEqual(inFlightRequests, 2)
        await gate.release()
        await manualRetry.value
        try await eventually("Manual retry connected") { connection.connected }
        XCTAssertNil(connection.recoveryMessage)
        XCTAssertFalse(connection.canRetryRecovery)
        await connection.retryRecoveryNow()
        let requests = await fixture.requests
        XCTAssertEqual(requests.map(\.sessionID), ["retry-now", "retry-now"])
        XCTAssertEqual(connection.draftText, "keep local text")
        await connection.disconnect()
        await connection.retryRecoveryNow()
        XCTAssertNil(connection.recoveryMessage)
    }

    @MainActor func testRecoveryProgressUsesLatestFailureAndClearsOnlyAfterPeerResponse() async throws {
        let fixture = TerminalLifecycleFixture(outcomes: [.http(503), .http(500), .success])
        let sleeper = TerminalRetrySleeper()
        let quiet = TerminalLifecycleTransport(responds: false)
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "progress", environment:
            lifecycleEnvironment(fixture, transports: [quiet, quiet, quiet], sleeper: sleeper))
        try await eventually("First retry wait") { await sleeper.delays.count == 1 }
        XCTAssertTrue(connection.recoveryMessage?.contains("Fixture HTTP 503") == true)
        await sleeper.resume(0)
        try await eventually("Second retry wait") { await sleeper.delays.count == 2 }
        XCTAssertTrue(connection.recoveryMessage?.contains("Fixture HTTP 500") == true)
        XCTAssertFalse(connection.recoveryMessage?.contains("503") == true)
        XCTAssertTrue(connection.recoveryMessage?.contains("Waiting 2 seconds") == true)
        await connection.retryRecoveryNow()
        XCTAssertEqual(connection.phase, .reconnecting)
        XCTAssertFalse(connection.canRetryRecovery)
        XCTAssertTrue(connection.recoveryMessage?.contains("Waiting for your terminal to respond") == true)
        XCTAssertTrue(connection.recoveryMessage?.contains("Fixture HTTP 500") == true)
        await quiet.respond()
        try await eventually("Peer confirmed recovery") { connection.connected }
        XCTAssertNil(connection.recoveryMessage)
    }

    @MainActor func testFailedDeleteStillFreshAttachesSameSessionWithoutReplayingInput() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(attachGate: gate, gatedAttempt: 2, releaseOutcomes: [.transient])
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "failed-delete", environment:
            lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        connection.draftText = "retain this draft"
        await connection.background()
        await connection.foreground()
        try await eventually("Fresh POST follows failed DELETE") { await gate.arrivals == 1 }
        XCTAssertNotNil(connection.cleanupWarning)
        XCTAssertFalse(connection.canRetryRecovery)
        let beforeAttach = await fixture.activeLeases
        XCTAssertEqual(beforeAttach, ["failed-delete": "lease-1"])
        await gate.release()
        try await eventually("Same-device recovery connected") { connection.connected }
        let requests = await fixture.requests
        let replaced = await fixture.replaced
        let released = await fixture.released
        let active = await fixture.activeLeases
        let oldStops = await old.stopCount
        let freshKeys = await fresh.keystrokes
        XCTAssertEqual(requests.map(\.sessionID), ["failed-delete", "failed-delete"])
        XCTAssertEqual(replaced, ["lease-1"])
        XCTAssertEqual(released, ["lease-1"], "Do not accumulate DELETE retries ahead of fresh attach")
        XCTAssertEqual(active, ["failed-delete": "lease-2"])
        XCTAssertEqual(oldStops, 1)
        XCTAssertEqual(freshKeys, [])
        // The client cannot infer cleanup for every provider from a fresh POST.
        XCTAssertNotNil(connection.cleanupWarning)
        XCTAssertEqual(connection.draftText, "retain this draft")
    }

    @MainActor func testRepeatedFailedDeletesNeverAccumulateBeforeFreshAttach() async throws {
        let fixture = TerminalLifecycleFixture(releaseOutcomes: Array(repeating: .transient, count: 4))
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "repeated-cleanup", environment:
            lifecycleEnvironment(fixture, transports: [], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        for attempt in 1...3 {
            await connection.background()
            await connection.foreground()
            try await eventually("Fresh attach after failed DELETE \(attempt)") { connection.connected }
            let released = await fixture.released
            let requests = await fixture.requests
            XCTAssertEqual(released, (1...attempt).map { "lease-\($0)" })
            XCTAssertEqual(requests.count, attempt + 1)
            XCTAssertTrue(requests.allSatisfy { $0.sessionID == "repeated-cleanup" })
        }
        let delays = await sleeper.delays
        XCTAssertEqual(delays, [], "Cleanup failure must not add an attach retry wait")
    }

    @MainActor func testMissingLeaseAfterBackendRestartIsAlreadyReleased() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(attachGate: gate, gatedAttempt: 2, releaseOutcomes: [.http(404)])
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "backend-restarted", environment:
            lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await old.setState(.failed(.transportFailure("backend restarted")))
        try await eventually("Restart schedules recovery after DELETE 404") { await sleeper.delays.count == 1 }
        XCTAssertNil(connection.cleanupWarning)
        let activeBeforeRetry = await fixture.activeLeases
        XCTAssertEqual(activeBeforeRetry, [:])
        let manualRetry = Task { await connection.retryRecoveryNow() }
        try await eventually("Replacement bootstrap blocked") { await gate.arrivals == 1 }
        XCTAssertNil(connection.cleanupWarning)
        await gate.release()
        await manualRetry.value
        try await eventually("Recovered after backend restart") { connection.connected }
        let requests = await fixture.requests
        XCTAssertEqual(requests.map(\.sessionID), ["backend-restarted", "backend-restarted"])
    }

    @MainActor func testLostPostResponseRecoversUnknownLeaseWithoutDeletingOrReplayingInput() async throws {
        let fixture = TerminalLifecycleFixture(outcomes: [.lostResponse, .success])
        let sleeper = TerminalRetrySleeper()
        let unused = TerminalLifecycleTransport()
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        connection.draftText = "never replay this"
        await connection.connect(sessionID: "lost-post", environment:
            lifecycleEnvironment(fixture, transports: [unused, fresh], sleeper: sleeper))
        try await eventually("Lost response schedules retry") { await sleeper.delays.count == 1 }
        let strandedLease = await fixture.activeLeases
        let initialReleases = await fixture.released
        let unusedStarts = await unused.startCount
        XCTAssertEqual(strandedLease, ["lost-post": "lease-1"])
        XCTAssertEqual(initialReleases, [])
        XCTAssertEqual(unusedStarts, 0)
        XCTAssertFalse(connection.insertDraft())
        connection.key("offline input")
        await sleeper.resume(0)
        try await eventually("Fresh lease connected") { connection.connected }
        let requests = await fixture.requests
        let replaced = await fixture.replaced
        let released = await fixture.released
        let keys = await fresh.keystrokes
        XCTAssertEqual(requests.map(\.sessionID), ["lost-post", "lost-post"])
        XCTAssertEqual(replaced, ["lease-1"])
        XCTAssertEqual(released, [])
        XCTAssertEqual(keys, [])
        XCTAssertEqual(connection.draftText, "never replay this")
        XCTAssertNil(connection.cleanupWarning)
    }

    @MainActor func testTransientAttachFailureArrivingAfterLockWaitsForForeground() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(outcomes: [.transient, .success], attachGate: gate, gatedAttempt: 1)
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        let environment = lifecycleEnvironment(fixture, transports: [], sleeper: sleeper)
        let opening = Task { await connection.connect(sessionID: "locked-request", environment: environment) }
        try await eventually("Initial bootstrap blocked") { await gate.arrivals == 1 }
        await connection.background()
        await gate.release()
        await opening.value
        XCTAssertEqual(connection.phase, .suspended)
        XCTAssertFalse(connection.canRetryRecovery)
        XCTAssertTrue(connection.recoveryMessage?.contains("Trill will reconnect when you return") == true)
        await connection.retryRecoveryNow()
        let backgroundRequests = await fixture.requests.count
        let delays = await sleeper.delays
        XCTAssertEqual(backgroundRequests, 1)
        XCTAssertEqual(delays, [])
        await connection.foreground()
        try await eventually("Foreground recovers suspended failure") { connection.connected }
        let requests = await fixture.requests
        XCTAssertEqual(requests.map(\.sessionID), ["locked-request", "locked-request"])
        XCTAssertNil(connection.recoveryMessage)
    }

    @MainActor func testBackgroundDuringStartPausesWithoutAcceptingInputThenFreshAttaches() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport(startGate: gate)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        let environment = lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper)
        let opening = Task { await connection.connect(sessionID: "locked-start", environment: environment) }
        try await eventually("UDP start blocked") { await gate.arrivals == 1 }
        connection.draftText = "keep during lock"
        await connection.background()
        await gate.release()
        await opening.value
        try await eventually("Started transport paused") { await old.pauseCount == 1 }
        XCTAssertEqual(connection.phase, .suspended)
        XCTAssertFalse(connection.terminal.isUserInteractionEnabled)
        XCTAssertFalse(connection.canRetryRecovery)
        XCTAssertFalse(connection.insertDraft())
        connection.key("locked input")
        await connection.foreground()
        try await eventually("Foreground replacement connected") { connection.connected }
        let requests = await fixture.requests
        let oldKeys = await old.keystrokes
        let freshKeys = await fresh.keystrokes
        XCTAssertEqual(requests.map(\.sessionID), ["locked-start", "locked-start"])
        XCTAssertEqual(oldKeys, [])
        XCTAssertEqual(freshKeys, [])
        XCTAssertEqual(connection.draftText, "keep during lock")
    }

    @MainActor func testPauseFailureCleansUpWithoutRetryingWhileLocked() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport(failsToPause: true)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "pause-failed", environment:
            lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await connection.background()
        try await eventually("Failed pause releases lease") { await fixture.released == ["lease-1"] }
        XCTAssertEqual(connection.phase, .suspended)
        XCTAssertFalse(connection.canRetryRecovery)
        XCTAssertTrue(connection.recoveryMessage?.contains("could not pause") == true)
        let delays = await sleeper.delays
        XCTAssertEqual(delays, [])
        await connection.foreground()
        try await eventually("Foreground replaces failed pause") { connection.connected }
        XCTAssertNil(connection.recoveryMessage)
    }

    @MainActor func testRetryNowIsUnavailableWhileFailedTransportCleanupIsPending() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(releaseGate: gate)
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "cleanup-pending", environment:
            lifecycleEnvironment(fixture, transports: [old], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await old.setState(.stopped)
        try await eventually("Cleanup blocked") { await gate.arrivals == 1 }
        XCTAssertFalse(connection.canRetryRecovery)
        await connection.retryRecoveryNow()
        let pendingRequests = await fixture.requests.count
        XCTAssertEqual(pendingRequests, 1)
        await gate.release()
        try await eventually("Cleanup completed and retry offered") { connection.canRetryRecovery }
        await connection.retryRecoveryNow()
        try await eventually("Recovery connected") { connection.connected }
        let requests = await fixture.requests.count
        XCTAssertEqual(requests, 2)
    }

    @MainActor func testLockDuringFailureCleanupSuspendsRecoveryUntilForeground() async throws {
        let gate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(releaseGate: gate)
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "locked-cleanup", environment:
            lifecycleEnvironment(fixture, transports: [old], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        await old.setState(.stopped)
        try await eventually("Failure cleanup blocked") { await gate.arrivals == 1 }
        await connection.background()
        XCTAssertEqual(connection.phase, .suspended)
        XCTAssertFalse(connection.canRetryRecovery)
        await gate.release()
        // disconnect() is intentionally not used as a barrier: it would clear intent.
        // Foreground serializes its fresh attach behind the pending cleanup.
        await connection.foreground()
        try await eventually("Foreground recovers after locked cleanup") { connection.connected }
        let requests = await fixture.requests
        let delays = await sleeper.delays
        XCTAssertEqual(requests.map(\.sessionID), ["locked-cleanup", "locked-cleanup"])
        XCTAssertEqual(delays, [])
        XCTAssertNil(connection.recoveryMessage)
    }

    @MainActor func testReconnectPreservesDraftAndNeverReplaysDeliveredOrQueuedInput() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let gate = TerminalLifecycleGate()
        let old = TerminalLifecycleTransport()
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await gate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        await connection.connect(sessionID: "input-session", environment:
            lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper))
        try await eventually("Initial connection") { connection.connected }
        connection.key("already delivered")
        try await eventually("First input delivered") { await old.keystrokes == [Data("already delivered".utf8)] }
        await old.blockKeystrokes(on: gate)
        connection.draftText = "retain pending draft"
        XCTAssertTrue(connection.insertDraft())
        connection.key("queued behind draft")
        try await eventually("Draft handoff blocked") { await gate.arrivals == 1 }
        await connection.background()
        XCTAssertFalse(connection.draftInsertionPending)
        XCTAssertFalse(connection.insertDraft())
        connection.key("typed while offline")
        await connection.foreground()
        try await eventually("Fresh input stream connected") { connection.connected }
        let freshKeysBeforeCompletion = await fresh.keystrokes
        XCTAssertEqual(freshKeysBeforeCompletion, [])
        XCTAssertEqual(connection.draftText, "retain pending draft")

        await gate.release()
        try await eventually("Old handoff completed late") { await old.keystrokes.count == 2 }
        // A stale completion must neither clear the retained draft nor send into the new stream.
        XCTAssertEqual(connection.draftText, "retain pending draft")
        XCTAssertFalse(connection.draftInsertionPending)
        let freshKeys = await fresh.keystrokes
        let oldKeys = await old.keystrokes
        XCTAssertEqual(freshKeys, [])
        XCTAssertEqual(oldKeys, [Data("already delivered".utf8), Data("retain pending draft".utf8)])
        XCTAssertTrue(connection.insertDraft(), "Only an explicit insertion may resend retained text")
        try await eventually("Explicit draft insertion completed") { connection.draftText.isEmpty }
        let explicitKeys = await fresh.keystrokes
        XCTAssertEqual(explicitKeys, [Data("retain pending draft".utf8)])
    }

    @MainActor func testKeyboardAndResizeOperationsReachTransportThroughEnvelopes() async throws {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        connection.draftText = "keep this draft"
        connection.send(source: connection.terminal, data: Array("hello 日本語".utf8)[...])
        connection.controlHeld = true
        connection.send(source: connection.terminal, data: [UInt8(99)][...])
        XCTAssertFalse(connection.controlHeld)
        connection.key("\r")
        connection.sizeChanged(source: connection.terminal, newCols: 120, newRows: 40)
        connection.sizeChanged(source: connection.terminal, newCols: 0, newRows: 0)
        channel.continuation.finish()
        var delivered: [MoshClientOp] = []
        for await envelope in channel.stream {
            XCTAssertNil(envelope.draftID)
            try await connection.handoff(envelope) { delivered.append($0) }
        }
        XCTAssertEqual(delivered, [.keystrokes(Data("hello 日本語".utf8)), .keystrokes(Data([3])),
                                   .keystrokes(Data("\r".utf8)), .resize(cols: 120, rows: 40),
                                   .resize(cols: 1, rows: 1)])
        XCTAssertEqual(connection.draftText, "keep this draft")
        XCTAssertFalse(connection.draftInsertionPending)
    }

}

private enum TerminalLifecycleTestError: Error { case timedOut }

private enum TerminalAttachOutcome: Sendable {
    case success, transient, lostResponse, http(Int)
}

private enum TerminalReleaseOutcome: Sendable {
    case success, transient, http(Int)
}

// Deliberately ignores cancellation until released, so late-result handling is exercised.
private actor TerminalLifecycleGate {
    private var isOpen = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private(set) var arrivals = 0

    func wait() async {
        arrivals += 1
        if isOpen { return }
        await withCheckedContinuation { waiters.append($0) }
    }

    func release() {
        isOpen = true
        let pending = waiters
        waiters.removeAll()
        for waiter in pending { waiter.resume() }
    }
}

private actor TerminalRetrySleeper {
    private(set) var delays: [Duration] = []
    private(set) var canceled: [Int] = []
    private var waiters: [Int: CheckedContinuation<Void, Never>] = [:]

    func sleep(_ duration: Duration) async throws {
        let index = delays.count
        delays.append(duration)
        await withCheckedContinuation { waiters[index] = $0 }
        // Return normally even after cancellation to exercise the caller's stale-task guards.
        if Task.isCancelled { canceled.append(index) }
    }

    func resume(_ index: Int) { waiters.removeValue(forKey: index)?.resume() }
    func resumeAll() {
        let pending = Array(waiters.values)
        waiters.removeAll()
        for waiter in pending { waiter.resume() }
    }
}

private actor TerminalLifecycleFixture {
    struct Request: Sendable {
        let sessionID: String
        let cols: Int
        let rows: Int
    }
    private let outcomes: [TerminalAttachOutcome]
    private let attachGate: TerminalLifecycleGate?
    private let gatedAttempt: Int
    private let releaseOutcomes: [TerminalReleaseOutcome]
    private let releaseGate: TerminalLifecycleGate?
    private(set) var requests: [Request] = []
    private(set) var released: [String] = []
    // Model the existing same-device server contract: a fresh POST replaces only
    // this session's old lease, including one whose response never reached iOS.
    private(set) var activeLeases: [String: String] = [:]
    private(set) var replaced: [String] = []

    init(outcomes: [TerminalAttachOutcome] = [], attachGate: TerminalLifecycleGate? = nil, gatedAttempt: Int = 0,
         releaseOutcomes: [TerminalReleaseOutcome] = [], releaseGate: TerminalLifecycleGate? = nil) {
        self.outcomes = outcomes
        self.attachGate = attachGate
        self.gatedAttempt = gatedAttempt
        self.releaseOutcomes = releaseOutcomes
        self.releaseGate = releaseGate
    }

    func attach(_ sessionID: String, cols: Int, rows: Int) async throws -> MoshLease {
        requests.append(Request(sessionID: sessionID, cols: cols, rows: rows))
        let attempt = requests.count
        if attempt == gatedAttempt { await attachGate?.wait() }
        let outcome = outcomes.indices.contains(attempt - 1) ? outcomes[attempt - 1] : .success
        switch outcome {
        case .transient: throw URLError(.notConnectedToInternet)
        case .http(let status): throw APIRequestError(status: status, message: "Fixture HTTP \(status)")
        case .success, .lostResponse:
            if let old = activeLeases.updateValue("lease-\(attempt)", forKey: sessionID) { replaced.append(old) }
            if case .lostResponse = outcome { throw URLError(.networkConnectionLost) }
            return MoshLease(host: "fixture.invalid", port: 60000, key: "fixture-key", leaseId: "lease-\(attempt)")
        }
    }

    func release(_ id: String) async throws {
        released.append(id)
        let index = released.count - 1
        await releaseGate?.wait()
        let outcome = releaseOutcomes.indices.contains(index) ? releaseOutcomes[index] : .success
        switch outcome {
        case .transient: throw URLError(.networkConnectionLost)
        case .http(let status):
            if status == 404 { activeLeases = activeLeases.filter { $0.value != id } }
            throw APIRequestError(status: status, message: "Fixture DELETE HTTP \(status)")
        case .success: activeLeases = activeLeases.filter { $0.value != id }
        }
    }
}

private actor TerminalLifecycleTransport: TerminalTransport {
    private(set) var state: MoshSessionState = .idle
    private(set) var startCount = 0
    private(set) var stopCount = 0
    private(set) var pauseCount = 0
    private(set) var resumeCount = 0
    private var responseCount: UInt64 = 0
    private var responseGate: TerminalLifecycleGate?
    private var drainGate: TerminalLifecycleGate?
    var peerResponseCount: UInt64 {
        get async {
            if let responseGate { await responseGate.wait() }
            return responseCount
        }
    }
    func blockResponses(on gate: TerminalLifecycleGate) { responseGate = gate }
    func blockNextOutput(on gate: TerminalLifecycleGate, text: String) {
        drainGate = gate
        hostOps.append(.hostBytes(Data(text.utf8)))
    }
    private let respondsOnResume: Bool
    private let resumeGate: TerminalLifecycleGate?
    private let returnsCompletedResumeAfterStop: Bool
    private(set) var statesAtStop: [MoshSessionState] = []
    private var hostOps: [MoshHostOp]
    private var operations: [MoshClientOp] = []
    private var inputGate: TerminalLifecycleGate?
    private let remainsRunningInBackground: Bool
    private let startGate: TerminalLifecycleGate?
    private let failsToPause: Bool

    init(responds: Bool = true, remainsRunningInBackground: Bool = false,
         startGate: TerminalLifecycleGate? = nil, failsToPause: Bool = false,
         respondsOnResume: Bool = false, resumeGate: TerminalLifecycleGate? = nil,
         returnsCompletedResumeAfterStop: Bool = false) {
        hostOps = responds ? [.hostBytes(Data("fixture terminal".utf8))] : []
        self.remainsRunningInBackground = remainsRunningInBackground
        self.startGate = startGate
        self.failsToPause = failsToPause
        self.respondsOnResume = respondsOnResume
        self.resumeGate = resumeGate
        self.returnsCompletedResumeAfterStop = returnsCompletedResumeAfterStop
    }

    var keystrokes: [Data] {
        operations.compactMap { if case .keystrokes(let bytes) = $0 { return bytes }; return nil }
    }

    func setState(_ state: MoshSessionState) { self.state = state }
    func respond() { hostOps.append(.hostBytes(Data("fixture response".utf8))) }
    func blockKeystrokes(on gate: TerminalLifecycleGate) { inputGate = gate }
    func start() async throws {
        startCount += 1
        await startGate?.wait()
        state = .running
    }
    func stop() async {
        stopCount += 1; statesAtStop.append(state); state = .stopped
        await resumeGate?.release()
    }
    func prepareForApplicationBackground() async throws -> MoshSnapshot {
        pauseCount += 1
        if failsToPause { throw URLError(.networkConnectionLost) }
        if !remainsRunningInBackground { state = .suspended }
        return MoshSnapshot(endpoint: .init(host: "fixture.invalid", port: 60000, keyBase64_22: "fixture-key"),
                            transportState: Data(), createdAtMs: 0)
    }
    func resumeFromApplicationBackground() async throws {
        resumeCount += 1
        await resumeGate?.wait()
        if state == .stopped, returnsCompletedResumeAfterStop { return }
        guard state != .stopped else { throw CancellationError() }
        state = .running
        if respondsOnResume { responseCount += 1 }
    }
    func acknowledge() { responseCount += 1 }
    func enqueue(_ op: MoshClientOp) async throws {
        if case .keystrokes = op, let inputGate { await inputGate.wait() }
        operations.append(op)
    }
    func drainHostOps() async -> [MoshHostOp] {
        let pending = hostOps
        hostOps.removeAll()
        let gate = drainGate; drainGate = nil
        await gate?.wait()
        return pending
    }
}


extension ModelTests {
    func testPhotoPathsAreLiteralShellArgumentsAndRejectControlCharacters() {
        let photo = PhotoAttachment(path: "/tmp/a 'quoted' $(touch nope).jpg", fileName: "photo.jpg", contentType: "image/jpeg", byteCount: 10)
        XCTAssertEqual(photo.terminalPath, "'/tmp/a '\"'\"'quoted'\"'\"' $(touch nope).jpg'")
        for path in ["relative.jpg", "/tmp/line\nreturn.jpg", "/tmp/escape\u{1b}.jpg", "/tmp/control\u{85}.jpg"] {
            XCTAssertNil(PhotoAttachment(path: path, fileName: "photo.jpg", contentType: "image/jpeg", byteCount: 1).terminalPath)
        }
    }

    @MainActor func testPhotoPreparationBoundsPixelsAndRemovesLocationMetadata() throws {
        let format = UIGraphicsImageRendererFormat(); format.scale = 1
        let image = UIGraphicsImageRenderer(size: CGSize(width: 3000, height: 1500), format: format).image { context in
            UIColor.systemTeal.setFill(); context.fill(CGRect(x: 0, y: 0, width: 3000, height: 1500))
        }
        let sourceData = NSMutableData()
        let destination = try XCTUnwrap(CGImageDestinationCreateWithData(sourceData, UTType.jpeg.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(destination, try XCTUnwrap(image.cgImage), [
            kCGImagePropertyGPSDictionary: [kCGImagePropertyGPSLatitude: 1.0, kCGImagePropertyGPSLatitudeRef: "N",
                                           kCGImagePropertyGPSLongitude: 1.0, kCGImagePropertyGPSLongitudeRef: "E"]
        ] as CFDictionary)
        XCTAssertTrue(CGImageDestinationFinalize(destination))
        let jpeg = try TerminalPhoto.jpeg(from: sourceData as Data)
        let source = try XCTUnwrap(CGImageSourceCreateWithData(jpeg as CFData, nil))
        XCTAssertEqual(CGImageSourceGetType(source) as String?, UTType.jpeg.identifier)
        let properties = try XCTUnwrap(CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any])
        XCTAssertEqual(properties[kCGImagePropertyPixelWidth] as? Int, 2048)
        XCTAssertEqual(properties[kCGImagePropertyPixelHeight] as? Int, 1024)
        XCTAssertNil(properties[kCGImagePropertyGPSDictionary])
        XCTAssertLessThan(jpeg.count, 10 * 1024 * 1024)
    }

    @MainActor func testWorkspaceImagePreviewIsBoundedAndRejectsInvalidData() throws {
        let format = UIGraphicsImageRendererFormat(); format.scale = 1
        let image = UIGraphicsImageRenderer(size: CGSize(width: 3000, height: 200), format: format).image { context in
            UIColor.systemBlue.setFill(); context.fill(CGRect(x: 0, y: 0, width: 3000, height: 200))
        }
        let input = try XCTUnwrap(image.pngData())
        let data = try WorkspaceImage.thumbnail(input)
        let preview = try XCTUnwrap(UIImage(data: data))
        XCTAssertEqual(preview.size.width, 2048)
        XCTAssertLessThan(preview.size.height, 200)
        XCTAssertThrowsError(try WorkspaceImage.thumbnail(Data("not an image".utf8)))
        XCTAssertThrowsError(try WorkspaceImage.thumbnail(Data(count: 10 * 1024 * 1024 + 1)))
    }

    @MainActor func testPhotoPreparationRejectsNonImageData() {
        XCTAssertThrowsError(try TerminalPhoto.jpeg(from: Data("not an image".utf8)))
        XCTAssertThrowsError(try TerminalPhoto.jpeg(from: Data()))
    }
}


extension ModelTests {
    @MainActor func testShiftReturnUsesBracketedNewlineOrRefusesUnsafeLegacySubmit() async {
        for bracketed in [false, true] {
            let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
            let connection = TerminalConnection(inputContinuation: channel.continuation)
            connection.phase = .connected
            if bracketed { connection.terminal.feed(text: "\u{1b}[?2004h") }
            connection.shiftHeld = true
            connection.key("\r")
            XCTAssertFalse(connection.shiftHeld)
            if bracketed { XCTAssertNil(connection.inputNotice) }
            else { XCTAssertNotNil(connection.inputNotice) }
            let operations = await collectedInput(channel)
            XCTAssertEqual(operations, bracketed ? [.keystrokes(Data("\u{1b}[200~\n\u{1b}[201~".utf8))] : [])
        }
    }
}

extension ModelTests {
    func testTerminalConsentRequestRejectsChangedComputerAndSession() {
        let computer = Computer(name: "First Mac", address: "https://first.invalid",
                                token: token, fingerprint: String(repeating: "b", count: 64))
        let request = TerminalConsentRequest(computer: computer, sessionID: "first-session", agent: "Terminal")
        XCTAssertTrue(request.isCurrent(computer: computer, sessionID: "first-session", changingComputer: false, readOnly: false))
        XCTAssertFalse(request.isCurrent(computer: nil, sessionID: "first-session", changingComputer: false, readOnly: false))
        XCTAssertFalse(request.isCurrent(computer: computer, sessionID: nil, changingComputer: false, readOnly: false))
        XCTAssertFalse(request.isCurrent(computer: computer, sessionID: "second-session", changingComputer: false, readOnly: false))

        var changed = computer; changed.address = "https://second.invalid"
        XCTAssertFalse(request.isCurrent(computer: changed, sessionID: "first-session", changingComputer: false, readOnly: false))
        changed = computer; changed.token = String(repeating: "c", count: 64)
        XCTAssertFalse(request.isCurrent(computer: changed, sessionID: "first-session", changingComputer: false, readOnly: false))
        changed = computer; changed.fingerprint = String(repeating: "d", count: 64)
        XCTAssertFalse(request.isCurrent(computer: changed, sessionID: "first-session", changingComputer: false, readOnly: false))

        // The presented destination remains the original snapshot after a computer change.
        XCTAssertEqual(request.computer, computer)
        XCTAssertEqual(request.sessionID, "first-session")
        XCTAssertEqual(request.destination, "Terminal · First Mac")
    }

    func testTerminalConsentRequestRejectsChangingComputerAndReadOnlyAccess() {
        let computer = Computer(name: "Mac", address: "https://mac.invalid", token: token, fingerprint: "")
        let request = TerminalConsentRequest(computer: computer, sessionID: "session", agent: "Terminal")
        XCTAssertFalse(request.isCurrent(computer: computer, sessionID: "session", changingComputer: true, readOnly: false))
        XCTAssertFalse(request.isCurrent(computer: computer, sessionID: "session", changingComputer: false, readOnly: true))
        XCTAssertFalse(request.isCurrent(computer: computer, sessionID: "session", changingComputer: true, readOnly: true))
        XCTAssertTrue(request.isCurrent(computer: computer, sessionID: "session", changingComputer: false, readOnly: false))
    }

    func testLegacySharingApprovalsRequireSeparateCurrentConsentAndRemainResettable() throws {
        let suite = "trill-consent-migration-" + UUID().uuidString
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let first = Computer(name: "A", address: "https://a.invalid", token: String(repeating: "a", count: 64), fingerprint: "")
        let second = Computer(name: "B", address: "https://b.invalid", token: String(repeating: "b", count: 64), fingerprint: "")
        let legacyTerminal = TerminalConsentPreference.remembering(second, in: TerminalConsentPreference.remembering(first, in: ""))
        let legacyPhoto = PhotoConsentPreference.remembering(second, in: PhotoConsentPreference.remembering(first, in: ""))
        defaults.set(legacyTerminal, forKey: "terminalConsent.v1")
        defaults.set(legacyPhoto, forKey: "photoConsent.v1")

        // Read the same current keys used by the consent sheets' AppStorage.
        func terminalApproved(_ computer: Computer, in store: UserDefaults) -> Bool {
            TerminalConsentPreference.contains(computer, in: store.string(forKey: TerminalConsentPreference.storageKey) ?? "")
        }
        func photoApproved(_ computer: Computer, in store: UserDefaults) -> Bool {
            PhotoConsentPreference.contains(computer, in: store.string(forKey: PhotoConsentPreference.storageKey) ?? "")
        }
        for computer in [first, second] {
            XCTAssertFalse(terminalApproved(computer, in: defaults))
            XCTAssertFalse(photoApproved(computer, in: defaults))
        }

        defaults.set(TerminalConsentPreference.remembering(first, in: ""), forKey: TerminalConsentPreference.storageKey)
        XCTAssertTrue(terminalApproved(first, in: defaults))
        XCTAssertFalse(photoApproved(first, in: defaults))
        defaults.set(PhotoConsentPreference.remembering(second, in: ""), forKey: PhotoConsentPreference.storageKey)
        XCTAssertTrue(photoApproved(second, in: defaults))
        XCTAssertFalse(terminalApproved(second, in: defaults))

        // Approve both permissions for both pairings, then reopen persistent storage.
        defaults.set(TerminalConsentPreference.remembering(second, in: defaults.string(forKey: TerminalConsentPreference.storageKey) ?? ""), forKey: TerminalConsentPreference.storageKey)
        defaults.set(PhotoConsentPreference.remembering(first, in: defaults.string(forKey: PhotoConsentPreference.storageKey) ?? ""), forKey: PhotoConsentPreference.storageKey)
        let reopened = try XCTUnwrap(UserDefaults(suiteName: suite))
        for computer in [first, second] {
            XCTAssertTrue(terminalApproved(computer, in: reopened))
            XCTAssertTrue(photoApproved(computer, in: reopened))
        }
        TerminalConsentPreference.clear(first, defaults: reopened)
        XCTAssertFalse(terminalApproved(first, in: reopened))
        XCTAssertTrue(photoApproved(first, in: reopened))
        PhotoConsentPreference.clear(second, defaults: reopened)
        XCTAssertFalse(photoApproved(second, in: reopened))
        XCTAssertTrue(terminalApproved(second, in: reopened))
        XCTAssertTrue(photoApproved(first, in: reopened))
        PhotoConsentPreference.clear(first, defaults: reopened)
        XCTAssertFalse(photoApproved(first, in: reopened))
        XCTAssertFalse(terminalApproved(first, in: reopened))
        XCTAssertTrue(terminalApproved(second, in: reopened))
        // Legacy approvals remain present but cannot restore either reset permission.
        XCTAssertEqual(reopened.string(forKey: "terminalConsent.v1"), legacyTerminal)
        XCTAssertEqual(reopened.string(forKey: "photoConsent.v1"), legacyPhoto)
    }

    func testPhotoPermissionPersistsSeparatelyAndResetsOnlyThatPairing() throws {
        let suite = "trill-photo-consent-" + UUID().uuidString
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let first = Computer(name: "A", address: "https://a.invalid", token: String(repeating: "a", count: 64), fingerprint: "")
        let second = Computer(name: "B", address: "https://b.invalid", token: String(repeating: "b", count: 64), fingerprint: "")
        defaults.set(TerminalConsentPreference.remembering(first, in: ""), forKey: TerminalConsentPreference.storageKey)
        XCTAssertFalse(PhotoConsentPreference.contains(first, in: defaults.string(forKey: PhotoConsentPreference.storageKey) ?? ""))
        let approved = PhotoConsentPreference.remembering(second, in: PhotoConsentPreference.remembering(first, in: ""))
        defaults.set(approved, forKey: PhotoConsentPreference.storageKey)
        let reopened = try XCTUnwrap(UserDefaults(suiteName: suite))
        XCTAssertTrue(PhotoConsentPreference.contains(first, in: reopened.string(forKey: PhotoConsentPreference.storageKey) ?? ""))
        var rePaired = first; rePaired.token = String(repeating: "c", count: 64)
        XCTAssertFalse(PhotoConsentPreference.contains(rePaired, in: approved))
        PhotoConsentPreference.clear(first, defaults: reopened)
        let remaining = reopened.string(forKey: PhotoConsentPreference.storageKey) ?? ""
        XCTAssertFalse(PhotoConsentPreference.contains(first, in: remaining))
        XCTAssertTrue(PhotoConsentPreference.contains(second, in: remaining))
        XCTAssertTrue(TerminalConsentPreference.contains(first, in: reopened.string(forKey: TerminalConsentPreference.storageKey) ?? ""))
    }
    func testRememberedTerminalPermissionIsScopedToHostCredentialAndCertificate() {
        let paired = Computer(name: "My Mac", address: "https://mac.example:7443", token: String(repeating: "a", count: 64), fingerprint: String(repeating: "b", count: 64))
        XCTAssertFalse(TerminalConsentPreference.contains(paired, in: ""))
        let permission = TerminalConsentPreference.remembering(paired, in: "")
        XCTAssertTrue(TerminalConsentPreference.contains(paired, in: permission))
        var other = paired
        other.address = "https://other.example:7443"
        XCTAssertFalse(TerminalConsentPreference.contains(other, in: permission))
        other = paired; other.token = String(repeating: "c", count: 64)
        XCTAssertFalse(TerminalConsentPreference.contains(other, in: permission))
        other = paired; other.fingerprint = String(repeating: "d", count: 64)
        XCTAssertFalse(TerminalConsentPreference.contains(other, in: permission))
        other = paired; other.name = "Renamed Mac"
        XCTAssertTrue(TerminalConsentPreference.contains(other, in: permission))
        XCTAssertFalse(permission.contains(paired.token))
        XCTAssertFalse(permission.contains(paired.address))
    }

    func testRememberedTerminalPermissionPersistsAndCanBeResetForOneComputer() throws {
        let suite = "trill-terminal-consent-test-" + UUID().uuidString
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let first = Computer(name: "First", address: "https://first.example", token: String(repeating: "a", count: 64), fingerprint: "")
        let second = Computer(name: "Second", address: "https://second.example", token: String(repeating: "b", count: 64), fingerprint: "")
        let value = TerminalConsentPreference.remembering(second, in: TerminalConsentPreference.remembering(first, in: ""))
        defaults.set(value, forKey: TerminalConsentPreference.storageKey)
        let reopened = try XCTUnwrap(UserDefaults(suiteName: suite))
        XCTAssertTrue(TerminalConsentPreference.contains(first, in: reopened.string(forKey: TerminalConsentPreference.storageKey) ?? ""))
        TerminalConsentPreference.clear(first, defaults: reopened)
        let remaining = reopened.string(forKey: TerminalConsentPreference.storageKey) ?? ""
        XCTAssertFalse(TerminalConsentPreference.contains(first, in: remaining))
        XCTAssertTrue(TerminalConsentPreference.contains(second, in: remaining))
    }
}

// Drive SwiftTerm's real tap hit testing without a mosh connection or personal session.
private final class TerminalLinkTestTap: UITapGestureRecognizer {
    var point: CGPoint = .zero
    override var state: UIGestureRecognizer.State { get { .ended } set {} }
    override func location(in view: UIView?) -> CGPoint { point }
}

/// Real loopback HTTP transport catches URLSession delegate-wiring regressions.
/// It never accesses the user's companion, projects, or sessions.
private final class VideoDownloadTestServer: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "trill.video-download-test")
    private var connections: [NWConnection] = []
    private let response: Data
    private let onRequest: @Sendable (String) -> Void
    init(body: Data, declaredSize: Int, status: String = "200 OK", extraHeaders: String = "",
         onRequest: @escaping @Sendable (String) -> Void = { _ in }) throws {
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
        response = Data("HTTP/1.1 \(status)\r\nContent-Type: video/mp4\r\nContent-Length: \(declaredSize)\r\n\(extraHeaders)Connection: close\r\n\r\n".utf8) + body
        self.onRequest = onRequest
    }
    func start() async throws -> URL {
        try await withCheckedThrowingContinuation { continuation in
            listener.stateUpdateHandler = { [weak self] state in
                guard let self else { return }
                switch state {
                case .ready:
                    self.listener.stateUpdateHandler = nil
                    continuation.resume(returning: URL(string: "http://localhost:\(self.listener.port!.rawValue)")!)
                case .failed(let error):
                    self.listener.stateUpdateHandler = nil
                    continuation.resume(throwing: error)
                default: break
                }
            }
            listener.newConnectionHandler = { [weak self] connection in
                guard let self else { connection.cancel(); return }
                self.connections.append(connection)
                connection.start(queue: self.queue)
                self.read(connection, prefix: Data())
            }
            listener.start(queue: queue)
        }
    }
    private func read(_ connection: NWConnection, prefix: Data) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 8192) { [weak self] data, _, done, error in
            guard let self, let data, error == nil else { return }
            let request = prefix + data
            if let text = String(data: request, encoding: .utf8), text.contains("\r\n\r\n") {
                self.onRequest(text)
                connection.send(content: self.response, completion: .contentProcessed { _ in })
            } else if !done && request.count < 8192 { self.read(connection, prefix: request) }
        }
    }
    func stop() {
        queue.sync {
            listener.cancel()
            connections.forEach { $0.cancel() }
            connections.removeAll()
        }
    }
}


extension ModelTests {
    @MainActor func testHerdrHandoffReleasesHiddenControllerAndReconnectsOnlyOnReturn() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        let firstSurface = UUID()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: firstSurface)
        await connection.connect(sessionID: "herdr-session", environment: lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Initial Herdr connection") { connection.connected }
        connection.draftText = "Keep this draft"
        connection.terminal.feed(text: "\u{1b}[?1h")
        connection.terminalDidDisappear(surfaceID: firstSurface)
        try await eventually("Hidden controller released") { await fixture.released == ["lease-1"] }
        XCTAssertEqual(connection.phase, .suspended)
        XCTAssertEqual(connection.draftText, "Keep this draft")
        XCTAssertEqual(connection.terminal.terminalInputStateSnapshot()?.applicationCursor, true)
        let oldStops = await old.stopCount
        XCTAssertEqual(oldStops, 1)
        await connection.foreground()
        let hiddenRequests = await fixture.requests.count
        XCTAssertEqual(hiddenRequests, 1, "App foreground must not reopen a hidden Herdr terminal")
        connection.sizeChanged(source: connection.terminal, newCols: 63, newRows: 21)
        let returningSurface = UUID()
        connection.terminalDidAppear(surfaceID: returningSurface)
        try await eventually("Visible session reconnected") { connection.connected }
        let requests = await fixture.requests
        let keys = await fresh.keystrokes
        XCTAssertEqual(requests.map(\.sessionID), ["herdr-session", "herdr-session"])
        XCTAssertEqual(requests.last?.cols, 63)
        XCTAssertEqual(requests.last?.rows, 21)
        XCTAssertEqual(keys, [], "Handoff must not replay input")
        XCTAssertEqual(connection.draftText, "Keep this draft")
        await connection.disconnect()
        connection.terminalDidDisappear(surfaceID: returningSurface)
        connection.terminalDidAppear(surfaceID: UUID())
        let finalRequests = await fixture.requests.count
        XCTAssertEqual(finalRequests, 2, "Explicit disconnect revokes automatic reconnect intent")
    }

    @MainActor func testHerdrBackgroundReleasesInsteadOfRetainingMoshAndWaitsUntilForeground() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport(respondsOnResume: true)
        let fresh = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: UUID())
        await connection.connect(sessionID: "background-handoff", environment: lifecycleEnvironment(fixture, transports: [old, fresh], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Initial Herdr connection") { connection.connected }
        await connection.background()
        let released = await fixture.released
        let retainedPauses = await old.pauseCount
        let stops = await old.stopCount
        XCTAssertEqual(released, ["lease-1"])
        XCTAssertEqual(retainedPauses, 0)
        XCTAssertEqual(stops, 1)
        connection.terminalDidAppear(surfaceID: UUID())
        let backgroundRequests = await fixture.requests.count
        XCTAssertEqual(backgroundRequests, 1, "A view appearing while backgrounded cannot reclaim control")
        await connection.foreground()
        try await eventually("Foreground reconnected") { connection.connected }
        let requests = await fixture.requests.count
        XCTAssertEqual(requests, 2)
    }

    @MainActor func testHerdrRapidReturnWaitsForReleaseAndIgnoresStaleSurfaceDisappearance() async throws {
        let releaseGate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(releaseGate: releaseGate)
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        let originalSurface = UUID(), returningSurface = UUID()
        addTeardownBlock { await releaseGate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: originalSurface)
        await connection.connect(sessionID: "rapid-return", environment: lifecycleEnvironment(fixture, transports: [TerminalLifecycleTransport(), TerminalLifecycleTransport()], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Initial connection") { connection.connected }
        connection.terminalDidDisappear(surfaceID: originalSurface)
        try await eventually("Release in flight") { await releaseGate.arrivals == 1 }
        connection.terminalDidAppear(surfaceID: returningSurface)
        connection.terminalDidAppear(surfaceID: returningSurface)
        let requestsBeforeRelease = await fixture.requests.count
        XCTAssertEqual(requestsBeforeRelease, 1)
        await releaseGate.release()
        try await eventually("One ordered replacement connection") { connection.connected }
        connection.terminalDidDisappear(surfaceID: originalSurface)
        XCTAssertTrue(connection.connected, "A previous presentation cannot release the current controller")
        let requests = await fixture.requests.count
        let releases = await fixture.released
        XCTAssertEqual(requests, 2)
        XCTAssertEqual(releases, ["lease-1"])
    }

    @MainActor func testHerdrHandoffOwnsAndReleasesLateAttachWithoutStartingHiddenTransport() async throws {
        let attachGate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(attachGate: attachGate, gatedAttempt: 1)
        let sleeper = TerminalRetrySleeper()
        let old = TerminalLifecycleTransport()
        let connection = TerminalConnection()
        let surface = UUID()
        addTeardownBlock { await attachGate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: surface)
        let connecting = Task { await connection.connect(sessionID: "late-herdr", environment: lifecycleEnvironment(fixture, transports: [old], sleeper: sleeper, returnsControlWhenHidden: true)) }
        try await eventually("Attach in flight") { await attachGate.arrivals == 1 }
        connection.terminalDidDisappear(surfaceID: surface)
        await attachGate.release()
        await connecting.value
        try await eventually("Late lease released") { await fixture.released == ["lease-1"] }
        let starts = await old.startCount
        let active = await fixture.activeLeases
        XCTAssertEqual(starts, 0)
        XCTAssertEqual(active, [:])
        XCTAssertEqual(connection.phase, .suspended)
    }

    @MainActor func testHerdrHandoffFailureStaysPausedAndReturnRecoversOnlyItsOwnLease() async throws {
        let fixture = TerminalLifecycleFixture(releaseOutcomes: [.transient])
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        let surface = UUID()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: surface)
        await connection.connect(sessionID: "failed-handoff", environment: lifecycleEnvironment(fixture, transports: [TerminalLifecycleTransport(), TerminalLifecycleTransport()], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Initial connection") { connection.connected }
        connection.terminalDidDisappear(surfaceID: surface)
        try await eventually("Handoff warning reported") { connection.cleanupWarning != nil }
        XCTAssertEqual(connection.phase, .suspended)
        let hiddenRequests = await fixture.requests.count
        XCTAssertEqual(hiddenRequests, 1)
        connection.terminalDidAppear(surfaceID: UUID())
        try await eventually("Visible same-device recovery") { connection.connected }
        let replaced = await fixture.replaced
        let releases = await fixture.released
        XCTAssertEqual(replaced, ["lease-1"])
        XCTAssertEqual(releases, ["lease-1"])
    }

    @MainActor func testHerdrReturnDoesNotTakeOverAnotherController() async throws {
        let fixture = TerminalLifecycleFixture(outcomes: [.success, .http(409)])
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        let surface = UUID()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: surface)
        await connection.connect(sessionID: "busy-herdr", environment: lifecycleEnvironment(fixture, transports: [TerminalLifecycleTransport()], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Initial connection") { connection.connected }
        connection.terminalDidDisappear(surfaceID: surface)
        try await eventually("Control released") { await fixture.released.count == 1 }
        connection.terminalDidAppear(surfaceID: UUID())
        try await eventually("Busy terminal reported") { if case .failed = connection.phase { return true }; return false }
        let requests = await fixture.requests.count
        let retries = await sleeper.delays
        XCTAssertEqual(requests, 2)
        XCTAssertEqual(retries, [])
    }

    @MainActor func testAppBackgroundPausesAllConnectionsBeforeWaitingForSlowHandoff() async throws {
        let releaseGate = TerminalLifecycleGate()
        let slowFixture = TerminalLifecycleFixture(releaseGate: releaseGate)
        let fastFixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let store = AppStore(loadSavedPairing: false)
        let slow = store.connection(for: "slow"), fast = store.connection(for: "fast")
        addTeardownBlock { await releaseGate.release(); await store.closeConnections(); await sleeper.resumeAll() }
        for connection in [slow, fast] { connection.terminalDidAppear(surfaceID: UUID()) }
        await slow.connect(sessionID: "slow", environment: lifecycleEnvironment(slowFixture, transports: [TerminalLifecycleTransport()], sleeper: sleeper, returnsControlWhenHidden: true))
        await fast.connect(sessionID: "fast", environment: lifecycleEnvironment(fastFixture, transports: [TerminalLifecycleTransport()], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Both connections opened") { slow.connected && fast.connected }
        let backgrounding = Task { await store.background() }
        try await eventually("Fast controller released despite blocked slow release") { await fastFixture.released.count == 1 }
        XCTAssertEqual(slow.phase, .suspended)
        XCTAssertEqual(fast.phase, .suspended)
        await releaseGate.release()
        await backgrounding.value
    }

    @MainActor func testXirpSurfaceSwitchRetainsWarmTransport() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let transport = TerminalLifecycleTransport(respondsOnResume: true)
        let connection = TerminalConnection()
        let surface = UUID()
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: surface)
        await connection.connect(sessionID: "xirp", environment: lifecycleEnvironment(fixture, transports: [transport], sleeper: sleeper))
        try await eventually("Xirp connected") { connection.connected }
        connection.terminalDidDisappear(surfaceID: surface)
        XCTAssertTrue(connection.connected)
        await connection.background()
        await connection.foreground()
        try await eventually("Warm Xirp transport resumed") { connection.connected }
        let requests = await fixture.requests.count
        let releases = await fixture.released
        XCTAssertEqual(requests, 1)
        XCTAssertEqual(releases, [])
    }
}


extension ModelTests {
    @MainActor func testSessionDetailVisibilityAutomaticallyHandsOffAndReconnectsHerdr() async throws {
        let fixture = TerminalLifecycleFixture()
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        let store = AppStore(loadSavedPairing: false)
        let session = RemoteSession(provider: "herdr", id: "visible-herdr", name: "Synthetic terminal", projectId: "project", projectName: "Synthetic project", status: "running", activityState: "idle", agent: "codex", branch: "", updatedAt: "", attachable: true, resumable: false)
        addTeardownBlock { await connection.disconnect(); await sleeper.resumeAll() }
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let originalWindow = scene.windows.first { $0.isKeyWindow }
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 402, height: 874)
        defer { window.isHidden = true; originalWindow?.makeKey() }
        func detail() -> AnyView {
            AnyView(NavigationStack {
                SessionDetailView(session: session, connection: connection, showWorkspace: .constant(false), workspaceTab: .constant(.files), previewAddress: .constant(nil))
            }.environment(store))
        }
        let controller = UIHostingController(rootView: detail())
        window.rootViewController = controller
        window.makeKeyAndVisible()
        try await eventually("Terminal presentation became visible") { connection.terminalIsVisible }
        await connection.connect(sessionID: session.id, environment: lifecycleEnvironment(fixture, transports: [TerminalLifecycleTransport(), TerminalLifecycleTransport()], sleeper: sleeper, returnsControlWhenHidden: true))
        try await eventually("Visible terminal connected") { connection.connected }
        connection.draftText = "Keep across navigation"
        controller.rootView = AnyView(Text("Sessions"))
        try await eventually("Navigation released controller") { await fixture.released == ["lease-1"] }
        XCTAssertFalse(connection.terminalIsVisible)
        XCTAssertEqual(connection.phase, .suspended)
        controller.rootView = detail()
        try await eventually("Returning presentation reconnected") { connection.connected }
        let requests = await fixture.requests.count
        XCTAssertEqual(requests, 2)
        XCTAssertEqual(connection.draftText, "Keep across navigation")
    }
}


extension ModelTests {
    @MainActor func testHerdrLostAttachResponseCannotClaimConfirmedDesktopHandoff() async throws {
        let attachGate = TerminalLifecycleGate()
        let fixture = TerminalLifecycleFixture(outcomes: [.lostResponse, .success], attachGate: attachGate, gatedAttempt: 1)
        let sleeper = TerminalRetrySleeper()
        let connection = TerminalConnection()
        let unused = TerminalLifecycleTransport(), fresh = TerminalLifecycleTransport()
        let surface = UUID()
        addTeardownBlock { await attachGate.release(); await connection.disconnect(); await sleeper.resumeAll() }
        connection.terminalDidAppear(surfaceID: surface)
        let connecting = Task { await connection.connect(sessionID: "lost-handoff", environment: lifecycleEnvironment(fixture, transports: [unused, fresh], sleeper: sleeper, returnsControlWhenHidden: true)) }
        try await eventually("Attach request pending") { await attachGate.arrivals == 1 }
        connection.terminalDidDisappear(surfaceID: surface)
        await attachGate.release()
        await connecting.value
        try await eventually("Unconfirmed handoff reported") { connection.cleanupWarning != nil && connection.recoveryMessage == connection.cleanupWarning }
        let hiddenRequests = await fixture.requests.count
        let releases = await fixture.released
        let starts = await unused.startCount
        XCTAssertEqual(hiddenRequests, 1)
        XCTAssertEqual(releases, [], "No lease ID is available for DELETE")
        XCTAssertEqual(starts, 0)
        XCTAssertEqual(connection.phase, .suspended)
        connection.terminalDidAppear(surfaceID: UUID())
        try await eventually("Visible same-device recovery") { connection.connected }
        let replaced = await fixture.replaced
        let keys = await fresh.keystrokes
        XCTAssertEqual(replaced, ["lease-1"])
        XCTAssertEqual(keys, [])
    }
}
