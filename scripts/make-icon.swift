import AppKit

// Trill Wave: the approved three-oscillation SVG, rendered without a corner mask.
// Usage: swift scripts/make-icon.swift output.png [--dark]
let size = 1024
let dark = CommandLine.arguments.contains("--dark")
let context = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8,
    bytesPerRow: 0, space: CGColorSpace(name: CGColorSpace.sRGB)!,
    bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
// Static icon snapshots of iOS secondarySystemBackground, light and dark.
// In-app surfaces use UIColor.secondarySystemBackground so UIKit resolves them.
let background = dark
    ? NSColor(srgbRed: 28.0 / 255, green: 28.0 / 255, blue: 30.0 / 255, alpha: 1)
    : NSColor(srgbRed: 242.0 / 255, green: 242.0 / 255, blue: 247.0 / 255, alpha: 1)
context.setFillColor(background.cgColor)
context.fill(CGRect(x: 0, y: 0, width: size, height: size))
// Match the SVG's translate(-10, 0) scale(1.4, 1), including its stroke scaling.
context.translateBy(x: -20, y: 1024)
context.scaleBy(x: 2.8, y: -2)
context.move(to: CGPoint(x: 100, y: 256))
context.addCurve(to: CGPoint(x: 126, y: 186), control1: CGPoint(x: 113, y: 256), control2: CGPoint(x: 113, y: 186))
context.addCurve(to: CGPoint(x: 152, y: 326), control1: CGPoint(x: 139, y: 186), control2: CGPoint(x: 139, y: 326))
context.addCurve(to: CGPoint(x: 178, y: 186), control1: CGPoint(x: 165, y: 326), control2: CGPoint(x: 165, y: 186))
context.addCurve(to: CGPoint(x: 204, y: 326), control1: CGPoint(x: 191, y: 186), control2: CGPoint(x: 191, y: 326))
context.addCurve(to: CGPoint(x: 230, y: 186), control1: CGPoint(x: 217, y: 326), control2: CGPoint(x: 217, y: 186))
context.addCurve(to: CGPoint(x: 256, y: 326), control1: CGPoint(x: 243, y: 186), control2: CGPoint(x: 243, y: 326))
context.addCurve(to: CGPoint(x: 282, y: 256), control1: CGPoint(x: 269, y: 326), control2: CGPoint(x: 269, y: 256))
let cobalt = dark
    ? NSColor(srgbRed: 124.0 / 255, green: 158.0 / 255, blue: 1, alpha: 1)
    : NSColor(srgbRed: 36.0 / 255, green: 73.0 / 255, blue: 219.0 / 255, alpha: 1)
context.setStrokeColor(cobalt.cgColor)
context.setLineWidth(20)
context.setLineCap(.round)
context.setLineJoin(.round)
context.strokePath()
let rep = NSBitmapImageRep(cgImage: context.makeImage()!)
try rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: CommandLine.arguments[1]))
