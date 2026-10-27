import Foundation
import SwiftTerm

/// SSP diffs describe a screen relative to oldNum, not an ordered byte stream.
/// Keep self-contained grid checkpoints so branches never inherit pixels/cursor
/// state from another update. Bytes are also portable across suspend/restore.
struct MoshRemoteScreen: Codable, Hashable, Sendable {
    var columns: Int
    var rows: Int
    var restorationBytes: Data
    var presentationBytes: Data
    var inputModeBytes: Data

    static let initial = MoshRemoteScreen(columns: 80, rows: 24, restorationBytes: Data(),
                                        presentationBytes: Data(), inputModeBytes: Data()).applying([])

    func applying(_ operations: [MoshHostOp]) -> MoshRemoteScreen {
        let delegate = MoshScreenDelegate()
        let terminal = Terminal(delegate: delegate, options: TerminalOptions(cols: columns, rows: rows, scrollback: 0))
        terminal.terminalLock.withLock {
            if !restorationBytes.isEmpty { terminal.feed(buffer: Array(restorationBytes)[...]) }
            for operation in operations {
                switch operation {
                case .hostBytes(let bytes): terminal.feed(buffer: Array(bytes)[...])
                case .resize(let columns, let rows): terminal.resize(cols: Int(columns), rows: Int(rows))
                case .echoAck: break
                }
            }
        }
        let screen = terminal.screenState()
        return MoshRemoteScreen(columns: screen.columns, rows: screen.rows, restorationBytes: screen.restorationBytes,
                                presentationBytes: screen.presentationBytes, inputModeBytes: screen.inputModeBytes)
    }
}

private final class MoshScreenDelegate: TerminalDelegate {
    // A remote display diff never solicits input. Do not turn parsing a saved
    // screen into new keyboard/network input or replay clipboard side effects.
    func send(source: Terminal, data: ArraySlice<UInt8>) {}
}
