import Foundation
import Security
import CryptoKit

final class PinnedSessionDelegate: NSObject, URLSessionDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    let fingerprint: String
    private let lock = NSLock()
    private var failure: String?
    var pinFailure: String? { lock.lock(); defer { lock.unlock() }; return failure }
    init(fingerprint: String) { self.fingerprint = CertificateFingerprint.normalized(fingerprint) }
    private func reject(_ message: String) { lock.lock(); failure = message; lock.unlock() }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust, !fingerprint.isEmpty else {
            completionHandler(.performDefaultHandling, nil); return
        }
        guard let cert = SecTrustCopyCertificateChain(trust) as? [SecCertificate], let leaf = cert.first else {
            reject("Your Mac did not provide a readable certificate.")
            completionHandler(.cancelAuthenticationChallenge, nil); return
        }
        let digest = SHA256.hash(data: SecCertificateCopyData(leaf) as Data).map { String(format: "%02x", $0) }.joined()
        guard digest == fingerprint else {
            reject("The certificate does not match this computer's pairing details. Create a new pairing code on your Mac.")
            completionHandler(.cancelAuthenticationChallenge, nil); return
        }
        // The scanned fingerprint establishes this leaf as a trust anchor. Keep
        // URLSession's SSL hostname and validity checks rather than accepting an
        // otherwise-invalid certificate solely because its bytes match.
        guard SecTrustSetAnchorCertificates(trust, [leaf] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
              SecTrustEvaluateWithError(trust, nil) else {
            reject("The paired certificate is expired or does not match this address. Create a new pairing code on your Mac.")
            completionHandler(.cancelAuthenticationChallenge, nil); return
        }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

struct APIRequestError: LocalizedError {
    let status: Int
    let message: String
    var errorDescription: String? { message }
    var isRetryable: Bool { status == 408 || status == 429 || status >= 500 }
}

final class APIClient: @unchecked Sendable {
    private let providerLock = NSLock()
    private var statuses: [ProviderStatus] = []
    var spaceProviders: [ProviderStatus] {
        providerLock.lock(); defer { providerLock.unlock() }
        return statuses.filter { $0.available && $0.createSpace == true }
    }
    var providerWarnings: [String] {
        providerLock.lock(); defer { providerLock.unlock() }
        return statuses.filter { !$0.available }.map { $0.message ?? "\($0.name) is unavailable." }
    }
    private func recordProviders(_ providers: [ProviderStatus]) {
        providerLock.lock(); defer { providerLock.unlock() }
        statuses = providers
    }
    let computer: Computer
    let baseURL: URL
    let session: URLSession
    private let trustDelegate: PinnedSessionDelegate
    init(computer: Computer) throws {
        self.computer = computer
        var localDevelopment = false
        #if DEBUG && targetEnvironment(simulator)
        localDevelopment = true
        #endif
        baseURL = try Computer.validate(address: computer.address, token: computer.token, fingerprint: computer.fingerprint, allowLoopback: localDevelopment)
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        config.timeoutIntervalForResource = 45
        config.urlCache = nil
        trustDelegate = PinnedSessionDelegate(fingerprint: computer.fingerprint)
        session = URLSession(configuration: config, delegate: trustDelegate, delegateQueue: nil)
    }
    func request<T: Decodable>(_ method: String = "GET", _ path: String, body: [String: Any]? = nil, data bodyData: Data? = nil, contentType: String = "application/json", query: [URLQueryItem] = []) async throws -> T {
        var request = URLRequest(url: try resourceURL(path, query: query))
        request.httpMethod = method
        request.setValue("Bearer \(computer.token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let bodyData {
            request.httpBody = bodyData
            request.setValue(contentType, forHTTPHeaderField: "Content-Type")
        } else if let body {
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let data: Data
        let response: URLResponse
        do { (data, response) = try await session.data(for: request) }
        catch { throw TrillError.message(ConnectionDiagnostic.message(for: error, hasPin: !trustDelegate.fingerprint.isEmpty, pinFailure: trustDelegate.pinFailure)) }
        guard let response = response as? HTTPURLResponse else { throw TrillError.message("Your computer returned an unexpected response.") }
        guard (200..<300).contains(response.statusCode) else {
            if response.statusCode == 401 { throw APIRequestError(status: 401, message: "This device's access was rejected. Create a new pairing code on your Mac and pair again.") }
            if response.statusCode == 403 { throw APIRequestError(status: 403, message: "The companion allows read-only access. Enable control on your Mac to continue.") }
            throw APIRequestError(status: response.statusCode, message: (try? JSONDecoder().decode(APIError.self, from: data).error) ?? "Your computer could not complete this request (\(response.statusCode)).")
        }
        return try JSONDecoder().decode(T.self, from: data)
    }
    static func redeem(_ invitation: PairingInvitation, deviceName: String) async throws -> Computer {
        guard invitation.expiresAt > .now else { throw TrillError.message("This pairing code has expired. Create a new one on your Mac.") }
        let draft = Computer(name: invitation.name, address: invitation.address, token: invitation.code, fingerprint: CertificateFingerprint.normalized(invitation.fingerprint))
        let client = try APIClient(computer: draft)
        defer { client.session.finishTasksAndInvalidate() }
        var request = URLRequest(url: client.baseURL.appendingPathComponent("v1/pair"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["code": invitation.code, "name": deviceName])
        let data: Data
        let response: URLResponse
        do { (data, response) = try await client.session.data(for: request) }
        catch { throw TrillError.message(ConnectionDiagnostic.message(for: error, hasPin: !client.trustDelegate.fingerprint.isEmpty, pinFailure: client.trustDelegate.pinFailure)) }
        guard let response = response as? HTTPURLResponse else { throw TrillError.message("Your Mac returned an unexpected response.") }
        guard response.statusCode == 200 || response.statusCode == 201 else {
            if response.statusCode == 403 { throw TrillError.message("This pairing code has expired or has already been used. Create a new code on your Mac.") }
            throw TrillError.message("Your Mac could not complete pairing (\(response.statusCode)).")
        }
        struct Enrollment: Decodable { let deviceId: String; let token: String }
        let enrollment = try JSONDecoder().decode(Enrollment.self, from: data)
        _ = try Computer.validate(address: draft.address, token: enrollment.token, fingerprint: draft.fingerprint)
        return Computer(name: draft.name, address: draft.address, token: enrollment.token, fingerprint: draft.fingerprint)
    }
    func notificationStatus() async throws -> AgentNotificationStatus { try await request("GET", "v1/notifications") }
    func registerNotifications(token: String, environment: String, computerID: UUID, preferences: AgentNotificationPreferences) async throws {
        let _: OKResponse = try await request("PUT", "v1/notifications", body: ["token": token, "environment": environment,
            "computerId": computerID.uuidString, "attention": preferences.attention, "completion": preferences.completion])
    }
    func deleteNotifications() async throws { let _: OKResponse = try await request("DELETE", "v1/notifications") }
    func health() async throws -> Health { try await request("GET", "v1/health") }
    func sessions() async throws -> [RemoteSession] { let r: SessionsResponse = try await request("GET", "v1/sessions"); recordProviders(r.providers ?? []); return r.sessions }
    func projects() async throws -> [Project] { let r: ProjectsResponse = try await request("GET", "v1/projects"); return r.projects }
    func waitForHerdrChange() async throws -> Bool {
        struct Change: Decodable { let changed: Bool }
        let change: Change = try await request("GET", "v1/changes")
        return change.changed
    }
    func closeSession(id: String) async throws { let _: OKResponse = try await request("DELETE", "v1/sessions/\(id)") }
    func rename(id: String, name: String) async throws { let _: OKResponse = try await request("PATCH", "v1/sessions/\(id)", body: ["name": name]) }
    func folders(directory: String? = nil) async throws -> MacFolderListing {
        try await request("GET", "v1/folders", query: directory.flatMap { $0.isEmpty ? nil : [URLQueryItem(name: "path", value: $0)] } ?? [])
    }
    func createTerminal(projectID: String, directory: String = "") async throws {
        var body: [String: Any] = ["projectId": projectID, "kind": "terminal", "useWorktree": false]
        if !directory.isEmpty { body["directory"] = directory }
        let _: OKResponse = try await request("POST", "v1/sessions", body: body)
    }
    func createSpace(providerID: String, name: String, directory: String) async throws {
        let _: OKResponse = try await request("POST", "v1/spaces", body: ["providerId": providerID, "name": name, "directory": directory])
    }
    func createWorktree(projectID: String, name: String, branch: String) async throws {
        let _: OKResponse = try await request("POST", "v1/sessions", body: ["projectId": projectID, "kind": "worktree", "useWorktree": true, "name": name, "branch": branch])
    }
    func attach(id: String, cols: Int, rows: Int) async throws -> TerminalLease {
        try await request("POST", "v1/sessions/\(id)/terminal", body: ["cols": cols, "rows": rows])
    }
    func herdrTransport(lease: TerminalLease) -> HerdrTerminalTransport {
        var components = URLComponents(url: baseURL.appendingPathComponent("v1/leases/\(lease.leaseId)/terminal"), resolvingAgainstBaseURL: false)!
        components.scheme = baseURL.scheme == "https" ? "wss" : "ws"
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(computer.token)", forHTTPHeaderField: "Authorization")
        let configuration = URLSessionConfiguration.ephemeral
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 20
        configuration.timeoutIntervalForResource = 12 * 60 * 60
        let streamSession = URLSession(configuration: configuration, delegate: trustDelegate, delegateQueue: nil)
        return HerdrTerminalTransport(request: request, session: streamSession)
    }
    func uploadPhoto(id: String, jpeg: Data) async throws -> PhotoAttachment {
        try await request("POST", "v1/sessions/\(id)/attachments", data: jpeg, contentType: "image/jpeg")
    }
    private func resourceURL(_ path: String, query: [URLQueryItem] = []) throws -> URL {
        guard var parts = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false) else {
            throw TrillError.message("This file address is invalid.")
        }
        if !query.isEmpty { parts.queryItems = query }
        guard let url = parts.url else { throw TrillError.message("This file address is invalid.") }
        return url
    }
    func workspaceFiles(sessionID: String, path: String, photos: Bool) async throws -> WorkspaceListing {
        try await request("GET", "v1/sessions/\(sessionID)/\(photos ? "attachments" : "files")", query: [URLQueryItem(name: "path", value: path)])
    }
    func workspaceFile(sessionID: String, path: String, photos: Bool) async throws -> WorkspaceFile {
        let url = try resourceURL("v1/sessions/\(sessionID)/\(photos ? "attachment" : "file")", query: [URLQueryItem(name: "path", value: path)])
        return try await workspaceFileResponse(url)
    }
    func workspaceStaticFile(sessionID: String, root: String, path: String) async throws -> WorkspaceFile {
        let url = try resourceURL("v1/sessions/\(sessionID)/static-file", query: [URLQueryItem(name: "root", value: root), URLQueryItem(name: "path", value: path)])
        return try await workspaceFileResponse(url)
    }
    private func workspaceFileResponse(_ url: URL) async throws -> WorkspaceFile {
        var request = URLRequest(url: url)
        request.setValue("Bearer \(computer.token)", forHTTPHeaderField: "Authorization")
        let data: Data
        let response: URLResponse
        do { (data, response) = try await session.data(for: request) }
        catch { throw TrillError.message(ConnectionDiagnostic.message(for: error, hasPin: !trustDelegate.fingerprint.isEmpty, pinFailure: trustDelegate.pinFailure)) }
        guard let response = response as? HTTPURLResponse else { throw TrillError.message("Your computer returned an unexpected response.") }
        guard response.statusCode == 200 else {
            throw APIRequestError(status: response.statusCode, message: (try? JSONDecoder().decode(APIError.self, from: data).error) ?? "This file could not be opened.")
        }
        guard data.count <= 10 * 1024 * 1024 else { throw TrillError.message("This file is too large to preview.") }
        return WorkspaceFile(data: data, contentType: response.mimeType ?? "application/octet-stream")
    }
    /// Download through the same pinned, authenticated origin. AVPlayer only sees
    /// a private local file, never a companion URL or bearer credential.
    func workspaceVideo(sessionID: String, path: String,
                        progress: @escaping @Sendable (Double?) -> Void) async throws -> WorkspaceVideoFile {
        let url = try resourceURL("v1/sessions/\(sessionID)/video", query: [URLQueryItem(name: "path", value: path)])
        var request = URLRequest(url: url)
        request.setValue("Bearer \(computer.token)", forHTTPHeaderField: "Authorization")
        let configuration = URLSessionConfiguration.ephemeral
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 30
        configuration.timeoutIntervalForResource = 300
        let delegate = WorkspaceVideoDownloadDelegate(trust: trustDelegate, progress: progress)
        do { return try await delegate.download(request: request, configuration: configuration) }
        catch let error as URLError {
            if Task.isCancelled { throw CancellationError() }
            throw TrillError.message(ConnectionDiagnostic.message(for: error, hasPin: !trustDelegate.fingerprint.isEmpty, pinFailure: trustDelegate.pinFailure))
        }
    }

    func openPreview(sessionID: String, port: Int) async throws -> PreviewGrant {
        try await request("POST", "v1/previews", body: ["sessionId": sessionID, "port": port])
    }
    func closePreview(id: String) async throws {
        let _: OKResponse = try await request("DELETE", "v1/previews/\(id)")
    }
    func release(_ id: String) async throws { let _: OKResponse = try await request("DELETE", "v1/leases/\(id)") }
}

enum PairingStore {
    static let service = "io.bcook.trill.computer"
    static func save(_ computer: Computer, account: String = "primary") throws {
        let data = try JSONEncoder().encode(computer)
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
        let attributes: [String: Any] = [kSecValueData as String: data, kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
        let updated = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updated == errSecItemNotFound {
            var item = query; attributes.forEach { item[$0.key] = $0.value }
            guard SecItemAdd(item as CFDictionary, nil) == errSecSuccess else { throw TrillError.message("Couldn't save pairing in Keychain.") }; return
        }
        guard updated == errSecSuccess else { throw TrillError.message("Couldn't update pairing in Keychain.") }
    }
    static func load(account: String = "primary") -> Computer? {
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess, let data = result as? Data else { return nil }
        return try? JSONDecoder().decode(Computer.self, from: data)
    }
    static func delete(account: String = "primary") throws {
        let code = SecItemDelete([kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account] as CFDictionary)
        guard code == errSecSuccess || code == errSecItemNotFound else { throw TrillError.message("Couldn't remove pairing from Keychain.") }
    }
}

/// A delegate-driven download is deliberate: the async download(for:) API does
/// not deliver didWriteData callbacks. Cancellation is bridged to the real task.
final class WorkspaceVideoDownloadDelegate: NSObject, URLSessionDownloadDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var oversized = false
    private var cancelled = false
    private var task: URLSessionDownloadTask?
    private var session: URLSession?
    private var continuation: CheckedContinuation<WorkspaceVideoFile, Error>?
    private var result: Result<WorkspaceVideoFile, Error>?
    private let trust: PinnedSessionDelegate
    var exceededLimit: Bool { lock.lock(); defer { lock.unlock() }; return oversized }
    let progress: @Sendable (Double?) -> Void
    init(trust: PinnedSessionDelegate = PinnedSessionDelegate(fingerprint: ""), progress: @escaping @Sendable (Double?) -> Void) {
        self.trust = trust
        self.progress = progress
    }
    func download(request: URLRequest, configuration: URLSessionConfiguration) async throws -> WorkspaceVideoFile {
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                lock.lock()
                if cancelled {
                    lock.unlock()
                    continuation.resume(throwing: CancellationError())
                    return
                }
                self.continuation = continuation
                let session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
                self.session = session
                let task = session.downloadTask(with: request)
                self.task = task
                lock.unlock()
                task.resume()
            }
        } onCancel: { self.cancel() }
    }
    private func cancel() {
        lock.lock()
        cancelled = true
        let task = task
        lock.unlock()
        task?.cancel()
    }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        trust.urlSession(session, didReceive: challenge, completionHandler: completionHandler)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData bytesWritten: Int64,
                    totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) {
        if totalBytesWritten > WorkspaceVideoFile.maximumSize || totalBytesExpectedToWrite > WorkspaceVideoFile.maximumSize {
            lock.lock(); oversized = true; lock.unlock()
            downloadTask.cancel()
            return
        }
        progress(totalBytesExpectedToWrite > 0 ? min(1, Double(totalBytesWritten) / Double(totalBytesExpectedToWrite)) : nil)
    }
    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        let result = Result { () throws -> WorkspaceVideoFile in
            guard let response = downloadTask.response as? HTTPURLResponse else {
                throw TrillError.message("Your computer returned an unexpected response.")
            }
            guard response.statusCode == 200 else {
                let message: String
                if response.statusCode == 404 { message = "This video is unavailable. If it still exists, update Trill on your computer to enable video previews." }
                else if response.statusCode == 413 { message = WorkspaceVideoFile.sizeMessage }
                else {
                    let handle = try FileHandle(forReadingFrom: location)
                    defer { try? handle.close() }
                    let data = try handle.read(upToCount: 8192) ?? Data()
                    message = (try? JSONDecoder().decode(APIError.self, from: data).error) ?? "This video could not be opened."
                }
                throw APIRequestError(status: response.statusCode, message: message)
            }
            // The system deletes location after this callback; move it now.
            return try WorkspaceVideoFile(download: location, response: response)
        }
        lock.lock(); self.result = result; lock.unlock()
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        lock.lock()
        let continuation = continuation
        let outcome: Result<WorkspaceVideoFile, Error>
        if cancelled { outcome = .failure(CancellationError()) }
        else if oversized { outcome = .failure(TrillError.message(WorkspaceVideoFile.sizeMessage)) }
        else if let error { outcome = .failure(error) }
        else { outcome = result ?? .failure(TrillError.message("The video download did not finish.")) }
        self.continuation = nil
        self.result = nil
        self.task = nil
        self.session = nil
        lock.unlock()
        session.finishTasksAndInvalidate()
        continuation?.resume(with: outcome)
    }
}

/// Lifetime ownership removes the download on close, failed decoding, stale
/// completion, or cancellation. UUID names never reuse a remote file path.
final class WorkspaceVideoFile: @unchecked Sendable {
    static let maximumSize: Int64 = 100 * 1024 * 1024
    static let sizeMessage = "Video previews support files up to 100 MiB."
    let url: URL
    private let directory: URL
    init(download: URL, response: HTTPURLResponse) throws {
        let mime = response.mimeType?.lowercased()
        guard ["video/mp4", "video/quicktime", "video/x-m4v"].contains(mime) else {
            throw TrillError.message("This file is not a supported video. Use an MP4, MOV, or M4V file.")
        }
        let size = try download.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0
        guard size > 0 else { throw TrillError.message("This video is empty.") }
        guard size <= Self.maximumSize else { throw TrillError.message(Self.sizeMessage) }
        guard response.expectedContentLength < 0 || response.expectedContentLength == Int64(size) else {
            throw TrillError.message("The video download was incomplete. Try opening it again.")
        }
        directory = FileManager.default.temporaryDirectory.appendingPathComponent("trill-video-" + UUID().uuidString, isDirectory: true)
        url = directory.appendingPathComponent(mime == "video/quicktime" ? "preview.mov" : "preview.mp4")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false,
            attributes: [.posixPermissions: 0o700, .protectionKey: FileProtectionType.complete])
        do {
            try FileManager.default.moveItem(at: download, to: url)
            try FileManager.default.setAttributes([.posixPermissions: 0o600, .protectionKey: FileProtectionType.complete], ofItemAtPath: url.path)
        } catch {
            try? FileManager.default.removeItem(at: directory)
            throw error
        }
    }
    deinit { try? FileManager.default.removeItem(at: directory) }
}
