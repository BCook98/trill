import Foundation
import Security
import XCTest
@testable import Trill

final class ComputerLibraryTests: XCTestCase {
    private func computer(_ address: String = "https://mac.example:7443", token: String = String(repeating: "a", count: 64)) -> Computer {
        Computer(name: "Test Mac", address: address, token: token, fingerprint: "")
    }

    func testEmptyLibraryAndOptionalSelection() throws {
        var library = ComputerLibrary()
        XCTAssertNil(library.selected)
        try library.validate()
        library.upsert(computer())
        library.activeID = nil
        XCTAssertNil(library.selected)
        try library.validate()
    }

    func testCanonicalUpsertRetainsIdentityAndRepairsCredentials() throws {
        var library = ComputerLibrary()
        let firstID = library.upsert(computer("HTTPS://MAC.EXAMPLE:443/"))
        let secondID = library.upsert(computer("https://other.example"))
        var repaired = computer("https://mac.example", token: String(repeating: "b", count: 64))
        repaired.name = "Repaired Mac"
        repaired.fingerprint = String(repeating: "c", count: 64)
        XCTAssertEqual(library.upsert(repaired), firstID)
        XCTAssertEqual(library.computers.map(\.id), [firstID, secondID])
        XCTAssertEqual(library.activeID, firstID)
        XCTAssertEqual(library.selected?.computer, repaired)
        try library.validate()
    }

    func testTrailingSlashAndExplicitDefaultPortAreEquivalent() throws {
        var library = ComputerLibrary()
        let id = library.upsert(computer("https://MAC.example/"))
        XCTAssertEqual(library.upsert(computer("https://mac.example:443")), id)
        XCTAssertEqual(library.computers.count, 1)
        XCTAssertEqual(library.selected?.computer.address, "https://mac.example")
        try library.validate()
    }

    func testDifferentHostsAndNondefaultPortsStayDistinct() throws {
        var library = ComputerLibrary()
        let ids = ["https://mac.example", "https://mac.example:7443", "https://other.example:7443"]
            .map { library.upsert(computer($0)) }
        XCTAssertEqual(Set(ids).count, 3)
        XCTAssertEqual(library.computers.count, 3)
        XCTAssertEqual(library.activeID, ids.last)
        try library.validate()
    }

    func testSchemesHaveSeparateEndpointIdentities() {
        // HTTP validity follows the build's transport policy; matching itself
        // must never collapse a different scheme into an HTTPS credential.
        var library = ComputerLibrary()
        let https = library.upsert(computer("https://localhost:7443"))
        let http = library.upsert(computer("http://localhost:7443"))
        XCTAssertNotEqual(https, http)
        XCTAssertEqual(library.computers.count, 2)
    }

    func testRemovalPreservesSelectionOrChoosesFirstRemaining() throws {
        var library = ComputerLibrary()
        let first = library.upsert(computer("https://first.example"))
        let second = library.upsert(computer("https://second.example"))
        let third = library.upsert(computer("https://third.example"))
        library.remove(id: second)
        XCTAssertEqual(library.activeID, third)
        let unchanged = library
        library.remove(id: UUID())
        XCTAssertEqual(library, unchanged)
        library.remove(id: third)
        XCTAssertEqual(library.activeID, first)
        library.remove(id: first)
        XCTAssertTrue(library.computers.isEmpty)
        XCTAssertNil(library.activeID)
        XCTAssertNil(library.selected)
        try library.validate()
    }

    func testValidationRejectsDuplicateIDsAndCanonicalEndpoints() {
        let first = SavedComputer(computer: computer("https://mac.example"))
        let sameID = SavedComputer(id: first.id, computer: computer("https://other.example"))
        XCTAssertThrowsError(try ComputerLibrary(computers: [first, sameID], activeID: first.id).validate())
        let sameEndpoint = SavedComputer(computer: computer("https://MAC.EXAMPLE:443/"))
        XCTAssertThrowsError(try ComputerLibrary(computers: [first, sameEndpoint], activeID: first.id).validate())
    }

    func testValidationRejectsDanglingSelection() {
        XCTAssertThrowsError(try ComputerLibrary(activeID: UUID()).validate())
        XCTAssertThrowsError(try ComputerLibrary(computers: [SavedComputer(computer: computer())], activeID: UUID()).validate())
    }

    func testValidationRejectsInvalidComputerDetails() {
        var badPin = computer()
        badPin.fingerprint = "invalid"
        let invalid = [computer(token: "short"), badPin,
                       computer("http://remote.example:7443"), computer("https://mac.example/private"),
                       computer("https://mac.example?token=secret"), computer("https://user:pass@mac.example"),
                       computer("https://mac.example:0"), computer("https://mac.example:65536")]
        for value in invalid {
            var library = ComputerLibrary()
            library.upsert(value)
            XCTAssertThrowsError(try library.validate())
        }
    }

    func testCapacityAllows32AndRejects33WithoutDroppingEntries() throws {
        var library = ComputerLibrary()
        for index in 0..<32 { library.upsert(computer("https://mac\(index).example")) }
        try library.validate()
        let thirtyThird = library.upsert(computer("https://extra.example"))
        XCTAssertEqual(library.computers.count, 33)
        XCTAssertEqual(library.activeID, thirtyThird)
        XCTAssertThrowsError(try library.validate())
        XCTAssertThrowsError(try JSONEncoder().encode(library))
    }

    func testVersionOneRoundTripAndInvalidSchemaRejection() throws {
        var library = ComputerLibrary()
        library.upsert(computer())
        let data = try JSONEncoder().encode(library)
        XCTAssertEqual(try JSONDecoder().decode(ComputerLibrary.self, from: data), library)
        let valid = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(valid["version"] as? Int, 1)
        var future = valid
        future["version"] = 2
        var missingVersion = valid
        missingVersion.removeValue(forKey: "version")
        var duplicate = valid
        let entry = try XCTUnwrap((valid["computers"] as? [[String: Any]])?.first)
        duplicate["computers"] = [entry, entry]
        var dangling = valid
        dangling["activeID"] = UUID().uuidString
        for invalid in [future, missingVersion, duplicate, dangling] {
            let encoded = try JSONSerialization.data(withJSONObject: invalid)
            XCTAssertThrowsError(try JSONDecoder().decode(ComputerLibrary.self, from: encoded))
        }
    }

    func testKeychainRoundTripUpdateIsolationAndAccessibility() throws {
        let firstAccount = "computer-library-tests-" + UUID().uuidString
        let secondAccount = "computer-library-tests-" + UUID().uuidString
        defer {
            try? ComputerLibraryStore.delete(account: firstAccount)
            try? ComputerLibraryStore.delete(account: secondAccount)
        }
        XCTAssertNil(try ComputerLibraryStore.load(account: firstAccount))
        var first = ComputerLibrary()
        let id = first.upsert(computer())
        var second = ComputerLibrary()
        second.upsert(computer("https://second.example"))
        try ComputerLibraryStore.save(first, account: firstAccount)
        try ComputerLibraryStore.save(second, account: secondAccount)
        XCTAssertEqual(try ComputerLibraryStore.load(account: firstAccount), first)
        XCTAssertEqual(try ComputerLibraryStore.load(account: secondAccount), second)
        XCTAssertEqual(first.upsert(computer(token: String(repeating: "d", count: 64))), id)
        try ComputerLibraryStore.save(first, account: firstAccount)
        XCTAssertEqual(try ComputerLibraryStore.load(account: firstAccount), first)
        XCTAssertEqual(try ComputerLibraryStore.load(account: secondAccount), second)

        var query = itemQuery(firstAccount)
        query[kSecReturnAttributes as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        XCTAssertEqual(status, errSecSuccess)
        let attributes = try XCTUnwrap(result as? [String: Any])
        XCTAssertEqual(attributes[kSecAttrAccessible as String] as? String, kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)

        try ComputerLibraryStore.delete(account: firstAccount)
        XCTAssertNil(try ComputerLibraryStore.load(account: firstAccount))
        XCTAssertEqual(try ComputerLibraryStore.load(account: secondAccount), second)
        try ComputerLibraryStore.delete(account: firstAccount)
    }

    func testCorruptKeychainDataThrowsAndRemainsIntact() throws {
        let account = "computer-library-tests-" + UUID().uuidString
        defer { try? ComputerLibraryStore.delete(account: account) }
        var library = ComputerLibrary()
        library.upsert(computer())
        try ComputerLibraryStore.save(library, account: account)
        let corruptions = [Data("broken JSON".utf8), Data(#"{"version":2,"computers":[],"activeID":null}"#.utf8)]
        for data in corruptions {
            XCTAssertEqual(SecItemUpdate(itemQuery(account) as CFDictionary, [kSecValueData as String: data] as CFDictionary), errSecSuccess)
            XCTAssertThrowsError(try ComputerLibraryStore.load(account: account))
            XCTAssertThrowsError(try ComputerLibraryStore.load(account: account))
            var query = itemQuery(account)
            query[kSecReturnData as String] = true
            query[kSecMatchLimit as String] = kSecMatchLimitOne
            var result: CFTypeRef?
            XCTAssertEqual(SecItemCopyMatching(query as CFDictionary, &result), errSecSuccess)
            XCTAssertEqual(result as? Data, data)
        }
    }

    func testInvalidSaveLeavesExistingKeychainBlobUntouched() throws {
        let account = "computer-library-tests-" + UUID().uuidString
        defer { try? ComputerLibraryStore.delete(account: account) }
        var valid = ComputerLibrary()
        valid.upsert(computer())
        try ComputerLibraryStore.save(valid, account: account)
        var invalid = valid
        invalid.computers.append(valid.computers[0])
        XCTAssertThrowsError(try ComputerLibraryStore.save(invalid, account: account))
        XCTAssertEqual(try ComputerLibraryStore.load(account: account), valid)
    }

    private func itemQuery(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: PairingStore.service,
         kSecAttrAccount as String: account,
         kSecAttrSynchronizable as String: false]
    }
}
