import Foundation

struct SessionCapabilities: Codable, Hashable {
    var rename = false
    var stop = false
    var archive = false
    var resume = false
    var createAgent = false
    var createTerminal = false
    var worktree = false
    var files = false
}
struct ProviderStatus: Decodable {
    let id: String
    let name: String
    let available: Bool
    let message: String?
    var createSpace: Bool? = nil
}
enum AgentActivityGroup: Int, CaseIterable, Identifiable {
    case needsAttention, working, idle, other
    var id: Self { self }
    var title: String {
        switch self {
        case .needsAttention: "Needs attention"
        case .working: "Working"
        case .idle: "Idle"
        case .other: "Other agents"
        }
    }
}

struct RemoteSession: Codable, Identifiable, Hashable {
    var provider: String? = nil
    var providerName: String? = nil
    var capabilities: SessionCapabilities? = nil
    let id: String
    var name: String
    let projectId: String
    let projectName: String
    let status: String
    let activityState: String
    let agent: String
    let branch: String
    let updatedAt: String
    let attachable: Bool
    let resumable: Bool
    var archived: Bool? = nil

    // Match the sidecar's endedStatus contract. "finished" may still be live.
    var isArchived: Bool { archived == true || ["completed", "stopped", "failed"].contains(status) }
    func matchesSearch(_ search: String) -> Bool {
        let query = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return query.isEmpty || "\(name) \(projectName) \(branch) \(agent) \(status)".localizedCaseInsensitiveContains(query)
    }

    var hasDetectedAgent: Bool {
        let agentKind = agent.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return !["", "shell", "terminal", "none", "unknown", "sh", "bash", "zsh", "fish", "pwsh", "powershell"].contains(agentKind)
    }
    var agentActivityGroup: AgentActivityGroup {
        if needsAttention { return .needsAttention }
        if activityState == "working" || status == "working" { return .working }
        if ["idle", "done", "ready", "finished"].contains(activityState) { return .idle }
        return .other
    }

    var needsAttention: Bool { ["waiting", "needs_input", "waiting_for_input", "error", "failed"].contains(activityState) || ["waiting", "error", "failed"].contains(status) }
    var statusLabel: String {
        if needsAttention { return "Needs attention" }
        if provider == "herdr" && status == "running" && activityState == "idle" { return "Idle" }
        if status == "running" { return activityState == "working" ? "Working" : "Running" }
        return status.replacingOccurrences(of: "_", with: " ").capitalized
    }
    var displayAgent: String { agent.isEmpty ? "Terminal" : agent.capitalized }
}
struct Project: Codable, Identifiable, Hashable {
    var parentId: String? = nil
    var branch: String? = nil
    var isWorktree: Bool? = nil
    var focused: Bool? = nil
    var workspaceOrder: Int? = nil
    var provider: String? = nil
    var providerName: String? = nil
    var capabilities: SessionCapabilities? = nil
    var canCreate: Bool { capabilities.map { $0.createAgent || $0.createTerminal } ?? true }
    let id: String
    let name: String
    let path: String
    func matchesSearch(_ search: String) -> Bool {
        let query = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return query.isEmpty || "\(name) \(branch ?? "")".localizedCaseInsensitiveContains(query)
    }
    var displayName: String { name.isEmpty ? (id.isEmpty ? "Unassigned project" : id) : name }
}
struct SessionsResponse: Decodable { let sessions: [RemoteSession]; var providers: [ProviderStatus]? = nil }
struct ProjectsResponse: Decodable { let projects: [Project] }
struct Health: Decodable {
    let name: String
    let version: String
    let readOnly: Bool
    var sessionManagement: Bool? = nil
}
struct MoshLease: Decodable { let host: String; let port: UInt16; let key: String; let leaseId: String }
struct OKResponse: Decodable { let ok: Bool }
struct APIError: Decodable { let error: String }

struct Computer: Codable, Equatable {
    var name: String
    var address: String
    var token: String
    var fingerprint: String

    static func validate(address: String, token: String, fingerprint: String, allowLoopback: Bool = false) throws -> URL {
        guard let url = URL(string: address.trimmingCharacters(in: .whitespacesAndNewlines)),
              let host = url.host, !host.isEmpty,
              url.user == nil, url.password == nil, url.query == nil, url.fragment == nil,
              url.path.isEmpty || url.path == "/" else { throw TrillError.message("Enter the companion's HTTPS address without a path or login details.") }
        let loopback = ["localhost", "127.0.0.1", "[::1]", "::1"].contains(host)
        guard url.scheme == "https" || (allowLoopback && loopback && url.scheme == "http") else {
            throw TrillError.message("Use HTTPS to protect your computer's connection.")
        }
        guard token.count >= 32, !token.contains(where: { $0.isWhitespace }) else { throw TrillError.message("Enter the complete pairing token from your computer.") }
        let pin = CertificateFingerprint.normalized(fingerprint)
        guard pin.isEmpty || (pin.count == 64 && pin.allSatisfy({ $0.isHexDigit })) else { throw TrillError.message("The certificate fingerprint must contain 64 hexadecimal characters.") }
        return url
    }
}

// Thrown only before a creation request is dispatched, so retrying is safe.
enum SessionCreationPreflightError: LocalizedError, Equatable {
    case unsupportedProject
    case unavailable

    var errorDescription: String? {
        switch self {
        case .unsupportedProject: "This project does not support the requested session type through Trill."
        case .unavailable: "Wait for the current operation to finish before creating a session."
        }
    }
}

enum TrillError: LocalizedError {
    case message(String)
    var errorDescription: String? { switch self { case .message(let text): text } }
}

struct PhotoAttachment: Decodable {
    let path: String
    let fileName: String
    let contentType: String
    let byteCount: Int

    // Safe as a literal file argument in a shell and readable in an agent prompt.
    var terminalPath: String? {
        guard path.hasPrefix("/"), !path.unicodeScalars.contains(where: { $0.value < 32 || (127...159).contains($0.value) }) else { return nil }
        return "'" + path.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
    }
}

/// Immutable presentation identity; execution resolves fresh metadata by ID.
struct SessionPrompt: Identifiable {
    enum Action: String { case archive, stop, rename }
    let id = UUID()
    let action: Action
    let sessionID: String
    let computerID: UUID?
    let sessionName: String
    let computerName: String
    var title: String { "\(action.rawValue.capitalized) “\(sessionName)”?" }
    var button: String { action == .rename ? "Save" : "\(action.rawValue.capitalized) session" }
    var message: String {
        switch action {
        case .archive: "Moves this session on \(computerName) to Projects → Archived. Archiving does not stop a running session."
        case .stop: "Stops the session on \(computerName). Choose Disconnect from the session menu to leave it running."
        case .rename: "Choose a name for this session on \(computerName)."
        }
    }
}
