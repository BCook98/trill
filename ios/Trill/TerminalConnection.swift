import SwiftUI
import SwiftTerm

// SwiftTerm emits terminal protocol replies through the same delegate as user
// input. Keep its terminal-core callback separate so a query cannot consume Shift
// and turn the user's next Shift+Return into a plain submit.
final class TrillTerminalView: TerminalView {
    // The remote shell can have editable text even when UIKit's local input
    // shadow is empty (for example after reconnecting or using Write).
    // Keep Backspace enabled so holding it continues sending deletions.
    override var hasText: Bool { true }

    var hostReply: (([UInt8]) -> Void)?
    nonisolated override func send(source: Terminal, data: ArraySlice<UInt8>) {
        let bytes = Array(data)
        DispatchQueue.main.async { [weak self] in self?.hostReply?(bytes) }
    }
}

enum TerminalInputOperation: Sendable, Equatable { case keystrokes(Data), resize(cols: Int32, rows: Int32) }
enum TerminalOutputOperation: Sendable, Equatable { case hostBytes(Data), resize(cols: Int32, rows: Int32), echoAck(UInt64) }
enum TerminalTransportFailure: Error, Sendable, Equatable { case transportFailure(String), protocolViolation(String) }
enum TerminalTransportState: Sendable, Equatable { case idle, starting, running, suspended, stopped, failed(TerminalTransportFailure) }
enum TerminalTransportError: Error { case notStarted }

// Tests supply an isolated implementation of the HTTPS terminal stream.
protocol TerminalTransport: AnyObject, Sendable {
    var state: TerminalTransportState { get async }
    var peerResponseCount: UInt64 { get async }
    func resumeFromApplicationBackground() async throws
    func start() async throws
    func stop() async
    func prepareForApplicationBackground() async throws
    func enqueue(_ op: TerminalInputOperation) async throws
    func drainHostOps() async -> [TerminalOutputOperation]
    func hostActivityStream() async -> AsyncStream<Void>
}

protocol RemoteTerminalScrolling: TerminalTransport {
    func scroll(lines: Int, column: Int, row: Int) async throws
}

@MainActor @Observable
final class TerminalConnection: NSObject, TerminalViewDelegate {
    enum Phase: Equatable { case disconnected, connecting, reconnecting, resuming, connected, suspended, failed(String) }
    var phase: Phase = .disconnected
    var title = ""
    var controlHeld = false
    var shiftHeld = false
    var draftText = "" {
        didSet { if draftText != oldValue { draftRevision = UUID() } }
    }
    struct InputEnvelope {
        let operation: TerminalInputOperation
        let draftID: UUID?
        let streamID: UUID
    }
    private struct PendingDraft {
        let id: UUID
        let revision: UUID
    }
    private var draftRevision = UUID()
    private var pendingDraft: PendingDraft?
    var draftInsertionPending: Bool { pendingDraft != nil }
    var pendingLink: URL?
    var cleanupWarning: String?
    var inputNotice: String?
    // UI progress is separate from Phase so existing lifecycle consumers stay stable.
    private(set) var recoveryMessage: String?
    private var latestInterruption: String?
    var canRetryRecovery: Bool {
        wantsForeground && target != nil && retry != nil && phase == .reconnecting
    }
    let terminal: TrillTerminalView
    struct Environment {
        var attach: (String, Int, Int) async throws -> TerminalLease
        var release: (String) async throws -> Void
        var makeTransport: (TerminalLease) -> any TerminalTransport
        var retrySleep: (Duration) async throws -> Void = { try await Task.sleep(for: $0) }
        var monitorInterval: Duration = .seconds(1)
        var responsePollLimit = 15
        var resumePollLimit = 3
        var resumeStartupTimeout: Duration = .seconds(3)
        var returnsControlWhenHidden = false
    }
    private struct Target {
        let sessionID: String
        let environment: Environment
    }
    // User intent survives transport failure, but never an explicit disconnect.
    private var target: Target?
    private var retry: Task<Void, Never>?
    private var retryCount = 0
    private var client: (any TerminalTransport)?
    private var lease: TerminalLease?
    private var environment: Environment?
    private var reader: Task<Void, Never>?
    private var drainingReader: Task<Void, Never>?
    private var resumeDeadline: Task<Void, Never>?
    private var pendingResume: (id: UUID, client: any TerminalTransport)?
    private var monitor: Task<Void, Never>?
    private var sender: Task<Void, Never>?
    private var input: AsyncStream<InputEnvelope>.Continuation?
    private var inputID = UUID()
    private var transitionTail: Task<Void, Never>?
    private var generation = UUID()
    private var wantsForeground = true
    private var applicationIsForeground = true
    private var visibleTerminalSurfaceID: UUID?
    var returnsControlWhenHidden: Bool { target?.environment.returnsControlWhenHidden == true }
    var terminalIsVisible: Bool { visibleTerminalSurfaceID != nil }
    private var receivedHostOp = false
    private var attachmentOutcomeUnconfirmed = false
    private var cols = 80
    private var rows = 24

    override init() {
        terminal = TrillTerminalView(frame: CGRect(x: 0, y: 0, width: 720, height: 440),
                                font: .monospacedSystemFont(ofSize: 14, weight: .regular))
        super.init()
        terminal.terminalDelegate = self
        terminal.activatesLinksOnTap = true
        terminal.linkHighlightMode = .always
        terminal.hostReply = { [weak self] bytes in
            guard let self, self.connected else { return }
            self.queueInput(.keystrokes(Data(bytes)))
        }
        terminal.nativeBackgroundColor = .black
        terminal.nativeForegroundColor = UIColor(white: 0.92, alpha: 1)
        terminal.caretColor = .systemMint
        terminal.minimumContrastRatio = 4.5
        terminal.inputAccessoryView = nil
        terminal.accessibilityLabel = "Remote terminal"
        terminal.accessibilityIdentifier = "remote-terminal"
        terminal.isUserInteractionEnabled = false
    }
    // Transport-only test seam: supplies a real input stream without creating
    // a terminal stream or starting any networking. Connection state stays explicit.
    convenience init(inputContinuation: AsyncStream<InputEnvelope>.Continuation) {
        self.init()
        input = inputContinuation
    }

    // Text validation is independent of connection availability. insertDraft()
    // additionally requires a connected transport with a live continuation.
    var draftValidationMessage: String? {
        if draftInsertionPending { return "Trill is sending this text to the terminal." }
        return validateDraft(normalizedDraftText,
                             bracketed: terminal.terminalInputStateSnapshot()?.bracketedPasteMode == true)
    }

    // Normalize Windows line endings only. A lone CR remains a rejected control
    // character so it can never become an unbracketed Return. The 64 KiB limit
    // applies to the original UTF-8 draft, before normalization or paste framing.
    private var normalizedDraftText: String {
        draftText.replacingOccurrences(of: "\r\n", with: "\n")
    }

    private func validateDraft(_ text: String, bracketed: Bool) -> String? {
        guard !text.isEmpty else { return "Write or dictate some text first." }
        guard draftText.utf8.count <= 64 * 1024 else { return "This draft is too long. Insert it in smaller parts." }
        for scalar in text.unicodeScalars {
            let value = scalar.value
            if (value < 0x20 && value != 0x09 && value != 0x0a) || (0x7f...0x9f).contains(value) {
                return "Remove terminal control characters before inserting."
            }
        }
        if !bracketed && text.unicodeScalars.contains(where: { $0.value == 0x09 || $0.value == 0x0a }) {
            return "This terminal accepts one line at a time. Remove tabs and line breaks before inserting."
        }
        guard !controlHeld, !shiftHeld else { return "Turn off Ctrl and Shift before inserting text." }
        return nil
    }

    @discardableResult
    func insertDraft() -> Bool {
        guard connected, let input, !draftInsertionPending else { return false }
        let text = normalizedDraftText
        let bracketed = terminal.terminalInputStateSnapshot()?.bracketedPasteMode == true
        guard validateDraft(text, bracketed: bracketed) == nil else { return false }
        var bytes = Data()
        if bracketed { bytes.append(contentsOf: "\u{1b}[200~".utf8) }
        bytes.append(contentsOf: text.utf8)
        if bracketed { bytes.append(contentsOf: "\u{1b}[201~".utf8) }
        // One synchronous operation, with no SwiftTerm paste callback that could
        // arrive after reconnect. The sender still checks its connection generation.
        // Acceptance means queued locally, not acknowledged by the remote program.
        // Keep the draft until transport handoff, and leave any intervening edits alone.
        let pending = PendingDraft(id: UUID(), revision: draftRevision)
        pendingDraft = pending
        let envelope = InputEnvelope(operation: .keystrokes(bytes), draftID: pending.id, streamID: inputID)
        guard case .enqueued = input.yield(envelope) else { pendingDraft = nil; return false }
        return true
    }

    // The sender and transport-only tests share the same handoff boundary.
    // Stream and draft IDs make late completions harmless after cancellation/retry.
    func handoff(_ envelope: InputEnvelope, enqueue: (TerminalInputOperation) async throws -> Void) async throws {
        guard !Task.isCancelled, envelope.streamID == inputID, input != nil else { return }
        if let id = envelope.draftID, pendingDraft?.id != id { return }
        do { try await enqueue(envelope.operation) }
        catch {
            if let id = envelope.draftID, pendingDraft?.id == id { pendingDraft = nil }
            throw error
        }
        guard !Task.isCancelled, envelope.streamID == inputID,
              let pending = pendingDraft, pending.id == envelope.draftID else { return }
        pendingDraft = nil
        if draftRevision == pending.revision { draftText = "" }
    }

    private func queueInput(_ operation: TerminalInputOperation) {
        input?.yield(InputEnvelope(operation: operation, draftID: nil, streamID: inputID))
    }

    var connected: Bool { phase == .connected }
    var isOpen: Bool { switch phase { case .connected, .connecting, .reconnecting, .resuming, .suspended: true; default: false } }
    var phaseLabel: String {
        switch phase {
        case .disconnected: "Disconnected"
        case .connecting: "Connecting…"
        case .reconnecting: "Reconnecting…"
        case .connected: "Herdr · encrypted"
        case .resuming: "Resuming…"
        case .suspended: returnsControlWhenHidden ? "Paused" : "Paused in background"
        case .failed: "Connection interrupted"
        }
    }
    var transportLabel: String { "Herdr · HTTPS" }
    func connect(sessionID: String, api: APIClient) async {
        await connect(sessionID: sessionID, environment: Environment(
            attach: { id, cols, rows in
                // Never reopen an archived/ended session as a side effect of recovery.
                let sessions = try await api.sessions()
                guard let session = sessions.first(where: { $0.id == id }), !session.isArchived, session.attachable else {
                    throw APIRequestError(status: 409, message: "This terminal is no longer active. Open or create a terminal in Herdr.")
                }
                guard session.provider == "herdr", session.capabilities?.terminalStream == true else {
                    throw APIRequestError(status: 409, message: "Update the Mac companion to use Trill’s Herdr terminal stream.")
                }
                return try await api.attach(id: id, cols: cols, rows: rows)
            },
            release: { try await api.release($0) },
            makeTransport: { api.herdrTransport(lease: $0) },
            returnsControlWhenHidden: true))
    }

    func connect(sessionID: String, environment: Environment) async {
        guard !isOpen else { return }
        target = Target(sessionID: sessionID, environment: environment)
        wantsForeground = applicationIsForeground && (!environment.returnsControlWhenHidden || terminalIsVisible)
        if !wantsForeground { phase = .suspended }
        latestInterruption = nil
        retryCount = 0
        await beginConnection(recovering: false)?.value
    }

    @discardableResult
    private func beginConnection(recovering: Bool) -> Task<Void, Never>? {
        guard wantsForeground, let target else { return nil }
        cancelRetry()
        let attempt = UUID(); generation = attempt
        stopIO()
        phase = recovering ? .reconnecting : .connecting
        showRecoveryProgress(recovering ? "Reconnecting to the same terminal…" : "Connecting to your terminal…")
        return enqueueTransition { [self] in
            await teardownResources()
            guard generation == attempt, wantsForeground else { return }
            environment = target.environment
            receivedHostOp = false
            showRecoveryProgress("Contacting your Mac…")
            do {
                // Own even a late result so disconnect/background cannot leak its lease.
                attachmentOutcomeUnconfirmed = target.environment.returnsControlWhenHidden
                lease = try await target.environment.attach(target.sessionID,
                    min(500, max(20, cols)), min(200, max(5, rows)))
                attachmentOutcomeUnconfirmed = false
                guard generation == attempt else { await teardownResources(); return }
                guard let lease else { return }
                let session = target.environment.makeTransport(lease)
                client = session
                if wantsForeground { try await activate(session, attempt: attempt) }
                else { phase = .suspended }
            } catch {
                if error is APIRequestError { attachmentOutcomeUnconfirmed = false }
                guard generation == attempt else { await teardownResources(); return }
                // Re-pairing, read-only, missing/ended sessions need user action.
                if let error = error as? APIRequestError, !error.isRetryable { self.target = nil }
                showFailure(error.localizedDescription)
                await teardownResources()
                guard generation == attempt else { return }
                scheduleRetry()
            }
        }
    }

    // Only skip an existing backoff. An attach or cleanup already in progress
    // cannot be duplicated by repeated taps or a stale UI action.
    func retryRecoveryNow() async {
        guard canRetryRecovery else { return }
        await beginConnection(recovering: true)?.value
    }

    private func showRecoveryProgress(_ progress: String) {
        recoveryMessage = [latestInterruption, progress].compactMap { $0 }.joined(separator: " ")
    }

    private func cancelRetry() { retry?.cancel(); retry = nil }

    private func scheduleRetry() {
        guard wantsForeground, let target, retry == nil else { return }
        let attempt = generation
        let delay = [1, 2, 4, 8, 15, 30][min(retryCount, 5)]
        retryCount = min(retryCount + 1, 5)
        phase = .reconnecting
        showRecoveryProgress("Waiting \(delay) \(delay == 1 ? "second" : "seconds") before trying again.")
        retry = Task { [weak self] in
            do { try await target.environment.retrySleep(.seconds(delay)) } catch { return }
            guard !Task.isCancelled, let self, self.generation == attempt, self.wantsForeground else { return }
            self.retry = nil
            self.beginConnection(recovering: true)
        }
    }

    @discardableResult
    private func enqueueTransition(_ work: @escaping @MainActor () async -> Void) -> Task<Void, Never> {
        let previous = transitionTail
        let task = Task { @MainActor in
            await previous?.value
            await work()
        }
        transitionTail = task
        return task
    }

    private func showFailure(_ message: String) {
        latestInterruption = message
        recoveryMessage = message
        // An in-flight bootstrap or pause can fail after the app locks. Preserve
        // recovery intent without presenting a foreground failure or starting retries.
        if !wantsForeground, target != nil {
            phase = .suspended
            showRecoveryProgress("Trill will reconnect when you return.")
        } else { phase = .failed(message) }
        terminal.isUserInteractionEnabled = false
        _ = terminal.resignFirstResponder()
        // Keep SwiftTerm's last screen, but never keep the transport or lease.
    }

    private func fail(_ message: String, attempt: UUID) {
        guard generation == attempt, isOpen else { return }
        generation = UUID()
        stopIO()
        showFailure(message)
        let failedGeneration = generation
        enqueueTransition { [self] in
            await teardownResources()
            guard generation == failedGeneration else { return }
            scheduleRetry()
        }
    }

    private func stopIO() {
        terminal.remoteScrollHandler = nil
        if let reader { drainingReader = reader }
        reader?.cancel(); monitor?.cancel(); sender?.cancel()
        resumeDeadline?.cancel(); resumeDeadline = nil
        if let pendingResume {
            self.pendingResume = nil
            // Do not leave a startup blocking transitionTail after backgrounding
            // or explicit disconnect cancels its foreground watchdog.
            Task { await pendingResume.client.stop() }
        }
        reader = nil; monitor = nil; sender = nil
        input?.finish(); input = nil
        inputID = UUID(); pendingDraft = nil
        controlHeld = false; shiftHeld = false
        _ = terminal.resignFirstResponder()
        terminal.isUserInteractionEnabled = false
    }

    private func teardownResources() async {
        stopIO()
        let oldClient = client; client = nil
        let oldLease = lease; lease = nil
        let oldEnvironment = environment; environment = nil
        if let oldClient { await oldClient.stop() }
        if let oldLease, let oldEnvironment {
            do { try await oldEnvironment.release(oldLease.leaseId); cleanupWarning = nil }
            catch let error as APIRequestError where error.status == 404 { cleanupWarning = nil }
            catch { cleanupWarning = "Trill could not confirm that the previous terminal connection on your Mac closed." }
        } else if attachmentOutcomeUnconfirmed {
            // An interrupted POST may have created a controller even though no
            // lease ID reached the phone. Never claim a confirmed handoff here.
            cleanupWarning = "Trill could not confirm that the phone attachment on your Mac closed. Check the connection before returning to desktop work."
        }
    }

    func disconnect() async {
        target = nil
        cancelRetry()
        retryCount = 0
        generation = UUID()
        stopIO()
        phase = .disconnected
        latestInterruption = nil
        recoveryMessage = nil
        await enqueueTransition { [self] in await teardownResources() }.value
    }

    func terminalDidAppear(surfaceID: UUID) {
        visibleTerminalSurfaceID = surfaceID
        guard returnsControlWhenHidden, applicationIsForeground else { return }
        resumeTransport()
    }

    func terminalDidDisappear(surfaceID: UUID) {
        // A delayed disappearance from an old SwiftUI presentation must not
        // hand off a newer presentation of this same retained connection.
        guard visibleTerminalSurfaceID == surfaceID else { return }
        visibleTerminalSurfaceID = nil
        if returnsControlWhenHidden { suspendTransport() }
    }

    func background() async {
        applicationIsForeground = false
        let handoff = suspendTransport()
        await handoff?.value
    }

    @discardableResult
    private func suspendTransport() -> Task<Void, Never>? {
        wantsForeground = false
        cancelRetry()
        // Failure cleanup may still be awaiting DELETE with Phase.failed while
        // recovery intent is live. Locking must suspend that path too.
        guard isOpen || target != nil else { return nil }
        if returnsControlWhenHidden {
            // Invalidate an in-flight attach too. Its late lease is still owned
            // and released by the serialized transition before any new attach.
            let handoffGeneration = UUID(); generation = handoffGeneration
            stopIO()
            phase = .suspended
            showRecoveryProgress("Returning terminal control to your Mac…")
            return enqueueTransition { [self] in
                await teardownResources()
                guard generation == handoffGeneration, !wantsForeground else { return }
                recoveryMessage = cleanupWarning ?? "Control returned to your Mac. Trill will reconnect when you return."
            }
        }
        let attempt = generation
        stopIO()
        phase = .suspended
        showRecoveryProgress("Connection paused. Trill will resume when you return.")
        // Return after recording the intent so AppStore can pause every open
        // connection even if one bootstrap is still waiting on the network.
        enqueueTransition { [self] in
            guard generation == attempt, !wantsForeground, let client else { return }
            // Bootstrap may have returned a lease without opening the stream yet.
            guard case .running = await client.state else { return }
            do { _ = try await client.prepareForApplicationBackground() }
            catch { fail("Trill could not pause the connection safely.", attempt: attempt) }
        }
        return nil
    }

    func foreground() async {
        applicationIsForeground = true
        guard !returnsControlWhenHidden || terminalIsVisible else { return }
        resumeTransport()
    }

    private func resumeTransport() {
        let wasForeground = wantsForeground
        wantsForeground = true
        guard !wasForeground, target != nil else { return }
        // Providers that retain their attachment can resume the encrypted
        // session and parser. A running actor alone isn't proof of a live peer: wait for an authenticated
        // response, including ACK-only traffic from an otherwise idle terminal.
        guard !returnsControlWhenHidden, receivedHostOp, client != nil, phase == .suspended else {
            beginConnection(recovering: true)
            return
        }
        let attempt = UUID(); generation = attempt
        stopIO()
        phase = .resuming
        recoveryMessage = nil
        enqueueTransition { [self] in
            guard generation == attempt, wantsForeground else { return }
            guard let client, lease != nil else { beginConnection(recovering: true); return }
            let baseline = await client.peerResponseCount
            guard generation == attempt, wantsForeground else { return }
            let deadline = environment?.resumeStartupTimeout ?? .seconds(3)
            resumeDeadline = Task { [weak self] in
                do { try await Task.sleep(for: deadline) } catch { return }
                guard let self, self.generation == attempt, self.wantsForeground,
                      self.phase == .resuming else { return }
                // stop() aborts a pending stream start, allowing the serialized
                // transition to finish before replacement attaches.
                let expiredAttempt = UUID()
                self.generation = expiredAttempt
                await client.stop()
                guard self.generation == expiredAttempt, self.wantsForeground else { return }
                self.beginConnection(recovering: true)
            }
            pendingResume = (attempt, client)
            defer { if pendingResume?.id == attempt { pendingResume = nil } }
            do {
                try await client.resumeFromApplicationBackground()
                if pendingResume?.id == attempt { pendingResume = nil }
                guard generation == attempt, wantsForeground else { return }
                try await activate(client, attempt: attempt, resumeBaseline: baseline)
            } catch {
                guard generation == attempt, wantsForeground else { return }
                beginConnection(recovering: true)
            }
        }
    }

    private func activate(_ session: any TerminalTransport, attempt: UUID, resumeBaseline: UInt64? = nil) async throws {
        guard generation == attempt else { return }
        if resumeBaseline == nil { try await session.start() }
        if let scrolling = session as? any RemoteTerminalScrolling {
            terminal.remoteScrollHandler = { [weak self] lines, column, row in
                guard let self, self.connected, self.generation == attempt else { return }
                Task {
                    do { try await scrolling.scroll(lines: lines, column: column, row: row) }
                    catch { self.fail("Trill could not scroll the remote terminal.", attempt: attempt) }
                }
            }
        }
        guard generation == attempt else { return }
        if !wantsForeground {
            phase = .suspended
            _ = try await session.prepareForApplicationBackground()
            return
        }
        guard reader == nil else { return }
        // The cancelled reader may already own a drained batch. Let it apply
        // that batch to this retained terminal before a replacement reads more.
        await drainingReader?.value
        drainingReader = nil
        guard generation == attempt, wantsForeground else { return }
        if resumeBaseline == nil {
            showRecoveryProgress("Waiting for your terminal to respond…")
            if phase != .reconnecting { phase = .connecting }
        }
        terminal.isUserInteractionEnabled = false
        let streamID = inputID
        let channel = AsyncStream<InputEnvelope>.makeStream()
        input = channel.continuation
        sender = Task { [weak self] in
            for await op in channel.stream {
                guard !Task.isCancelled, let self, self.generation == attempt else { return }
                do { try await self.handoff(op) { try await session.enqueue($0) } }
                catch {
                    if !Task.isCancelled, op.streamID == self.inputID {
                        self.fail("Trill could not send your input. Check your network connection.", attempt: attempt)
                    }
                    return
                }
            }
        }
        sendResize()
        reader = Task { [weak self] in
            let activity = await session.hostActivityStream()
            for await _ in activity {
                guard !Task.isCancelled else { return }
                // Wake on incoming traffic; SwiftTerm coalesces drawing at the
                // display cadence. Notifications coalesce without dropping bytes.
                let ops = await session.drainHostOps()
                guard let self else { return }
                self.consumeHostOps(ops, from: session)
                guard !Task.isCancelled, self.generation == attempt,
                      self.inputID == streamID, self.wantsForeground else { return }
                if let baseline = resumeBaseline, self.phase == .resuming {
                    let responses = await session.peerResponseCount
                    guard !Task.isCancelled, self.generation == attempt,
                          self.inputID == streamID, self.wantsForeground else { return }
                    if responses != baseline {
                        self.phase = .connected
                        self.resumeDeadline?.cancel(); self.resumeDeadline = nil
                        self.retryCount = 0
                        self.latestInterruption = nil
                        self.recoveryMessage = nil
                        self.terminal.isUserInteractionEnabled = true
                        self.sendResize()
                    }
                }
                if resumeBaseline == nil, self.receivedHostOp, !self.connected {
                    self.retryCount = 0
                    self.phase = .connected
                    self.latestInterruption = nil
                    self.recoveryMessage = nil
                    self.terminal.isUserInteractionEnabled = true
                    self.sendResize()
                }
                guard self.wantsForeground else { return }
            }
        }
        let monitorInterval = environment?.monitorInterval ?? .seconds(1)
        let responsePollLimit = environment?.responsePollLimit ?? 15
        let resumePollLimit = environment?.resumePollLimit ?? 3
        monitor = Task { [weak self] in
            var elapsed = 0
            while !Task.isCancelled {
                do { try await Task.sleep(for: monitorInterval) } catch { return }
                guard let self, self.generation == attempt, self.wantsForeground else { return }
                let state = await session.state
                guard !Task.isCancelled, self.generation == attempt,
                      self.inputID == streamID, self.wantsForeground else { return }
                elapsed += 1
                if self.phase == .resuming {
                    if state == .stopped || elapsed >= resumePollLimit {
                        self.beginConnection(recovering: true)
                        return
                    }
                }
                switch state {
                case .failed, .stopped:
                    self.fail("The terminal connection was interrupted. Check your network and that the companion is running.", attempt: attempt)
                    return
                default: break
                }
                if elapsed >= responsePollLimit, !self.receivedHostOp {
                    self.fail("Your terminal has not responded. Check your network connection to the companion.", attempt: attempt)
                    return
                }
            }
        }
    }
    private func consumeHostOps(_ ops: [TerminalOutputOperation], from session: any TerminalTransport) {
        guard client === session else { return }
        var output: [UInt8] = []
        if !ops.isEmpty, !receivedHostOp {
            output.append(contentsOf: "\u{1b}c".utf8)
            receivedHostOp = true
        }
        for op in ops {
            if case .hostBytes(let bytes) = op { output.append(contentsOf: bytes) }
        }
        // A state repair must be parsed with its host update before a renderer
        // can sample the viewport, rather than exposing an intermediate screen.
        if !output.isEmpty { terminal.feed(byteArray: output[...]) }
    }

    func key(_ text: String) {
        guard connected else { return }
        if let bytes = modifiedInput(Array(text.utf8)) { queueInput(.keystrokes(Data(bytes))) }
    }

    // One-shot modifiers apply to the next terminal key, never an inserted draft.
    // Use bracketed newline or negotiated CSI-u for Shift+Return: tmux can
    // otherwise normalize it into submit. Back-tab and cursors use standard sequences.
    private func modifiedInput(_ bytes: [UInt8]) -> [UInt8]? {
        let shift = shiftHeld, control = controlHeld
        inputNotice = nil
        defer { shiftHeld = false; controlHeld = false }
        let modifier = 1 + (shift ? 1 : 0) + (control ? 4 : 0)
        let text = String(bytes: bytes, encoding: .utf8)
        if let text, ["\u{1b}[A", "\u{1b}[B", "\u{1b}[C", "\u{1b}[D",
                      "\u{1b}OA", "\u{1b}OB", "\u{1b}OC", "\u{1b}OD"].contains(text), let final = text.last {
            if modifier > 1 { return Array("\u{1b}[1;\(modifier)\(final)".utf8) }
            let prefix = terminal.terminalInputStateSnapshot()?.applicationCursor == true ? "\u{1b}O" : "\u{1b}["
            return Array("\(prefix)\(final)".utf8)
        }
        guard bytes.count == 1, let original = bytes.first else {
            // Composed characters and IME text are Unicode, not single UTF-8 bytes.
            if shift, !control, let text, !text.unicodeScalars.contains(where: { $0.value < 32 || $0.value == 127 }) {
                return Array(text.uppercased().utf8)
            }
            return bytes
        }
        if original == 13, shift {
            let mode = terminal.terminalInputStateSnapshot()
            if !control, mode?.bracketedPasteMode == true {
                return Array("\u{1b}[200~\n\u{1b}[201~".utf8)
            }
            if mode?.keyboardEnhancementFlags.contains(.disambiguate) == true || mode?.keyboardEnhancementFlags.contains(.reportAllKeys) == true {
                return Array("\u{1b}[13;\(modifier)u".utf8)
            }
            inputNotice = "This terminal does not support safe Shift+Return, so Trill did not send Return. Use Write for multiple lines when the terminal supports bracketed paste."
            return nil
        }
        if original == 13, modifier > 1 { return Array("\u{1b}[13;\(modifier)u".utf8) }
        if original == 9, modifier > 1 {
            return Array((shift && !control ? "\u{1b}[Z" : "\u{1b}[9;\(modifier)u").utf8)
        }
        var byte = original
        if shift {
            if (97...122).contains(byte) { byte -= 32 }
            else {
                let plain = Array("`1234567890-=[]\\;',./".utf8)
                let shifted = Array("~!@#$%^&*()_+{}|:\"<>?".utf8)
                if let index = plain.firstIndex(of: byte) { byte = shifted[index] }
            }
        }
        if control, (64...127).contains(byte) { byte &= 0x1f }
        return [byte]
    }
    private func sendResize() {
        queueInput(.resize(cols: Int32(clamping: cols), rows: Int32(clamping: rows)))
    }
    func toggleKeyboard() { if terminal.isFirstResponder { _ = terminal.resignFirstResponder() } else if connected { _ = terminal.becomeFirstResponder() } }
    func setFontSize(_ size: Double) {
        // Reappearing session views reapply this preference. SwiftTerm's font
        // setter soft-resets modes and scrolling margins even for the same font.
        guard terminal.font.pointSize != CGFloat(size) else { return }
        terminal.font = .monospacedSystemFont(ofSize: CGFloat(size), weight: .regular)
    }
    func send(source: TerminalView, data: ArraySlice<UInt8>) {
        guard connected else { return }
        if let bytes = modifiedInput(Array(data)) { queueInput(.keystrokes(Data(bytes))) }
    }
    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        cols = max(1, newCols); rows = max(1, newRows)
        if connected { sendResize() }
    }
    func setTerminalTitle(source: TerminalView, title: String) { self.title = title }
    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
    func scrolled(source: TerminalView, position: Double) {}
    func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
    func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        guard let url = TerminalLinkRoute.webURL(link) else { return }
        pendingLink = url
    }
}

struct TerminalSurface: UIViewRepresentable {
    let connection: TerminalConnection
    func makeUIView(context: Context) -> TerminalView { connection.terminal }
    func updateUIView(_ uiView: TerminalView, context: Context) {}
    static func dismantleUIView(_ uiView: TerminalView, coordinator: ()) { _ = uiView.resignFirstResponder() }
}

/// A Herdr controller delivers ordered ANSI frames.
/// It shares the connection lifecycle, input serialization and lease cleanup.
actor HerdrTerminalTransport: RemoteTerminalScrolling {
    private(set) var state: TerminalTransportState = .idle
    private(set) var peerResponseCount: UInt64 = 0
    private let request: URLRequest
    private let session: URLSession
    private var socket: URLSessionWebSocketTask?
    private var receiver: Task<Void, Never>?
    private var heartbeat: Task<Void, Never>?
    private var pendingOperations: [TerminalOutputOperation] = []
    private var pendingByteCount = 0
    private var previousSequence: UInt64?
    private let activity = AsyncStream<Void>.makeStream(bufferingPolicy: .bufferingNewest(1))

    init(request: URLRequest, session: URLSession) {
        self.request = request
        self.session = session
    }
    func start() async throws {
        guard state == .idle else { throw TerminalTransportError.notStarted }
        state = .starting
        let socket = session.webSocketTask(with: request)
        socket.maximumMessageSize = 4 * 1024 * 1024
        self.socket = socket
        socket.resume()
        receiver = Task { [weak self] in
            do {
                while !Task.isCancelled {
                    let message = try await socket.receive()
                    guard let self else { return }
                    try await self.receive(message)
                }
            } catch { await self?.interrupted(error) }
        }
        heartbeat = Task { [weak self] in
            do {
                while !Task.isCancelled {
                    try await Task.sleep(for: .seconds(15))
                    try await Self.ping(socket)
                }
            } catch { await self?.interrupted(error) }
        }
    }
    struct Frame: Decodable {
        let type: String
        let seq: UInt64?
        let bytes: Data?
        let width: Int?
        let height: Int?
    }
    nonisolated static func ping(_ socket: URLSessionWebSocketTask) async throws {
        let deadline = Task {
            do { try await Task.sleep(for: .seconds(5)) }
            catch { return }
            socket.cancel(with: .goingAway, reason: nil)
        }
        defer { deadline.cancel() }
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            socket.sendPing { error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume() }
            }
        }
    }
    func receive(_ message: URLSessionWebSocketTask.Message) throws {
        let data: Data
        switch message {
        case .string(let text): data = Data(text.utf8)
        case .data(let bytes): data = bytes
        @unknown default: throw TrillError.message("Unknown terminal frame.")
        }
        let frame = try JSONDecoder().decode(Frame.self, from: data)
        guard frame.type == "terminal.frame", let bytes = frame.bytes,
              let sequence = frame.seq, let width = frame.width, let height = frame.height,
              (20...500).contains(width), (5...200).contains(height),
              previousSequence.map({ sequence > $0 }) ?? true else {
            throw TrillError.message("The Herdr terminal stream ended or returned an invalid frame.")
        }
        guard pendingByteCount + bytes.count <= 8 * 1024 * 1024 else {
            throw TrillError.message("The terminal produced output faster than Trill could display it.")
        }
        previousSequence = sequence
        pendingByteCount += bytes.count
        pendingOperations.append(.hostBytes(bytes))
        peerResponseCount &+= 1
        state = .running
        activity.continuation.yield(())
    }
    private func interrupted(_ error: Error) {
        guard state != .stopped else { return }
        state = .failed(.protocolViolation(error.localizedDescription))
        socket?.cancel(with: .goingAway, reason: nil)
        activity.continuation.finish()
    }
    func enqueue(_ operation: TerminalInputOperation) async throws {
        switch operation {
        case .keystrokes(let bytes):
            // Match the companion's bounded input messages. Preserve byte order.
            for start in stride(from: 0, to: bytes.count, by: 8192) {
                let end = min(start + 8192, bytes.count)
                try await send(["type": "terminal.input", "bytes": bytes.subdata(in: start..<end).base64EncodedString()])
            }
        case .resize(let columns, let rows):
            try await send(["type": "terminal.resize", "cols": max(20, min(500, Int(columns))), "rows": max(5, min(200, Int(rows)))])
        }
    }
    func scroll(lines: Int, column: Int, row: Int) async throws {
        guard lines != 0 else { return }
        try await send(["type": "terminal.scroll", "direction": lines > 0 ? "up" : "down", "lines": min(200, abs(lines)), "column": column, "row": row])
    }
    private func send(_ command: [String: Any]) async throws {
        guard let socket, state == .starting || state == .running else { throw TerminalTransportError.notStarted }
        let data = try JSONSerialization.data(withJSONObject: command)
        try await socket.send(.string(String(decoding: data, as: UTF8.self)))
    }
    func drainHostOps() -> [TerminalOutputOperation] {
        defer { pendingOperations.removeAll(keepingCapacity: true); pendingByteCount = 0 }
        return pendingOperations
    }
    func hostActivityStream() -> AsyncStream<Void> { activity.stream }
    func stop() {
        state = .stopped
        receiver?.cancel(); receiver = nil
        heartbeat?.cancel(); heartbeat = nil
        socket?.cancel(with: .normalClosure, reason: nil); socket = nil
        session.invalidateAndCancel()
        activity.continuation.finish()
    }
    // Herdr always releases the controller on background; it reconnects with a
    // fresh full frame rather than resuming a stale viewport or replaying input.
    func resumeFromApplicationBackground() async throws { throw TerminalTransportError.notStarted }
    func prepareForApplicationBackground() async throws {
        stop()
        throw TerminalTransportError.notStarted
    }
}
