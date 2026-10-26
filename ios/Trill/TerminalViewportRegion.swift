import SwiftUI

/// Keep a terminal's rectangular character grid clear of an active fold or camera.
/// Native navigation still owns the bars, sidebar, and safe-area insets.
struct TerminalViewportRegion<Content: View>: View {
    @ViewBuilder var content: () -> Content

    var body: some View {
        GeometryReader { geometry in
            let region = visibleRegion(in: geometry)
            content()
                .frame(width: region.width, height: region.height)
                .clipped()
                .position(x: region.midX, y: region.midY)
        }
    }

    private func visibleRegion(in geometry: GeometryProxy) -> CGRect {
        let bounds = CGRect(origin: .zero, size: geometry.size)
        if #available(iOS 27.1, *) {
            let regions = geometry.reservedRegions(kind: .division)
                + geometry.reservedRegions(kind: .occlusion)
            // The public frame already includes the system's reserved margins.
            let exclusions = regions.map(\.frame)
            return TerminalViewportLayout.visibleRect(in: bounds, excluding: exclusions)
        }
        return bounds
    }
}

enum TerminalViewportLayout {
    /// Prefer the largest uninterrupted rectangle. Equal regions prefer the top
    /// for tabletop reading, then the right for book-to-cover continuity.
    static func visibleRect(in bounds: CGRect, excluding exclusions: [CGRect]) -> CGRect {
        guard bounds.width > 0, bounds.height > 0 else { return .zero }
        var candidates = [bounds]
        for exclusion in exclusions where !exclusion.isNull && !exclusion.isEmpty {
            candidates = candidates.flatMap { rect -> [CGRect] in
                let cut = rect.intersection(exclusion)
                guard !cut.isNull, !cut.isEmpty else { return [rect] }
                return [
                    CGRect(x: rect.minX, y: rect.minY, width: cut.minX - rect.minX, height: rect.height),
                    CGRect(x: cut.maxX, y: rect.minY, width: rect.maxX - cut.maxX, height: rect.height),
                    CGRect(x: rect.minX, y: rect.minY, width: rect.width, height: cut.minY - rect.minY),
                    CGRect(x: rect.minX, y: cut.maxY, width: rect.width, height: rect.maxY - cut.maxY)
                ].filter { $0.width > 0 && $0.height > 0 }
            }
        }
        return candidates.max { a, b in
            let aArea = a.width * a.height, bArea = b.width * b.height
            if aArea != bArea { return aArea < bArea }
            if a.minY != b.minY { return a.minY > b.minY }
            return a.minX < b.minX
        } ?? .zero
    }
}

/// One layout owns output and input so a tabletop fold cannot put controls in
/// the reading region. The terminal UIView stays in the same structural slot.
struct TerminalInteractionRegion<Output: View, Controls: View>: View {
    @Binding var tabletop: Bool
    @State private var keyboardTop: CGFloat = .greatestFiniteMagnitude
    @ViewBuilder var output: () -> Output
    @ViewBuilder var controls: (Bool) -> Controls

    var body: some View {
        GeometryReader { geometry in
            let divisions = divisions(in: geometry)
            let occlusions = occlusions(in: geometry)
            let horizontal = divisions.contains { $0.width > $0.height && $0.height > 0 }
            TerminalInteractionLayout(divisions: divisions, occlusions: occlusions, keyboardTop: keyboardTop) {
                output().clipped()
                controls(horizontal)
            }
            .background(KeyboardBoundaryReader { keyboardTop = $0 })
            .onChange(of: horizontal, initial: true) { _, value in tabletop = value }
        }
        // Duo may inset the whole lower display for the keyboard. Use UIKit's
        // keyboard boundary and active fold together to place the input row.
        .ignoresSafeArea(.keyboard)
    }

    private func divisions(in geometry: GeometryProxy) -> [CGRect] {
        if #available(iOS 27.1, *) { return geometry.reservedRegions(kind: .division).map(\.frame) }
        return []
    }
    private func occlusions(in geometry: GeometryProxy) -> [CGRect] {
        if #available(iOS 27.1, *) { return geometry.reservedRegions(kind: .occlusion).map(\.frame) }
        return []
    }
}

private struct TerminalInteractionLayout: Layout {
    let divisions: [CGRect]
    let occlusions: [CGRect]
    let keyboardTop: CGFloat
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        proposal.replacingUnspecifiedDimensions()
    }
    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard subviews.count == 2 else { return }
        // Controls have a bounded height; measure at the clear region’s width.
        let local = CGRect(origin: .zero, size: bounds.size)
        let area = TerminalViewportLayout.controlsArea(in: local, divisions: divisions, occlusions: occlusions)
        let height = subviews[1].sizeThatFits(ProposedViewSize(width: area.width, height: nil)).height
        let regions = TerminalViewportLayout.interactionRegions(in: local, divisions: divisions,
                                                                 occlusions: occlusions, controlsHeight: height, keyboardTop: keyboardTop)
        for (index, rect) in [regions.output, regions.controls].enumerated() {
            subviews[index].place(at: CGPoint(x: bounds.minX + rect.minX, y: bounds.minY + rect.minY),
                                  anchor: .topLeading, proposal: ProposedViewSize(rect.size))
        }
    }
}

extension TerminalViewportLayout {
    struct InteractionRegions: Equatable {
        let output: CGRect
        let controls: CGRect
    }
    static func controlsArea(in bounds: CGRect, divisions: [CGRect], occlusions: [CGRect]) -> CGRect {
        let fold = divisions.map { bounds.intersection($0) }.first {
            !$0.isNull && !$0.isEmpty && $0.width >= bounds.width * 0.8 && $0.width > $0.height && $0.minY > bounds.minY
        }
        let lower = fold.map { CGRect(x: bounds.minX, y: $0.maxY, width: bounds.width, height: max(0, bounds.maxY - $0.maxY)) } ?? bounds
        return visibleRect(in: lower, excluding: divisions + occlusions)
    }
    static func interactionRegions(in bounds: CGRect, divisions: [CGRect], occlusions: [CGRect], controlsHeight: CGFloat, keyboardTop: CGFloat = .greatestFiniteMagnitude) -> InteractionRegions {
        guard bounds.width > 0, bounds.height > 0 else { return .init(output: .zero, controls: .zero) }
        if controlsHeight <= 0 {
            return .init(output: visibleRect(in: bounds, excluding: divisions + occlusions), controls: .zero)
        }
        // In tabletop posture iOS reserves the entire lower display for input.
        // Keep controls at that display's leading edge, outside the fold, while
        // the system keyboard occupies the lower portion of the same display.
        if let fold = divisions.first(where: { $0.width >= bounds.width * 0.8 && $0.width > $0.height && $0.minY > bounds.minY && $0.maxY < bounds.maxY }),
           keyboardTop < bounds.maxY, keyboardTop <= fold.maxY {
            let area = controlsArea(in: bounds, divisions: divisions, occlusions: occlusions)
            let controls = CGRect(x: area.minX, y: area.minY, width: area.width, height: min(controlsHeight, area.height))
            let upper = CGRect(x: bounds.minX, y: bounds.minY, width: bounds.width, height: fold.minY - bounds.minY)
            return .init(output: visibleRect(in: upper, excluding: occlusions), controls: controls)
        }
        if keyboardTop < bounds.maxY {
            let visible = CGRect(x: bounds.minX, y: bounds.minY, width: bounds.width, height: max(0, keyboardTop - bounds.minY))
            return interactionRegions(in: visible, divisions: divisions, occlusions: occlusions, controlsHeight: controlsHeight)
        }
        let cuts = divisions.map { bounds.intersection($0) }.filter { !$0.isNull && !$0.isEmpty }
        let fold = cuts.first { $0.width >= bounds.width * 0.8 && $0.width > $0.height && $0.minY > bounds.minY }
        var availableControls = controlsArea(in: bounds, divisions: divisions, occlusions: occlusions)
        // A floating keyboard, window resize, or accessibility text can leave
        // too little lower space. Keep the entire strip reachable above the fold.
        if let fold, availableControls.height < controlsHeight {
            let upper = CGRect(x: bounds.minX, y: bounds.minY, width: bounds.width, height: fold.minY - bounds.minY)
            availableControls = visibleRect(in: upper, excluding: divisions + occlusions)
        }
        let height = min(max(0, controlsHeight), availableControls.height)
        let controls = CGRect(x: availableControls.minX, y: availableControls.maxY - height,
                              width: availableControls.width, height: height)
        let outputEnd = min(fold?.minY ?? controls.minY, controls.minY)
        let outputBounds = CGRect(x: bounds.minX, y: bounds.minY, width: bounds.width,
                                  height: max(0, outputEnd - bounds.minY))
        let output = visibleRect(in: outputBounds, excluding: divisions + occlusions)
        return .init(output: output, controls: controls)
    }
}


/// Convert keyboard geometry through the current window; no device dimensions
/// or global-screen assumptions, and no rebuilding the terminal to move keys.
private struct KeyboardBoundaryReader: UIViewRepresentable {
    let changed: (CGFloat) -> Void
    func makeUIView(context: Context) -> KeyboardBoundaryView {
        let view = KeyboardBoundaryView()
        view.changed = changed
        view.isUserInteractionEnabled = false
        return view
    }
    func updateUIView(_ view: KeyboardBoundaryView, context: Context) { view.changed = changed }
}
private final class KeyboardBoundaryView: UIView {
    var changed: ((CGFloat) -> Void)?
    private var lastTop: CGFloat = -1
    private var keyboardFrame: CGRect?
    override init(frame: CGRect) {
        super.init(frame: frame)
        NotificationCenter.default.addObserver(self, selector: #selector(keyboardChanged(_:)), name: UIResponder.keyboardWillChangeFrameNotification, object: nil)
        NotificationCenter.default.addObserver(self, selector: #selector(keyboardHidden), name: UIResponder.keyboardWillHideNotification, object: nil)
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }
    deinit { NotificationCenter.default.removeObserver(self) }
    @objc private func keyboardChanged(_ notification: Notification) {
        keyboardFrame = notification.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect
        setNeedsLayout()
    }
    @objc private func keyboardHidden() { keyboardFrame = nil; setNeedsLayout() }
    override func layoutSubviews() {
        super.layoutSubviews()
        var top = bounds.height
        if let window, let keyboardFrame {
            let inWindow = window.convert(keyboardFrame, from: window.screen.coordinateSpace)
            let overlap = bounds.intersection(convert(inWindow, from: window))
            if !overlap.isNull, overlap.width >= bounds.width * 0.8 { top = max(0, min(bounds.height, overlap.minY)) }
        }
        guard abs(top - lastTop) > 0.5 else { return }
        lastTop = top
        DispatchQueue.main.async { [weak self] in
            guard let self, self.lastTop == top else { return }
            self.changed?(top)
        }
    }
}
