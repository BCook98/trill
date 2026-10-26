import Foundation
import XCTest
import MoshCompression
@testable import MoshCore
import MoshProtoLite
import MoshTransport
import MoshWire

final class PeerResponseCountTests: XCTestCase {
    func testAckOnlyAndNoOpResponsesCountEvenForRepeatedRemoteState() async throws {
        let (session, peer) = try await makeSession()
        let initialCount = await session.peerResponseCount
        XCTAssertEqual(initialCount, 0)

        // Idle peers repeat their current state number with no terminal output.
        let ack = TransportInstruction(protocolVersion: MoshWire.protocolVersion, oldNum: 0, newNum: 0, ackNum: 0)
        try await peer.send(datagram(instruction: ack, sequence: 0))
        try await waitForCount(1, session: session)
        try await peer.send(datagram(instruction: ack, sequence: 1))
        try await waitForCount(2, session: session)

        let noOp = TransportInstruction(
            protocolVersion: MoshWire.protocolVersion, oldNum: 0, newNum: 1, diff: Data()
        )
        try await peer.send(datagram(instruction: noOp, sequence: 2))
        try await waitForCount(3, session: session)

        // The ACK is still accepted when the diff's base state is unavailable.
        let unavailableBase = TransportInstruction(
            protocolVersion: MoshWire.protocolVersion, oldNum: 9, newNum: 10, ackNum: 0
        )
        try await peer.send(datagram(instruction: unavailableBase, sequence: 3))
        try await waitForCount(4, session: session)
        let hostOps = await session.drainHostOps()
        XCTAssertEqual(hostOps, [])
    }

    func testSuspendResumePreservesCountAndReplayFilter() async throws {
        let (session, peer) = try await makeSession()
        let ack = TransportInstruction(protocolVersion: MoshWire.protocolVersion, oldNum: 0, newNum: 0, ackNum: 0)
        let original = try datagram(instruction: ack, sequence: 0)
        try await peer.send(original)
        try await waitForCount(1, session: session)

        _ = try await session.prepareForApplicationBackground()
        let suspendedCount = await session.peerResponseCount
        XCTAssertEqual(suspendedCount, 1)
        // Match the caller's contract: read the baseline immediately before resume.
        let baseline = await session.peerResponseCount
        try await session.resumeFromApplicationBackground()
        let resumedCount = await session.peerResponseCount
        XCTAssertEqual(resumedCount, baseline)

        try await peer.send(original) // Replayed ciphertext must remain filtered after resume.
        try await peer.send(datagram(payload: Data(), sequence: 1)) // Empty assembled instruction.
        try await peer.send(datagram(instruction: ack, sequence: 2, direction: .toServer))
        try await peer.send(datagram(instruction: ack, sequence: 3))
        try await waitForCount(baseline + 1, session: session)
        let idleHostOps = await session.drainHostOps()
        XCTAssertEqual(idleHostOps, [])

        // A following visible instruction is a processing barrier for all traffic
        // above, so an erroneously counted replay cannot make this test pass early.
        let visible = TransportInstruction(
            protocolVersion: MoshWire.protocolVersion, oldNum: 0, newNum: 1,
            diff: HostMessage(instructions: [.hostBytes(Data("barrier".utf8))]).encoded()
        )
        try await peer.send(datagram(instruction: visible, sequence: 4))
        var hostOps: [MoshHostOp] = []
        for _ in 0..<100 {
            hostOps += await session.drainHostOps()
            if !hostOps.isEmpty { break }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(hostOps, [.hostBytes(Data("barrier".utf8))])
        let finalCount = await session.peerResponseCount
        XCTAssertEqual(finalCount, baseline + 2)
    }

    func testInvalidTrafficDoesNotIncrementCount() async throws {
        let ack = TransportInstruction(protocolVersion: MoshWire.protocolVersion, ackNum: 0)
        var badTag = try datagram(instruction: ack, sequence: 1)
        badTag[badTag.index(before: badTag.endIndex)] ^= 1
        let invalidTraffic: [(String, Data)] = [
            ("empty datagram", Data()),
            ("truncated ciphertext", Data([1, 2, 3])),
            ("authentication failure", badTag),
            ("short authenticated plaintext", try seal(Data([0]), sequence: 1)),
            ("malformed fragment", try seal(Data(repeating: 0, count: 4), sequence: 1)),
            ("malformed instruction", try datagram(payload: Data([0x80]), sequence: 1)),
            ("protocol mismatch", try datagram(instruction: TransportInstruction(protocolVersion: 999), sequence: 1))
        ]

        for (name, invalid) in invalidTraffic {
            let (session, peer) = try await makeSession()
            try await peer.send(datagram(instruction: ack, sequence: 0))
            try await waitForCount(1, session: session)
            try await peer.send(invalid)
            var failure: MoshSessionFailure?
            for _ in 0..<100 {
                failure = await session.failure
                if failure != nil { break }
                try await Task.sleep(nanoseconds: 10_000_000)
            }
            XCTAssertNotNil(failure, name)
            let count = await session.peerResponseCount
            XCTAssertEqual(count, 1, name)
            await session.stop()
            await peer.stop()
        }
    }

    private func makeSession() async throws -> (MoshClientSession, InMemoryDatagramEndpoint) {
        let (client, peer) = await InMemoryDatagramPair.makeLinked()
        let session = MoshClientSession(
            endpoint: MoshEndpoint(host: "127.0.0.1", port: 60001, keyBase64_22: "AAECAwQFBgcICQoLDA0ODw"),
            config: MoshClientConfig(useNetworkCrypto: true),
            endpointFactory: { _, _ in client },
            snapshotEncoder: { try JSONEncoder().encode($0) }
        )
        addTeardownBlock {
            await session.stop()
            await peer.stop()
        }
        try await peer.start()
        try await session.start()
        return (session, peer)
    }

    private func datagram(
        instruction: TransportInstruction, sequence: UInt64, direction: MoshDirection = .toClient
    ) throws -> Data {
        try datagram(
            payload: MoshCompressionCodec().compress(instruction.encoded(), algorithm: .zlib),
            sequence: sequence, direction: direction
        )
    }

    private func datagram(
        payload: Data, sequence: UInt64, direction: MoshDirection = .toClient
    ) throws -> Data {
        let fragment = MoshFragment(id: sequence, fragmentNumber: 0, final: true, contents: payload)
        // Two timestamp fields, followed by one complete transport fragment.
        return try seal(Data([0, 0, 255, 255]) + fragment.encoded(), sequence: sequence, direction: direction)
    }

    private func seal(_ plaintext: Data, sequence: UInt64, direction: MoshDirection = .toClient) throws -> Data {
        let cipher = try OCBTransportCipher(key: Data((0..<16).map(UInt8.init)))
        return try cipher.seal(
            directionalSequence: MoshWire.directionalSequence(sequence: sequence, direction: direction),
            plaintext: plaintext
        )
    }

    private func waitForCount(_ expected: UInt64, session: MoshClientSession) async throws {
        for _ in 0..<100 {
            if await session.peerResponseCount >= expected { break }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        let count = await session.peerResponseCount
        XCTAssertEqual(count, expected)
    }
}
