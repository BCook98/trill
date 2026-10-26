import XCTest
@testable import Trill

final class WorkspacePreviewTests: XCTestCase {
    @MainActor func testPolicyCancellationPreservesActionablePreviewError() throws {
        let api = try APIClient(computer: Computer(name: "Preview test", address: "https://computer.example:7443", token: String(repeating: "a", count: 64), fingerprint: ""))
        let browser = WorkspaceBrowser(api: api, sessionID: "test")
        browser.error = "Check the development server, then reload."
        browser.loading = true
        browser.webView(browser.webView, didFailProvisionalNavigation: nil,
            withError: NSError(domain: "WebKitErrorDomain", code: 102))
        XCTAssertEqual(browser.error, "Check the development server, then reload.")
        XCTAssertFalse(browser.loading)
        browser.close()
    }

    @MainActor func testFailedLinkedDestinationIsRetainedForReload() throws {
        let api = try APIClient(computer: Computer(name: "Preview test", address: "https://computer.example:7443", token: String(repeating: "a", count: 64), fingerprint: ""))
        let browser = WorkspaceBrowser(api: api, sessionID: "test")
        browser.address = "http://localhost:3000/"
        let response = try XCTUnwrap(HTTPURLResponse(url: XCTUnwrap(URL(string: "https://computer.example:7443/dashboard?filter=a%20b&tab=2")), statusCode: 502, httpVersion: nil, headerFields: ["X-Trill-Preview-Error": "upstream-unavailable"]))
        browser.recordPreviewFailure(response, port: 3000, message: "Reload the server.")
        // reload() passes this address to open(), never the last successful page.
        XCTAssertEqual(browser.address, "http://localhost:3000/dashboard?filter=a%20b&tab=2")
        XCTAssertFalse(browser.loading)
        browser.close()
    }

    func testProxyFailureRequiresTrustedOriginStatusAndHeader() throws {
        let companion = try XCTUnwrap(URL(string: "https://computer.example:7443"))
        func response(_ address: String, status: Int = 502, code: String? = "upstream-unavailable") throws -> HTTPURLResponse {
            try XCTUnwrap(HTTPURLResponse(url: XCTUnwrap(URL(string: address)), statusCode: status,
                httpVersion: nil, headerFields: code.map { ["X-Trill-Preview-Error": $0] } ?? [:]))
        }
        let valid = try response("https://computer.example:7443/page")
        XCTAssertTrue(try XCTUnwrap(WorkspacePreviewFailure.message(for: valid, companion: companion, port: 3000)).contains("localhost:3000"))
        for address in ["https://other.example:7443/page", "https://computer.example:7444/page", "http://computer.example:7443/page"] {
            XCTAssertNil(WorkspacePreviewFailure.message(for: try response(address), companion: companion, port: 3000))
        }
        for status in [200, 401, 404, 503] {
            XCTAssertNil(WorkspacePreviewFailure.message(for: try response(companion.absoluteString, status: status), companion: companion, port: 3000))
        }
        XCTAssertNil(WorkspacePreviewFailure.message(for: try response(companion.absoluteString, code: nil), companion: companion, port: 3000))
        XCTAssertNil(WorkspacePreviewFailure.message(for: try response(companion.absoluteString, code: "unknown"), companion: companion, port: 3000))
        XCTAssertTrue(try XCTUnwrap(WorkspacePreviewFailure.message(for: response(companion.absoluteString, code: "response-rejected"), companion: companion, port: 3000)).contains("redirects"))
    }
}
