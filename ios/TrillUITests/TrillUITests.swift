import XCTest

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
