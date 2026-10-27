import XCTest
import SwiftUI
import UIKit
import MoshCompression
import MoshCryptoOCB
import MoshProtoLite
import MoshTransport
import MoshWire
@testable import MoshCore
@testable import SwiftTerm
@testable import Trill

final class TerminalInputTests: XCTestCase {
    @MainActor func testBackspaceRemainsEnabledForRepeatedDeletionWithoutLocalText() async {
        let channel = AsyncStream<TerminalConnection.InputEnvelope>.makeStream()
        let connection = TerminalConnection(inputContinuation: channel.continuation)
        connection.phase = .connected
        // Text on the remote shell may come from another client, a restored
        // terminal, or Write, so UIKit has no local text shadow to inspect.
        for _ in 0..<8 {
            XCTAssertTrue(connection.terminal.hasText)
            if connection.terminal.hasText { connection.terminal.deleteBackward() }
        }
        channel.continuation.finish()
        var operations: [MoshClientOp] = []
        for await envelope in channel.stream { operations.append(envelope.operation) }
        XCTAssertEqual(operations, Array(repeating: .keystrokes(Data([127])), count: 8))
    }

    @MainActor func testPhotoAppendPreservesUnicodeCaretAndSelection() {
        let original = "café 👩🏽‍💻 日本語"
        let selections = [
            NSRange(location: (original as NSString).length, length: 0),
            (original as NSString).range(of: "👩🏽‍💻"),
            (original as NSString).range(of: "日本語")
        ]
        for selection in selections {
            var model = original
            let coordinator = TerminalDraftEditor.Coordinator(text: Binding(get: { model }, set: { model = $0 }))
            let view = UITextView()
            view.text = original
            view.selectedRange = selection
            model += " '/tmp/photo.jpg'"

            coordinator.synchronize(view)

            XCTAssertEqual(view.text, model)
            XCTAssertEqual(view.selectedRange, selection)
            coordinator.synchronize(view)
            XCTAssertEqual(view.text, original + " '/tmp/photo.jpg'", "A render must not duplicate the path")
        }
    }

    @MainActor func testPhotoAppendWaitsForMarkedTextAndSurvivesCandidateReplacement() {
        var model = "say "
        var compositionStates: [Bool] = []
        let coordinator = TerminalDraftEditor.Coordinator(
            text: Binding(get: { model }, set: { model = $0 }),
            compositionChanged: { compositionStates.append($0) })
        let view = UITextView()
        view.text = model
        view.selectedRange = NSRange(location: 4, length: 0)
        view.setMarkedText("に", selectedRange: NSRange(location: 1, length: 0))
        coordinator.textViewDidChange(view)
        XCTAssertNotNil(view.markedTextRange)
        XCTAssertEqual(model, "say に")
        let nativeSelection = view.selectedRange

        model += " '/tmp/first.jpg'"
        coordinator.synchronize(view)
        XCTAssertEqual(view.text, "say に")
        XCTAssertNotNil(view.markedTextRange)
        XCTAssertEqual(view.selectedRange, nativeSelection)

        model += " '/tmp/second.jpg'"
        coordinator.synchronize(view)
        view.setMarkedText("日本語", selectedRange: NSRange(location: 3, length: 0))
        coordinator.textViewDidChange(view)
        XCTAssertEqual(model, "say 日本語 '/tmp/first.jpg' '/tmp/second.jpg'")
        XCTAssertEqual(view.text, "say 日本語")

        view.unmarkText()
        let committedSelection = view.selectedRange
        coordinator.textViewDidChangeSelection(view)
        XCTAssertEqual(view.text, model)
        XCTAssertEqual(view.selectedRange, committedSelection)
        XCTAssertEqual(compositionStates, [true, false])
        coordinator.synchronize(view)
        XCTAssertEqual(view.text, "say 日本語 '/tmp/first.jpg' '/tmp/second.jpg'")
    }

    @MainActor func testPhotoCompletionBeforeSwiftUIUpdatePreservesNewNativeTyping() {
        var model = "draft"
        let coordinator = TerminalDraftEditor.Coordinator(text: Binding(get: { model }, set: { model = $0 }))
        let view = UITextView()
        view.text = model
        model += " '/tmp/photo.jpg'"
        // UIKit has accepted another edit, but SwiftUI has not called updateUIView.
        view.text = "draft café 🙂"
        view.selectedRange = NSRange(location: (view.text as NSString).length, length: 0)
        let caret = view.selectedRange

        coordinator.textViewDidChange(view)

        XCTAssertEqual(model, "draft café 🙂 '/tmp/photo.jpg'")
        XCTAssertEqual(view.text, model)
        XCTAssertEqual(view.selectedRange, caret)
    }

    @MainActor func testOrdinaryRenderLeavesMarkedTextIntact() {
        var model = ""
        let coordinator = TerminalDraftEditor.Coordinator(text: Binding(get: { model }, set: { model = $0 }))
        let view = UITextView()
        view.setMarkedText("かな", selectedRange: NSRange(location: 2, length: 0))
        coordinator.textViewDidChange(view)
        let selection = view.selectedRange

        coordinator.synchronize(view)

        XCTAssertNotNil(view.markedTextRange)
        XCTAssertEqual(view.text, "かな")
        XCTAssertEqual(view.selectedRange, selection)
    }

    @MainActor func testExternalClearClampsSelection() {
        var model = "keep café 🙂"
        let coordinator = TerminalDraftEditor.Coordinator(text: Binding(get: { model }, set: { model = $0 }))
        let view = UITextView()
        view.text = model
        view.selectedRange = NSRange(location: (model as NSString).length, length: 0)
        model = ""

        coordinator.synchronize(view)

        XCTAssertEqual(view.text, "")
        XCTAssertEqual(view.selectedRange, NSRange(location: 0, length: 0))
    }

    @MainActor func testUploadLifetimeEndsOnEditorTeardown() {
        var model = "kept draft"
        var cancellations = 0
        let coordinator = TerminalDraftEditor.Coordinator(
            text: Binding(get: { model }, set: { model = $0 }),
            onDismantle: { cancellations += 1 })
        let view = UITextView()
        view.text = model
        view.delegate = coordinator

        coordinator.synchronize(view)
        XCTAssertEqual(cancellations, 0, "Covering sheets and ordinary renders retain the editor")
        TerminalDraftEditor.dismantleUIView(view, coordinator: coordinator)
        XCTAssertEqual(cancellations, 1)
        XCTAssertNil(view.delegate)
        XCTAssertEqual(model, "kept draft")
    }
}

extension TerminalInputTests {
    @MainActor func testReturningToSessionDoesNotResetItsTerminalModesOrScrollRegion() {
        let connection = TerminalConnection()
        connection.setFontSize(14)
        connection.terminal.feed(text: "\u{1b}[?1049h\u{1b}[2J\u{1b}[1;1HHEADER\u{1b}[2;5r\u{1b}[?6h\u{1b}[?1h")
        XCTAssertEqual(connection.terminal.terminalInputStateSnapshot()?.applicationCursor, true)

        // SessionDetailView reapplies this preference whenever a retained
        // connection is shown again after switching to another session.
        connection.setFontSize(14)
        XCTAssertEqual(connection.terminal.terminalInputStateSnapshot()?.applicationCursor, true)
        connection.terminal.feed(text: "\u{1b}[1;1HUPDATE")
        let rows = connection.terminal.terminalStateSnapshot().visibleRows
        XCTAssertTrue(rows[0].text.hasPrefix("HEADER"), "The TUI header must remain outside its scrolling region")
        XCTAssertTrue(rows[1].text.hasPrefix("UPDATE"), "Origin-relative output must still use the retained scrolling region")
    }

    @MainActor func testRepeatedSessionSwitchesPreserveIndependentTerminalState() {
        let first = TerminalConnection()
        let second = TerminalConnection()
        first.setFontSize(14); second.setFontSize(14)
        first.terminal.feed(text: "\u{1b}[?1049h\u{1b}[2J\u{1b}[1;1HFIRST\u{1b}[2;5r\u{1b}[?6h\u{1b}[?1h")
        second.terminal.feed(text: "\u{1b}[?1049h\u{1b}[2J\u{1b}[1;1HSECOND\u{1b}[3;6r\u{1b}[?6h\u{1b}[?1h")
        for _ in 0..<3 {
            first.setFontSize(14)
            first.terminal.feed(text: "\u{1b}[1;1HONE")
            second.setFontSize(14)
            second.terminal.feed(text: "\u{1b}[1;1HTWO")
        }
        let firstRows = first.terminal.terminalStateSnapshot().visibleRows
        let secondRows = second.terminal.terminalStateSnapshot().visibleRows
        XCTAssertTrue(firstRows[0].text.hasPrefix("FIRST"))
        XCTAssertTrue(firstRows[1].text.hasPrefix("ONE"))
        XCTAssertTrue(secondRows[0].text.hasPrefix("SECOND"))
        XCTAssertTrue(secondRows[2].text.hasPrefix("TWO"))
        XCTAssertEqual(first.terminal.terminalInputStateSnapshot()?.applicationCursor, true)
        XCTAssertEqual(second.terminal.terminalInputStateSnapshot()?.applicationCursor, true)
    }
}

// Tests the native finger-to-wheel path, the real encrypted Mosh client and the
// connection reader. The peer is a synthetic scrolling TUI, not mosh-server.
extension TerminalInputTests {
    @MainActor func testAgentScrollWithSimulatedNetworkLatency() async throws {
        var measurements: [[String: Any]] = []
        for roundTripMilliseconds in [0, 50, 100, 200] {
            let (clientEndpoint, peerEndpoint) = await InMemoryDatagramPair.makeLinked()
            let delayedClient = ScrollDelayedEndpoint(endpoint: clientEndpoint, delayMilliseconds: roundTripMilliseconds / 2)
            let delayedPeer = ScrollDelayedEndpoint(endpoint: peerEndpoint, delayMilliseconds: roundTripMilliseconds / 2)
            let key = Data((0..<16).map(UInt8.init))
            let session = MoshClientSession(
                endpoint: .init(host: "scroll-fixture.invalid", port: 60001, keyBase64_22: "AAECAwQFBgcICQoLDA0ODw"),
                config: MoshClientConfig(), endpointFactory: { _, _ in delayedClient },
                snapshotEncoder: { try JSONEncoder().encode($0) })
            let peer = ScrollLatencyPeer(engine: TransportEngine(
                endpoint: delayedPeer, outgoingDirection: .toClient,
                cipher: try OCBTransportCipher(key: key)))
            try await peer.start()
            let peerTask = Task { try await peer.run() }
            let connection = TerminalConnection()
            let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 700))
            let controller = UIViewController()
            window.rootViewController = controller
            connection.terminal.frame = controller.view.bounds
            controller.view.addSubview(connection.terminal)
            window.makeKeyAndVisible()
            let sampler = ScrollViewportSampler(terminal: connection.terminal)
            do {
                await connection.connect(sessionID: "synthetic-scroll", environment: .init(
                    attach: { _, _, _ in .init(host: "scroll-fixture.invalid", port: 60001,
                                               key: "AAECAwQFBgcICQoLDA0ODw", leaseId: "synthetic-scroll") },
                    release: { _ in }, makeTransport: { _ in session }))
                try await peer.drawInitialScreen()
                try await scrollWait { connection.connected && connection.terminal.terminalInputStateSnapshot()?.mouseMode != .off }
                let gesture = ScrollProbeGesture()
                gesture.testLocation = CGPoint(x: connection.terminal.bounds.midX, y: connection.terminal.bounds.midY)
                sampler.start()
                // A steady drag at approximately 60 samples/second, one cell per sample.
                for _ in 0..<30 {
                    sampler.inputTimes.append(ProcessInfo.processInfo.systemUptime)
                    connection.terminal.forwardWheelDrag(distance: connection.terminal.cellDimension.height,
                                                          gestureRecognizer: gesture)
                    try await Task.sleep(for: .milliseconds(17))
                }
                try await scrollWait { sampler.lastPosition == 30 }
                sampler.stop()
                let latencies = sampler.samples.map { ($0.time - sampler.inputTimes[$0.position - 1]) * 1000 }.sorted()
                XCTAssertFalse(latencies.isEmpty)
                let median = latencies[latencies.count / 2]
                let maximum = latencies.last ?? 0
                XCTAssertGreaterThanOrEqual(median, Double(roundTripMilliseconds) - 10)
                XCTAssertLessThan(maximum, Double(roundTripMilliseconds) + 250)
                let peerCount = await peer.scrollPosition
                XCTAssertEqual(peerCount, 30, "All generated scroll reports must reach the peer")
                var measurement: [String: Any] = [
                    "simulated_rtt_ms": roundTripMilliseconds, "input_samples": 30,
                    "observed_viewport_changes": sampler.samples.count,
                    "median_viewport_latency_ms": median, "maximum_viewport_latency_ms": maximum,
                    "maximum_change_gap_ms": zip(sampler.samples, sampler.samples.dropFirst())
                        .map { ($1.time - $0.time) * 1000 }.max() ?? 0
                ]
                // Local history uses the terminal buffer and must not send wheel
                // reports or wait for a delayed remote response.
                connection.terminal.feed(text: "\u{1b}c" + (0..<200).map { "LOCAL \($0)\r\n" }.joined())
                try await scrollWait {
                    connection.terminal.terminalContentSnapshot(region: .history(maximumScrollbackRows: 200))?
                        .rows.contains(where: { $0.text == "LOCAL 199" }) == true
                }
                let previousTop = connection.terminal.terminalContentSnapshot(region: .viewport)?.rows.first?.text
                let localStart = ProcessInfo.processInfo.systemUptime
                connection.terminal.scrollUp(lines: 8)
                try await scrollWait {
                    connection.terminal.terminalContentSnapshot(region: .viewport)?.rows.first?.text != previousTop
                }
                let localLatency = (ProcessInfo.processInfo.systemUptime - localStart) * 1000
                XCTAssertLessThan(localLatency, 100)
                measurement["local_scroll_ready_ms"] = localLatency
                measurements.append(measurement)
                await connection.disconnect()
                await peer.stop()
                peerTask.cancel()
                _ = await peerTask.result
                let clientDeliveryFailure = await delayedClient.deliveryFailure
                let peerDeliveryFailure = await delayedPeer.deliveryFailure
                XCTAssertNil(clientDeliveryFailure)
                XCTAssertNil(peerDeliveryFailure)
                window.isHidden = true
            } catch {
                print("SCROLL_PROBE_FAILURE rtt=\(roundTripMilliseconds) peer=\(await peer.scrollPosition) viewport=\(sampler.lastPosition) inputs=\(sampler.inputTimes.count) phase=\(connection.phase) text=\(connection.terminal.terminalContentSnapshot(region: .viewport)?.rows.first?.text ?? "nil")")
                sampler.stop()
                await connection.disconnect()
                await peer.stop()
                peerTask.cancel()
                _ = await peerTask.result
                window.isHidden = true
                throw error
            }
        }
        let report = try JSONSerialization.data(withJSONObject: measurements, options: [.prettyPrinted, .sortedKeys])
        print("SCROLL_LATENCY_REPORT " + String(decoding: report, as: UTF8.self))
        let attachment = XCTAttachment(data: report, uniformTypeIdentifier: "public.json")
        attachment.name = "scroll-latency.json"
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    @MainActor private func scrollWait(_ condition: () -> Bool) async throws {
        let deadline = ProcessInfo.processInfo.systemUptime + 5
        while !condition() {
            guard ProcessInfo.processInfo.systemUptime < deadline else { throw ScrollProbeError.timeout }
            try await Task.sleep(for: .milliseconds(5))
        }
    }
}

private enum ScrollProbeError: Error { case timeout }

@MainActor private final class ScrollProbeGesture: UIPanGestureRecognizer {
    var testLocation = CGPoint.zero
    override func location(in view: UIView?) -> CGPoint { testLocation }
}

@MainActor private final class ScrollViewportSampler: NSObject {
    let terminal: TrillTerminalView
    var inputTimes: [TimeInterval] = []
    var samples: [(position: Int, time: TimeInterval)] = []
    var lastPosition = 0
    private var displayLink: CADisplayLink?
    init(terminal: TrillTerminalView) { self.terminal = terminal }
    func start() {
        let link = CADisplayLink(target: self, selector: #selector(sample))
        link.add(to: .main, forMode: .common)
        displayLink = link
    }
    func stop() { displayLink?.invalidate(); displayLink = nil }
    @objc private func sample() {
        guard let firstRow = terminal.terminalContentSnapshot(region: .viewport)?.rows.first?.text,
              firstRow.hasPrefix("SCROLL "),
              let position = Int(firstRow.dropFirst(7).trimmingCharacters(in: .whitespaces)),
              position > lastPosition else { return }
        lastPosition = position
        samples.append((position, ProcessInfo.processInfo.systemUptime))
    }
}

// Schedule packets independently: sleeping in send/receive itself would impose
// an artificial bandwidth limit of one packet per delay interval.
private actor ScrollDelayedEndpoint: DatagramEndpoint {
    let endpoint: InMemoryDatagramEndpoint
    let delayMilliseconds: Int
    private var deliveries: [UUID: Task<Void, Never>] = [:]
    private var deliveryTail: Task<Void, Never>?
    private(set) var deliveryFailure: String?
    init(endpoint: InMemoryDatagramEndpoint, delayMilliseconds: Int) {
        self.endpoint = endpoint; self.delayMilliseconds = delayMilliseconds
    }
    func start() async throws { try await endpoint.start() }
    func stop() async {
        for delivery in deliveries.values { delivery.cancel() }
        deliveries.removeAll()
        await endpoint.stop()
    }
    func send(_ data: Data) async throws {
        let identifier = UUID()
        let previousDelivery = deliveryTail
        let delivery = Task {
            do {
                try await Task.sleep(for: .milliseconds(delayMilliseconds))
                await previousDelivery?.value
                try Task.checkCancellation()
                try await endpoint.send(data)
            } catch is CancellationError {
            } catch { deliveryFailure = String(describing: error) }
            deliveries.removeValue(forKey: identifier)
        }
        deliveries[identifier] = delivery
        deliveryTail = delivery
    }
    func receive() async throws -> TransportDatagram { try await endpoint.receive() }
}

private actor ScrollLatencyPeer {
    let engine: TransportEngine
    private var remoteState: UInt64 = 0
    private var lastClientState: UInt64 = 0
    private(set) var scrollPosition = 0
    init(engine: TransportEngine) { self.engine = engine }
    func start() async throws { try await engine.start() }
    func stop() async { await engine.stop() }
    func drawInitialScreen() async throws {
        try await draw("\u{1b}[?1049h\u{1b}[?1000h\u{1b}[?1006h\u{1b}[2J\u{1b}[HSCROLL 0")
    }
    func run() async throws {
        while !Task.isCancelled {
            let packet = try await engine.receivePayload()
            let payload = try MoshCompressionCodec().decompress(packet.payload, algorithm: .zlib)
            let instruction = try TransportInstruction(decoding: payload)
            guard let newState = instruction.newNum, newState > lastClientState else { continue }
            lastClientState = newState
            let input = try UserMessage(decoding: instruction.diff ?? Data())
            for operation in input.instructions {
                if case .keystroke(let bytes) = operation {
                    let text = String(decoding: bytes, as: UTF8.self)
                    scrollPosition += text.components(separatedBy: "\u{1b}[<64;").count - 1
                }
            }
            try await draw("\u{1b}[H\u{1b}[2KSCROLL \(scrollPosition)")
        }
    }
    private func draw(_ text: String) async throws {
        let previousState = remoteState
        remoteState += 1
        let instruction = TransportInstruction(protocolVersion: MoshWire.protocolVersion,
            oldNum: previousState, newNum: remoteState, ackNum: lastClientState,
            diff: HostMessage(instructions: [.hostBytes(Data(text.utf8))]).encoded())
        try await engine.sendPayload(MoshCompressionCodec().compress(instruction.encoded(), algorithm: .zlib))
    }
}
