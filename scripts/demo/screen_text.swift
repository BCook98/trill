// Local OCR for private screen verification; stdout is JSON only.
import Foundation
import ImageIO
import Vision

@main struct ScreenText {
    static func recognize(_ path: String) throws -> [String] {
        guard let source = CGImageSourceCreateWithURL(URL(fileURLWithPath: path) as CFURL, nil),
              let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else { return [] }
        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.usesLanguageCorrection = false
        request.recognitionLanguages = ["en-US"]
        try VNImageRequestHandler(cgImage: image).perform([request])
        return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }
    }
    static func emit(_ lines: [String]) throws {
        var data = try JSONSerialization.data(withJSONObject: lines)
        data.append(10)
        FileHandle.standardOutput.write(data)
    }
    static func main() throws {
        guard CommandLine.arguments.count == 2 else { exit(1) }
        if CommandLine.arguments[1] == "--server" {
            while let path = readLine() { try autoreleasepool { try emit(recognize(path)) } }
        } else { try emit(recognize(CommandLine.arguments[1])) }
    }
}
