import Foundation
import XCTest
import SwiftTerm
import MoshProtoLite
import MoshTransport
import MoshWire
import MoshCryptoOCB
import MoshCompression
@testable import MoshCore

final class RemoteScreenStateTests: XCTestCase {
    func testCheckpointPreservesDeferredWrapAfterASCIIAndWideCharacters() {
        for text in ["ABCD", "AB界"] {
            let delegate = ScreenTestDelegate()
            let options = TerminalOptions(cols: 4, rows: 3, scrollback: 0)
            let original = Terminal(delegate: delegate, options: options)
            original.terminalLock.withLock { original.feed(buffer: Array(text.utf8)[...]) }
            let checkpoint = original.screenState()
            let restored = Terminal(delegate: delegate, options: options)
            restored.terminalLock.withLock { restored.feed(buffer: Array(checkpoint.restorationBytes)[...]) }
            for terminal in [original, restored] {
                terminal.terminalLock.withLock { terminal.feed(buffer: Array("E".utf8)[...]) }
            }
            let expected = original.contentSnapshot(region: .viewport)
            let actual = restored.contentSnapshot(region: .viewport)
            XCTAssertEqual(actual.rows.map { $0.text.trimmingCharacters(in: .whitespaces) },
                           expected.rows.map { $0.text.trimmingCharacters(in: .whitespaces) })
            XCTAssertEqual(actual.cursorColumn, expected.cursorColumn)
            XCTAssertEqual(actual.cursorRow, expected.cursorRow)
        }
    }

    func testReturningToInitialStateWithAnEmptyDeltaClearsTheDisplay() async throws {
        let session = makeSession()
        let delegate = ScreenTestDelegate()
        let display = Terminal(delegate: delegate)
        feed(try await update(session, old: 0, new: 1, text: "VISIBLE"), into: display)
        feed(try await session._testDecodeHostOps(instruction: TransportInstruction(
            protocolVersion: 2, oldNum: 0, newNum: 2, diff: Data())), into: display)
        XCTAssertEqual(firstRow(display), "")
    }

    func testRepaintingStableMouseModesDoesNotRestartTheGesture() async throws {
        let session = makeSession()
        _ = try await update(session, old: 0, new: 1, text: "\u{1b}[?1000h\u{1b}[?1006h")
        _ = try await update(session, old: 1, new: 2, text: "a")
        let repaired = try await update(session, old: 1, new: 3, text: "b")
        guard case .hostBytes(let bytes) = repaired.last else { return XCTFail("Missing repair") }
        XCTAssertFalse(String(decoding: bytes, as: UTF8.self).contains("\u{1b}[?1000"))
        XCTAssertFalse(String(decoding: bytes, as: UTF8.self).contains("\u{1b}[?1049"))
    }

    func testInvalidRemoteDimensionsFailBeforeAllocatingAScreen() async throws {
        let session = makeSession()
        do {
            _ = try await session._testDecodeHostOps(instruction: TransportInstruction(protocolVersion: 2,
                oldNum: 0, newNum: 1, diff: HostMessage(instructions: [.resize(width: Int32.max, height: 24)]).encoded()))
            XCTFail("Invalid remote dimensions must be rejected")
        } catch {
            let snapshot = try await session.makeSnapshot()
            let state = try JSONDecoder().decode(SessionStateBlob.self, from: snapshot.transportState)
            XCTAssertEqual(state.latestReceivedStateNum, 0)
        }
    }

    func testRecordedHerdrTrafficRepairsTheLegacyDisplayCorruption() async throws {
        guard let path = ProcessInfo.processInfo.environment["TRILL_MOSH_CAPTURE_PATH"] else {
            throw XCTSkip("Set TRILL_MOSH_CAPTURE_PATH to a private owned-lab packet recording")
        }
        let records = try String(contentsOfFile: path, encoding: .utf8).split(separator: "\n").map {
            try JSONSerialization.jsonObject(with: Data($0.utf8)) as! [String: Any]
        }
        let leaseIndex = try XCTUnwrap(records.lastIndex { $0["kind"] as? String == "lease" })
        let header = records[leaseIndex]
        let key = try MoshBase64Key(printableKey: XCTUnwrap(header["key"] as? String))
        let size = try XCTUnwrap(header["size"] as? [String: Int])
        let delegate = ScreenTestDelegate()
        let options = TerminalOptions(cols: size["cols"] ?? 80, rows: size["rows"] ?? 24, scrollback: 0)
        let legacyDisplay = Terminal(delegate: delegate, options: options)
        let fixedDisplay = Terminal(delegate: delegate, options: options)
        let session = makeSession()
        let cipher = try OCBTransportCipher(key: key.raw)
        var assembly = MoshFragmentAssembly()
        var expectedSequence: UInt64 = 0
        var legacyLatest: UInt64 = 0
        var legacyStates: Set<UInt64> = []
        var updates = 0
        var legacyCorruptionObserved = false
        var fixedCorruptionObserved = false
        func hasStrayDigits(_ terminal: Terminal) -> Bool {
            terminal.contentSnapshot(region: .viewport).rows.contains {
                $0.text.range(of: "Trill\\s*[0-9]+\\s*Herdr", options: .regularExpression) != nil
            }
        }
        for record in records.dropFirst(leaseIndex + 1) where record["direction"] as? String == "inbound" {
            let datagram = try XCTUnwrap(Data(base64Encoded: XCTUnwrap(record["data"] as? String)))
            let (directionalSequence, plaintext) = try cipher.open(datagram: datagram)
            let (sequence, direction) = MoshWire.splitDirectionalSequence(directionalSequence)
            guard direction == .toClient, sequence >= expectedSequence else { continue }
            let fragment = try MoshFragment(decoding: Data(plaintext.dropFirst(4)))
            guard assembly.add(fragment) else { continue }
            expectedSequence = sequence + 1
            let compressed = try assembly.assembled()
            let instruction = try TransportInstruction(decoding: MoshCompressionCodec().decompress(compressed, algorithm: .zlib))
            feed(try await session._testDecodeHostOps(instruction: instruction), into: fixedDisplay)
            fixedCorruptionObserved = fixedCorruptionObserved || hasStrayDigits(fixedDisplay)
            guard let number = instruction.newNum, !legacyStates.contains(number),
                  (instruction.oldNum ?? 0) <= legacyLatest else { continue }
            legacyStates.insert(number); legacyLatest = max(number, legacyLatest)
            if let diff = instruction.diff, let host = try? HostMessage(decoding: diff) {
                feed(host.instructions.compactMap { instruction in
                    if case .hostBytes(let bytes) = instruction { return .hostBytes(bytes) }
                    if case .resize(let columns, let rows) = instruction { return .resize(cols: columns, rows: rows) }
                    return nil
                }, into: legacyDisplay)
            }
            updates += 1
            legacyCorruptionObserved = legacyCorruptionObserved || hasStrayDigits(legacyDisplay)
        }
        XCTAssertGreaterThan(updates, 20)
        XCTAssertTrue(legacyCorruptionObserved, "The recorded traffic must reproduce the old receiver's corruption")
        XCTAssertFalse(fixedCorruptionObserved)
        print("HERDR_PACKET_REPLAY: \(updates) screen states; legacy corrupted=\(legacyCorruptionObserved); fixed corrupted=\(fixedCorruptionObserved)")
    }

    func testSharedReferenceRestoresCellsThatReturnToTheirOriginalValue() async throws {
        let session = makeSession()
        let delegate = ScreenTestDelegate()
        let display = Terminal(delegate: delegate)
        feed(try await update(session, old: 0, new: 1, text: "\u{1b}[HTrill Herdr"), into: display)
        feed(try await update(session, old: 1, new: 2, text: "\u{1b}[1;6H87"), into: display)
        XCTAssertEqual(firstRow(display), "Trill87erdr")
        // The host returned to state 1's text. Its delta only moves the cursor;
        // forwarding that delta onto state 2 would leave the stray digits behind.
        feed(try await update(session, old: 1, new: 3, text: "\u{1b}[H"), into: display)
        XCTAssertEqual(firstRow(display), "Trill Herdr")
    }

    func testSharedReferencePreservesCursorAndRendition() async throws {
        let session = makeSession()
        let delegate = ScreenTestDelegate()
        let display = Terminal(delegate: delegate)
        feed(try await update(session, old: 0, new: 1, text: "\u{1b}[31;3m\u{1b}[2;4H"), into: display)
        feed(try await update(session, old: 1, new: 2, text: "87"), into: display)
        feed(try await update(session, old: 1, new: 3, text: "OK"), into: display)
        let snapshot = display.contentSnapshot(region: .viewport)
        XCTAssertEqual(snapshot.rows[1].text.trimmingCharacters(in: .whitespaces), "OK")
        XCTAssertEqual(snapshot.rows[1].cells[3].attribute.fg, .ansi256(code: 1))
        XCTAssertTrue(snapshot.rows[1].cells[3].attribute.style.contains(.italic))
        XCTAssertEqual(snapshot.cursorColumn, 5)
    }

    func testOlderStateIsRetainedAsAReferenceWithoutRegressingTheDisplay() async throws {
        let session = makeSession()
        let delegate = ScreenTestDelegate()
        let display = Terminal(delegate: delegate)
        feed(try await update(session, old: 0, new: 1, text: "\u{1b}[HBASE"), into: display)
        feed(try await update(session, old: 1, new: 3, text: "\u{1b}[HNEW"), into: display)
        let older = try await update(session, old: 1, new: 2, text: "\u{1b}[HOLD")
        XCTAssertTrue(older.isEmpty)
        XCTAssertEqual(firstRow(display), "NEWE")
        feed(try await update(session, old: 2, new: 4, text: "\u{1b}[1;4H!"), into: display)
        XCTAssertEqual(firstRow(display), "OLD!")
    }

    func testSnapshotRestoreKeepsTheActualReferenceScreens() async throws {
        let session = makeSession()
        _ = try await update(session, old: 0, new: 1, text: "\u{1b}[HTrill Herdr")
        _ = try await update(session, old: 1, new: 2, text: "\u{1b}[1;6H87")
        let restored = try await MoshClientSession.restore(from: session.makeSnapshot())
        let delegate = ScreenTestDelegate()
        let display = Terminal(delegate: delegate)
        feed(try await update(restored, old: 1, new: 3, text: "\u{1b}[H"), into: display)
        XCTAssertEqual(firstRow(display), "Trill Herdr")
    }

    func testMissingReferenceIsNotAcknowledgedOrRendered() async throws {
        let session = makeSession()
        _ = try await update(session, old: 0, new: 5, text: "\u{1b}[HNEW")
        let missing = try await update(session, old: 3, new: 6, text: "WRONG")
        XCTAssertTrue(missing.isEmpty)
        let snapshot = try await session.makeSnapshot()
        let state = try JSONDecoder().decode(SessionStateBlob.self, from: snapshot.transportState)
        XCTAssertEqual(state.latestReceivedStateNum, 5)
    }

    func testScreenRoundTripPreservesWideTextColorsAndInputModes() {
        let delegate = ScreenTestDelegate()
        let original = Terminal(delegate: delegate, options: TerminalOptions(cols: 16, rows: 4, scrollback: 0))
        let bytes = Array("\u{1b}[?1049h\u{1b}[38;2;12;34;56;48;5;16;1;3;4m日本語 café 👩🏽‍💻\u{1b}[2;3H\u{1b}[?1000h\u{1b}[?1006h\u{1b}[?2004h".utf8)
        original.terminalLock.withLock { original.feed(buffer: bytes[...]) }
        let checkpoint = original.screenState()
        let restored = Terminal(delegate: delegate, options: TerminalOptions(cols: 16, rows: 4, scrollback: 0))
        restored.terminalLock.withLock { restored.feed(buffer: Array(checkpoint.restorationBytes)[...]) }
        let expected = original.contentSnapshot(region: .viewport)
        let actual = restored.contentSnapshot(region: .viewport)
        XCTAssertEqual(actual.rows.map { $0.text.trimmingCharacters(in: .whitespaces) },
                       expected.rows.map { $0.text.trimmingCharacters(in: .whitespaces) })
        func visibleCells(_ snapshot: TerminalContentSnapshot) -> [[TerminalCellSnapshot]] {
            snapshot.rows.map { row in row.cells.map { cell in
                TerminalCellSnapshot(text: cell.text == "\u{0}" || cell.text.isEmpty ? " " : cell.text,
                                     width: cell.width, attribute: cell.attribute)
            } }
        }
        XCTAssertEqual(visibleCells(actual), visibleCells(expected))
        XCTAssertEqual(actual.inputState, expected.inputState)
        XCTAssertEqual(actual.cursorColumn, expected.cursorColumn)
        XCTAssertEqual(actual.cursorRow, expected.cursorRow)
    }

    private func makeSession() -> MoshClientSession {
        MoshClientSession(endpoint: .init(host: "127.0.0.1", port: 60001, keyBase64_22: "AAECAwQFBgcICQoLDA0ODw"))
    }
    private func update(_ session: MoshClientSession, old: UInt64, new: UInt64, text: String) async throws -> [MoshHostOp] {
        try await session._testDecodeHostOps(instruction: TransportInstruction(protocolVersion: 2,
            oldNum: old, newNum: new, diff: HostMessage(instructions: [.hostBytes(Data(text.utf8))]).encoded()))
    }
    private func feed(_ operations: [MoshHostOp], into terminal: Terminal) {
        terminal.terminalLock.withLock {
            for operation in operations {
                switch operation {
                case .hostBytes(let bytes): terminal.feed(buffer: Array(bytes)[...])
                case .resize(let columns, let rows): terminal.resize(cols: Int(columns), rows: Int(rows))
                case .echoAck: break
                }
            }
        }
    }
    private func firstRow(_ terminal: Terminal) -> String {
        terminal.contentSnapshot(region: .viewport).rows[0].text.trimmingCharacters(in: .whitespaces)
    }
}
private final class ScreenTestDelegate: TerminalDelegate {
    func send(source: Terminal, data: ArraySlice<UInt8>) {}
}
