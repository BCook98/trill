import Foundation
import Security

struct SavedComputer: Codable, Identifiable, Equatable {
    let id: UUID
    var computer: Computer
    var notificationPreferences: AgentNotificationPreferences?

    init(id: UUID = UUID(), computer: Computer) {
        self.id = id
        self.computer = computer
    }
}

struct ComputerLibrary: Codable, Equatable {
    var computers: [SavedComputer] = []
    var activeID: UUID? = nil
    private var version = 1

    init(computers: [SavedComputer] = [], activeID: UUID? = nil) {
        self.computers = computers
        self.activeID = activeID
    }

    var selected: SavedComputer? {
        guard let activeID else { return nil }
        return computers.first { $0.id == activeID }
    }

    // Invalid drafts and a 33rd entry are rejected by validate/save, rather than
    // silently dropped by this deliberately nonthrowing model operation.
    @discardableResult
    mutating func upsert(_ computer: Computer) -> UUID {
        var computer = computer
        if let endpoint = Self.canonicalEndpoint(computer.address) {
            computer.address = endpoint
            if let index = computers.firstIndex(where: { Self.canonicalEndpoint($0.computer.address) == endpoint }) {
                computers[index].computer = computer
                activeID = computers[index].id
                return computers[index].id
            }
        }
        let saved = SavedComputer(computer: computer)
        computers.append(saved)
        activeID = saved.id
        return saved.id
    }

    mutating func remove(id: UUID) {
        computers.removeAll { $0.id == id }
        if activeID == id { activeID = computers.first?.id }
    }

    func validate() throws {
        guard version == 1 else {
            throw ComputerLibraryFailure.invalid("This saved-computer format is not supported. Update Trill before continuing.")
        }
        guard computers.count <= 32 else {
            throw ComputerLibraryFailure.invalid("Save at most 32 computers.")
        }
        var ids = Set<UUID>()
        var endpoints = Set<String>()
        for saved in computers {
            guard ids.insert(saved.id).inserted else {
                throw ComputerLibraryFailure.invalid("Saved computers contain a duplicate identity.")
            }
            guard let endpoint = Self.canonicalEndpoint(saved.computer.address), endpoints.insert(endpoint).inserted else {
                throw ComputerLibraryFailure.invalid("Saved computers contain an invalid or duplicate address.")
            }
            // Keep the same transport policy as APIClient. HTTP is only usable
            // for the existing Debug simulator loopback lab.
            var allowLoopback = false
            #if DEBUG && targetEnvironment(simulator)
            allowLoopback = true
            #endif
            _ = try Computer.validate(address: saved.computer.address, token: saved.computer.token,
                                      fingerprint: saved.computer.fingerprint, allowLoopback: allowLoopback)
        }
        // A nil selection is allowed; an explicit selection must resolve exactly.
        if let activeID, !ids.contains(activeID) {
            throw ComputerLibraryFailure.invalid("The selected computer is not in the saved library.")
        }
    }

    private static func canonicalEndpoint(_ address: String) -> String? {
        guard var parts = URLComponents(string: address.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = parts.scheme?.lowercased(), ["https", "http"].contains(scheme),
              let host = parts.host?.lowercased(), !host.isEmpty,
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.percentEncodedPath.isEmpty || parts.percentEncodedPath == "/" else { return nil }
        if let port = parts.port, !(1...65535).contains(port) { return nil }
        parts.scheme = scheme
        parts.host = host
        // HTTPS defaults to 443; the simulator's HTTP exception defaults to 80.
        if parts.port == (scheme == "https" ? 443 : 80) { parts.port = nil }
        parts.path = ""
        return parts.url?.absoluteString
    }

    private enum CodingKeys: String, CodingKey { case version, computers, activeID }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        version = try container.decode(Int.self, forKey: .version)
        computers = try container.decode([SavedComputer].self, forKey: .computers)
        activeID = try container.decodeIfPresent(UUID.self, forKey: .activeID)
        try validate()
    }

    func encode(to encoder: Encoder) throws {
        try validate()
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(version, forKey: .version)
        try container.encode(computers, forKey: .computers)
        try container.encode(activeID, forKey: .activeID)
    }
}

enum ComputerLibraryStore {
    static func load(account: String = "computer-library") throws -> ComputerLibrary? {
        var query = try itemQuery(account: account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else { throw ComputerLibraryFailure.keychain(status) }
        guard let data = result as? Data else {
            throw ComputerLibraryFailure.invalid("Saved computers could not be read from Keychain.")
        }
        // Decode and validation errors deliberately propagate. Never interpret
        // corruption or restricted Keychain access as an empty/missing library.
        return try JSONDecoder().decode(ComputerLibrary.self, from: data)
    }

    static func save(_ library: ComputerLibrary, account: String = "computer-library") throws {
        try library.validate()
        let data = try JSONEncoder().encode(library)
        let query = try itemQuery(account: account)
        let attributes: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        ]
        // Replace the entire library in one Keychain operation. Never delete
        // before adding: a failed update must leave the existing blob intact.
        let status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            var item = query
            attributes.forEach { item[$0.key] = $0.value }
            let added = SecItemAdd(item as CFDictionary, nil)
            guard added == errSecSuccess else { throw ComputerLibraryFailure.keychain(added) }
        } else if status != errSecSuccess {
            throw ComputerLibraryFailure.keychain(status)
        }
    }

    static func delete(account: String = "computer-library") throws {
        let status = SecItemDelete(try itemQuery(account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw ComputerLibraryFailure.keychain(status)
        }
    }

    private static func itemQuery(account: String) throws -> [String: Any] {
        // This store never reads or overwrites the legacy or pending enrollment.
        guard !account.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !["primary", "pending", "pending-enrollment"].contains(account) else {
            throw ComputerLibraryFailure.invalid("Use a separate Keychain account for saved computers.")
        }
        return [kSecClass as String: kSecClassGenericPassword,
                kSecAttrService as String: PairingStore.service,
                kSecAttrAccount as String: account,
                kSecAttrSynchronizable as String: false]
    }
}

private enum ComputerLibraryFailure: LocalizedError {
    case invalid(String)
    case keychain(OSStatus)

    var errorDescription: String? {
        switch self {
        case .invalid(let message): return message
        case .keychain(let status): return "Couldn’t access saved computers in Keychain (status \(status))."
        }
    }
}
