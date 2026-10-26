import XCTest
import SwiftUI
import UIKit
import MoshCore
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
