import XCTest
import UIKit
import Vision

final class TrillUITests: XCTestCase {
    func testPrivacyAndSupportAreAvailableBeforePairing() {
        let app = XCUIApplication()
        app.launchArguments = ["--test-onboarding"]
        app.launch()
        let policy = app.buttons["privacy-policy"]
        XCTAssertTrue(policy.waitForExistence(timeout: 10))
        policy.tap()
        XCTAssertTrue(app.navigationBars["Privacy policy"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "Trill privacy policy")).firstMatch.exists)
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["support"].waitForExistence(timeout: 10))
        app.buttons["support"].tap()
        XCTAssertTrue(app.navigationBars["Support"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.links["Email trill@bcook.io"].exists || app.buttons["Email trill@bcook.io"].exists)
    }

    func testPairingScreenAndValidation() {
        let app = XCUIApplication()
        app.launchArguments = ["--test-onboarding"]
        app.launch()
        let pair = app.buttons["pair-computer"]
        XCTAssertTrue(pair.waitForExistence(timeout: 10))
        pair.tap()
        XCTAssertTrue(app.buttons["scan-pairing"].exists)
        app.buttons["Enter details manually"].tap()
        XCTAssertTrue(app.textFields["sidecar-address"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.secureTextFields["pairing-token"].exists)
        XCTAssertTrue(app.buttons["confirm-pairing"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["confirm-pairing"].isEnabled)
        app.buttons["Cancel"].tap()
        XCTAssertTrue(pair.exists)
    }
}

// Opt-in integration fixture. The local harness owns Herdr, the agent, companion
// and UDP relay; normal CI has no fixture and skips this test.
extension TrillUITests {
    @MainActor func testOwnedHerdrScrollUnderNetworkLatency() async throws {
        let fixtureURL = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent(".local/herdr-scroll-e2e/fixture.json")
        guard FileManager.default.fileExists(atPath: fixtureURL.path) else {
            throw XCTSkip("Start the isolated Herdr scroll E2E harness explicitly")
        }
        let fixture = try JSONDecoder().decode(HerdrScrollFixture.self, from: Data(contentsOf: fixtureURL))
        guard fixture.kind == "trill-owned-herdr-scroll-e2e",
              URL(string: fixture.address)?.host == "127.0.0.1" else {
            XCTFail("Refusing a fixture outside the owned loopback lab"); return
        }
        func control(_ route: String, body: [String: Int]? = nil) async throws -> Data {
            let path = route.hasPrefix("/v1/") ? route : "/__probe/" + route
            var request = URLRequest(url: URL(string: fixture.address + path)!)
            request.setValue("Bearer " + fixture.token, forHTTPHeaderField: "Authorization")
            if let body {
                request.httpMethod = "POST"
                request.httpBody = try JSONSerialization.data(withJSONObject: body)
                request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            }
            let (data, response) = try await URLSession.shared.data(for: request)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            return data
        }
        _ = try await control("network", body: ["rtt_ms": 0])
        let app = XCUIApplication()
        app.launchArguments = ["--test-onboarding"]
        addUIInterruptionMonitor(withDescription: "Dismiss password saving in the disposable simulator") { alert in
            if alert.buttons["Not Now"].exists { alert.buttons["Not Now"].tap(); return true }
            return false
        }
        app.launch()
        defer { app.terminate() }
        XCTAssertTrue(app.buttons["pair-computer"].waitForExistence(timeout: 15))
        app.buttons["pair-computer"].tap()
        app.buttons["Enter details manually"].tap()
        let address = app.textFields["sidecar-address"]
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.tap(); address.typeText(fixture.address)
        let token = app.secureTextFields["pairing-token"]
        token.tap(); token.typeText(fixture.token)
        app.buttons["confirm-pairing"].tap()
        if app.buttons["Not Now"].waitForExistence(timeout: 2) {
            app.buttons["Not Now"].coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        }
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        if springboard.buttons["Not Now"].waitForExistence(timeout: 3) {
            springboard.buttons["Not Now"].tap()
        }
        // This simulator seed omits the password-saving overlay from both app
        // and SpringBoard AX trees. Resolve its actual visible button with OCR.
        for _ in 0..<12 {
            if herdrDismissVisiblePasswordPrompt(app) { break }
            try await Task.sleep(for: .milliseconds(500))
        }
        let listing = try JSONSerialization.jsonObject(with: await control("/v1/sessions")) as? [String: Any]
        let sessions = listing?["sessions"] as? [[String: Any]] ?? []
        let owned = try XCTUnwrap(sessions.first { ($0["id"] as? String) == fixture.sessionID })
        let name = try XCTUnwrap(owned["name"] as? String)
        let session = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", name)).firstMatch
        guard session.waitForExistence(timeout: 15) else {
            XCTFail("The owned Herdr agent row was not visible"); return
        }
        herdrTap(app, session)
        XCTAssertFalse(app.buttons["open-terminal"].exists, "Selecting a live session opens it directly")
        let consent = app.buttons["Allow once"]
        if consent.waitForExistence(timeout: 10) { herdrTap(app, consent) }
        let terminal = app.descendants(matching: .any)["remote-terminal"].firstMatch
        guard terminal.waitForExistence(timeout: 20) else {
            XCTFail("The real terminal did not open"); return
        }
        let deadline = Date().addingTimeInterval(120)
        while true {
            let recent = try await control("recent")
            let object = try JSONSerialization.jsonObject(with: recent) as? [String: Any]
            let text = object?["text"] as? String ?? ""
            if text.components(separatedBy: .newlines).contains(where: {
                $0.trimmingCharacters(in: .whitespaces) == "E2E_SCROLL_READY"
            }) { break }
            guard Date() < deadline else { XCTFail("Real agent did not finish its scrolling output"); return }
            try await Task.sleep(for: .milliseconds(500))
        }
        while !herdrScreenText(app).contains("SCROLLTEST") {
            guard Date() < deadline else {
                XCTFail("Real agent output did not appear in the native terminal"); return
            }
            try await Task.sleep(for: .milliseconds(500))
        }
        let keyboard = app.buttons["terminal-input-keyboard"]
        if app.keyboards.firstMatch.exists, keyboard.exists { keyboard.tap() }
        var measurements: [[String: Any]] = []
        for roundTripMilliseconds in [0, 50, 100, 200] {
            _ = try await control("network", body: ["rtt_ms": roundTripMilliseconds])
            try await Task.sleep(for: .milliseconds(300))
            let before = herdrScreenText(app)
            let beforeNumbers = herdrScrollNumbers(before)
            let hostBefore = try await control("snapshot")
            let start = terminal.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
            let end = terminal.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75))
            let began = ProcessInfo.processInfo.systemUptime
            start.press(forDuration: 0.1, thenDragTo: end)
            var after = herdrScreenText(app)
            let responseDeadline = Date().addingTimeInterval(8)
            while herdrScrollNumbers(after) == beforeNumbers && Date() < responseDeadline {
                try await Task.sleep(for: .milliseconds(50)); after = herdrScreenText(app)
            }
            let hostAfter = try await control("snapshot")
            let image = XCTAttachment(screenshot: app.screenshot())
            image.name = "Herdr real agent scroll - RTT \(roundTripMilliseconds)ms"
            image.lifetime = .keepAlways; add(image)
            let afterNumbers = herdrScrollNumbers(after)
            let changed = !afterNumbers.isEmpty && afterNumbers != beforeNumbers
            XCTAssertTrue(changed, "Native output must change after an actual touch drag at \(roundTripMilliseconds)ms RTT")
            measurements.append([
                "simulated_rtt_ms": roundTripMilliseconds,
                "gesture_and_ocr_observation_ms": (ProcessInfo.processInfo.systemUptime - began) * 1000,
                "native_text_changed": changed, "host_viewport_changed": hostBefore != hostAfter,
                "before_line_numbers": beforeNumbers, "after_line_numbers": afterNumbers,
                "before_ocr": before, "after_ocr": after
            ])
            // Return toward the latest output using the same native gesture path.
            for _ in 0..<3 { end.press(forDuration: 0.1, thenDragTo: start) }
            try await Task.sleep(for: .milliseconds(roundTripMilliseconds + 300))
            let settledText = herdrScreenText(app)
            XCTAssertFalse(settledText.contains("Back to bottom"), "Return drags must reach the latest agent output")
            let corruption = settledText.range(of: "Trill\\s*[0-9]+\\s*Herdr", options: .regularExpression) != nil
            measurements[measurements.count - 1]["settled_after_return_ocr"] = settledText
            measurements[measurements.count - 1]["stray_digits_in_rendered_text"] = corruption
            let settledImage = XCTAttachment(screenshot: app.screenshot())
            settledImage.name = "Herdr settled return - RTT \(roundTripMilliseconds)ms"
            settledImage.lifetime = .keepAlways; add(settledImage)
            XCTAssertFalse(corruption, "Rendered text must match the clean real agent output after returning to the bottom")
        }
        let status = try await control("status")
        let statusAttachment = XCTAttachment(data: status, uniformTypeIdentifier: "public.json")
        statusAttachment.name = "Real UDP relay packet counts"; statusAttachment.lifetime = .keepAlways; add(statusAttachment)
        let data = try JSONSerialization.data(withJSONObject: measurements, options: [.prettyPrinted, .sortedKeys])
        let attachment = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
        attachment.name = "Herdr E2E scroll observations"; attachment.lifetime = .keepAlways; add(attachment)
        print("HERDR_SCROLL_E2E_REPORT " + String(decoding: data, as: UTF8.self))
    }
}

private func herdrScrollNumbers(_ text: String) -> [Int] {
    let expression = try! NSRegularExpression(pattern: "SCROLLTEST\\s+([0-9]{1,3})")
    let source = text as NSString
    return expression.matches(in: text, range: NSRange(location: 0, length: source.length))
        .compactMap { Int(source.substring(with: $0.range(at: 1))) }
}

@MainActor private func herdrTap(_ app: XCUIApplication, _ element: XCUIElement) {
    let frame = element.frame
    app.coordinate(withNormalizedOffset: .zero)
        .withOffset(CGVector(dx: frame.midX, dy: frame.midY)).tap()
}

@MainActor private func herdrDismissVisiblePasswordPrompt(_ app: XCUIApplication) -> Bool {
    guard let image = app.screenshot().image.cgImage else { return false }
    let request = VNRecognizeTextRequest()
    request.recognitionLevel = .accurate
    do { try VNImageRequestHandler(cgImage: image).perform([request]) }
    catch { return false }
    let observations = request.results ?? []
    guard observations.contains(where: { $0.topCandidates(1).first?.string == "Save Password?" }),
          let button = observations.first(where: { $0.topCandidates(1).first?.string == "Not Now" }) else { return false }
    let box = button.boundingBox
    app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(
        dx: box.midX * app.frame.width, dy: (1 - box.midY) * app.frame.height)).tap()
    return true
}

private struct HerdrScrollFixture: Decodable {
    let kind: String
    let address: String
    let token: String
    let sessionID: String
}

@MainActor private func herdrScreenText(_ app: XCUIApplication) -> String {
    guard let image = app.screenshot().image.cgImage else { return "" }
    let request = VNRecognizeTextRequest()
    request.recognitionLevel = .accurate
    request.usesLanguageCorrection = false
    do { try VNImageRequestHandler(cgImage: image).perform([request]) }
    catch { return "OCR ERROR: " + String(describing: error) }
    return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: "\n")
}
