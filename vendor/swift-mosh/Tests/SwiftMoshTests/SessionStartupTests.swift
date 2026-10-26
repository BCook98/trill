import Foundation
import XCTest
@testable import MoshCore
import MoshTransport

final class SessionStartupTests: XCTestCase {
    func testStopAbortsPendingStartup() async throws {
        let entered = expectation(description: "endpoint start entered")
        let finished = expectation(description: "session start aborted")
        let endpoint = StartupEndpoint(pendingStart: true, onStart: { entered.fulfill() })
        let session = makeSession([endpoint])
        let start = Task {
            defer { finished.fulfill() }
            try await session.start()
        }
        await fulfillment(of: [entered], timeout: 1)
        await session.stop()
        await fulfillment(of: [finished], timeout: 1)
        // Release even on regression so a broken implementation cannot hang the suite.
        await endpoint.completeStart(error: TransportError.cancelled)
        await assertCancelled(start)
        let state = await session.state
        XCTAssertEqual(state, .stopped)
    }

    func testStopAbortsPendingResume() async throws {
        let entered = expectation(description: "resume endpoint start entered")
        let finished = expectation(description: "resume aborted")
        let resumedEndpoint = StartupEndpoint(pendingStart: true, onStart: { entered.fulfill() })
        let session = makeSession([StartupEndpoint(), resumedEndpoint])
        try await session.start()
        _ = try await session.prepareForApplicationBackground()
        let resume = Task {
            defer { finished.fulfill() }
            try await session.resumeFromApplicationBackground()
        }
        await fulfillment(of: [entered], timeout: 1)
        await session.stop()
        await fulfillment(of: [finished], timeout: 1)
        await resumedEndpoint.completeStart(error: TransportError.cancelled)
        await assertCancelled(resume)
        let state = await session.state
        XCTAssertEqual(state, .stopped)
    }

    func testLateStartupSuccessCannotReviveStoppedSession() async throws {
        let entered = expectation(description: "endpoint start entered")
        let endpoint = StartupEndpoint(
            pendingStart: true, cancelStartOnStop: false, onStart: { entered.fulfill() }
        )
        let session = makeSession([endpoint])
        let start = Task { try await session.start() }
        await fulfillment(of: [entered], timeout: 1)
        await session.stop()
        await endpoint.completeStart()
        await assertCancelled(start)
        let state = await session.state
        let running = await endpoint.running
        let receives = await endpoint.receiveCount
        XCTAssertEqual(state, .stopped)
        XCTAssertFalse(running, "The late endpoint must be closed again")
        XCTAssertEqual(receives, 0, "An obsolete startup must not launch a receive loop")
    }

    func testLateStartupSuccessAndFailureCannotClobberReplacement() async throws {
        for lateFailure in [false, true] {
            let entered = expectation(description: "old endpoint start entered")
            let old = StartupEndpoint(
                pendingStart: true, cancelStartOnStop: false, onStart: { entered.fulfill() }
            )
            let replacement = StartupEndpoint()
            let session = makeSession([old, replacement])
            let start = Task { try await session.start() }
            await fulfillment(of: [entered], timeout: 1)
            await session.stop()
            try await session.start()
            await old.completeStart(error: lateFailure ? TransportError.networkFailure("late failure") : nil)
            do {
                try await start.value
                XCTFail("Obsolete startup must throw")
            } catch {}
            let state = await session.state
            XCTAssertEqual(state, .running)
            // Exercise the retained engine, not just the published session state.
            try await session.enqueue(.keystrokes(Data("x".utf8)))
            let sends = await replacement.sendCount
            let running = await replacement.running
            XCTAssertEqual(sends, 1)
            XCTAssertTrue(running)
            await session.stop()
        }
    }

    func testStartIsRejectedWhileStopIsPendingIncludingRepeatedStop() async throws {
        let stopping = expectation(description: "endpoint stop entered")
        let old = StartupEndpoint(pendingStop: true, onStop: { stopping.fulfill() })
        let replacement = StartupEndpoint()
        let session = makeSession([old, replacement])
        try await session.start()
        let stop = Task { await session.stop() }
        await fulfillment(of: [stopping], timeout: 1)
        await session.stop()
        let stoppingState = await session.state
        XCTAssertEqual(stoppingState, .stopping)
        do {
            try await session.start()
            XCTFail("Start must not overlap unfinished shutdown")
        } catch {
            guard case .notStarted = error as? MoshSessionError else {
                await old.completeStop()
                await stop.value
                return XCTFail("Unexpected error: \(error)")
            }
        }
        await old.completeStop()
        await stop.value
        try await session.start()
        try await session.enqueue(.keystrokes(Data("x".utf8)))
        let state = await session.state
        let sends = await replacement.sendCount
        XCTAssertEqual(state, .running)
        XCTAssertEqual(sends, 1)
    }

    func testFailedStartupCleanupCannotResetNewStartupToIdle() async throws {
        let entered = expectation(description: "first startup entered")
        let cleaning = expectation(description: "failed startup cleanup entered")
        let nextEntered = expectation(description: "replacement startup entered")
        let old = StartupEndpoint(
            pendingStart: true, pendingStop: true,
            onStart: { entered.fulfill() }, onStop: { cleaning.fulfill() }
        )
        let replacement = StartupEndpoint(pendingStart: true, onStart: { nextEntered.fulfill() })
        let session = makeSession([old, replacement])
        let start = Task { try await session.start() }
        await fulfillment(of: [entered], timeout: 1)
        await old.completeStart(error: TransportError.networkFailure("startup failure"))
        await fulfillment(of: [cleaning], timeout: 1)
        await session.stop()
        let nextStart = Task { try await session.start() }
        await fulfillment(of: [nextEntered], timeout: 1)
        await old.completeStop()
        do {
            try await start.value
            XCTFail("Expected startup failure")
        } catch {}
        let state = await session.state
        XCTAssertEqual(state, .starting)
        await replacement.completeStart()
        try await nextStart.value
        try await session.enqueue(.keystrokes(Data("x".utf8)))
        let sends = await replacement.sendCount
        XCTAssertEqual(sends, 1)
    }

    private func makeSession(_ endpoints: [StartupEndpoint]) -> MoshClientSession {
        let factory = StartupEndpointFactory(endpoints)
        let session = MoshClientSession(
            endpoint: MoshEndpoint(host: "127.0.0.1", port: 60001, keyBase64_22: "AAECAwQFBgcICQoLDA0ODw"),
            config: .init(heartbeatIntervalMs: 60_000, useNetworkCrypto: false),
            endpointFactory: { _, _ in factory.next() },
            snapshotEncoder: { try JSONEncoder().encode($0) }
        )
        addTeardownBlock {
            for endpoint in endpoints {
                await endpoint.completeStart(error: TransportError.cancelled)
                await endpoint.completeStop()
            }
            await session.stop()
        }
        return session
    }

    private func assertCancelled(_ task: Task<Void, Error>) async {
        do {
            try await task.value
            XCTFail("Expected cancelled startup")
        } catch {
            guard case .cancelled = error as? TransportError else {
                return XCTFail("Unexpected error: \(error)")
            }
        }
    }
}

private final class StartupEndpointFactory: @unchecked Sendable {
    private let lock = NSLock()
    private var endpoints: [StartupEndpoint]

    init(_ endpoints: [StartupEndpoint]) { self.endpoints = endpoints }

    func next() -> StartupEndpoint {
        lock.lock()
        defer { lock.unlock() }
        return endpoints.removeFirst()
    }
}

private actor StartupEndpoint: DatagramEndpoint {
    private let pendingStart: Bool
    private let cancelStartOnStop: Bool
    private var pendingStop: Bool
    private let onStart: @Sendable () -> Void
    private let onStop: @Sendable () -> Void
    private var startContinuation: CheckedContinuation<Void, Error>?
    private var stopContinuation: CheckedContinuation<Void, Never>?
    private var receiveContinuation: CheckedContinuation<TransportDatagram, Error>?
    private(set) var running = false
    private(set) var sendCount = 0
    private(set) var receiveCount = 0

    init(
        pendingStart: Bool = false, cancelStartOnStop: Bool = true, pendingStop: Bool = false,
        onStart: @escaping @Sendable () -> Void = {}, onStop: @escaping @Sendable () -> Void = {}
    ) {
        self.pendingStart = pendingStart
        self.cancelStartOnStop = cancelStartOnStop
        self.pendingStop = pendingStop
        self.onStart = onStart
        self.onStop = onStop
    }

    func start() async throws {
        if pendingStart {
            try await withCheckedThrowingContinuation { continuation in
                startContinuation = continuation
                onStart()
            }
        } else {
            onStart()
        }
        running = true
    }

    func completeStart(error: (any Error)? = nil) {
        let continuation = startContinuation
        startContinuation = nil
        if let error { continuation?.resume(throwing: error) }
        else { continuation?.resume() }
    }

    func stop() async {
        running = false
        if cancelStartOnStop { completeStart(error: TransportError.cancelled) }
        receiveContinuation?.resume(throwing: TransportError.cancelled)
        receiveContinuation = nil
        if pendingStop {
            pendingStop = false
            await withCheckedContinuation { continuation in
                stopContinuation = continuation
                onStop()
            }
        }
    }

    func completeStop() {
        stopContinuation?.resume()
        stopContinuation = nil
    }

    func send(_ data: Data) async throws {
        guard running else { throw TransportError.notStarted }
        sendCount += 1
    }

    func receive() async throws -> TransportDatagram {
        guard running else { throw TransportError.notStarted }
        receiveCount += 1
        return try await withCheckedThrowingContinuation { receiveContinuation = $0 }
    }
}
