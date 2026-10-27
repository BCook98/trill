#if !SWIFTTERM_EMBEDDED
import Foundation

/// A self-contained visible screen for state-synchronizing terminal transports.
/// This excludes scrollback, images and host-side effects such as clipboard writes.
public struct TerminalScreenState: Sendable, Hashable {
    public let columns: Int
    public let rows: Int
    public let restorationBytes: Data
    public let presentationBytes: Data
    public let inputModeBytes: Data

    init(content: TerminalContentSnapshot, parserState: String, inputModes: String) {
        columns = content.inputState.dimensions.cols
        rows = content.inputState.dimensions.rows
        // Establish an absolute coordinate system before repainting. Do not RIS:
        // a transport repaint must preserve the embedding view's history/settings.
        let bufferMode = "\u{1b}[?1049\(content.inputState.isAlternateBuffer ? "h" : "l")"
        var output = "\u{1b}[?6l\u{1b}[4l\u{1b}[r\u{1b}[?7h\u{1b}(B\u{f}\u{1b}[0m\u{1b}[2J"
        var previousAttribute: Attribute?
        for (rowIndex, row) in content.rows.enumerated() {
            if rowIndex == 0 || !row.isWrapped { output += "\u{1b}[\(rowIndex + 1);1H" }
            for cell in row.cells where cell.width > 0 {
                if cell.attribute != previousAttribute {
                    output += cell.attribute.screenStateSGR
                    previousAttribute = cell.attribute
                }
                output += cell.text.isEmpty || cell.text == "\u{0}" ? " " : cell.text
            }
        }
        output += parserState
        presentationBytes = Data(output.utf8)
        inputModeBytes = Data((bufferMode + inputModes).utf8)
        restorationBytes = Data((bufferMode + output + inputModes).utf8)
    }
}

extension Attribute {
    var screenStateSGR: String {
        var parameters = ["0"]
        for (flag, code) in [(CharacterStyle.bold, "1"), (.dim, "2"), (.italic, "3"),
                             (.blink, "5"), (.inverse, "7"), (.invisible, "8"), (.crossedOut, "9")] {
            if style.contains(flag) { parameters.append(code) }
        }
        if underlineStyle != .none { parameters.append("4:\(underlineStyle.rawValue)") }
        else if style.contains(.underline) { parameters.append("4") }
        parameters.append(screenStateColor(fg, foreground: true))
        parameters.append(screenStateColor(bg, foreground: false))
        if let underlineColor {
            switch underlineColor {
            case .ansi256(let code): parameters.append("58;5;\(code)")
            case .trueColor(let red, let green, let blue): parameters.append("58;2;\(red);\(green);\(blue)")
            default: parameters.append("59")
            }
        }
        return "\u{1b}[" + parameters.joined(separator: ";") + "m"
    }

    private func screenStateColor(_ color: Color, foreground: Bool) -> String {
        switch color {
        case .ansi256(let code): return "\(foreground ? 38 : 48);5;\(code)"
        case .trueColor(let red, let green, let blue): return "\(foreground ? 38 : 48);2;\(red);\(green);\(blue)"
        default: return foreground ? "39" : "49"
        }
    }
}
#endif
