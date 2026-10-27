import Foundation
import SwiftTerm

struct ProbeFailure: Error { let message: String }
struct Lease: Decodable { let leaseId: String }
actor StreamClient {
    private var output = Data()
    private let socket: URLSessionWebSocketTask
    private var reader: Task<Void, Never>?
    private var failed = false
    init(request: URLRequest) { socket = URLSession.shared.webSocketTask(with: request) }
    func start() {
        socket.resume()
        reader = Task {
            do {
                while !Task.isCancelled {
                    let message = try await socket.receive()
                    let data: Data
                    switch message { case .string(let text): data = Data(text.utf8); case .data(let bytes): data = bytes; @unknown default: throw ProbeFailure(message: "Unknown frame") }
                    struct Frame: Decodable { let type: String; let bytes: Data? }
                    let frame = try JSONDecoder().decode(Frame.self, from: data)
                    guard frame.type == "terminal.frame", let bytes = frame.bytes else { throw ProbeFailure(message: "Stream ended") }
                    output.append(bytes)
                }
            } catch { failed = true }
        }
    }
    func drain() throws -> Data {
        guard !failed else { throw ProbeFailure(message: "Stream failed") }
        defer { output.removeAll(keepingCapacity: true) }; return output
    }
    func command(_ value: [String: Any]) async throws {
        let data = try JSONSerialization.data(withJSONObject: value)
        try await socket.send(.string(String(decoding: data, as: UTF8.self)))
    }
    func stop() { reader?.cancel(); socket.cancel(with: .normalClosure, reason: nil) }
}
@main struct HerdrTransportProbe {
    static func main() async {
        do { try await run() } catch { print("FAIL: \(error)"); exit(1) }
    }
    static func run() async throws {
        let arguments = CommandLine.arguments
        guard arguments.count == 4, let base = URL(string: arguments[1]), base.scheme == "http", base.host == "127.0.0.1" else { throw ProbeFailure(message: "Usage: HerdrTransportProbe http://127.0.0.1:PORT TOKEN_FILE SESSION_ID") }
        let token = try String(contentsOfFile: arguments[2], encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        func request(_ method: String, _ path: String, _ body: [String: Any]? = nil) async throws -> Data {
            var request = URLRequest(url: base.appendingPathComponent(path)); request.httpMethod = method
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            if let body { request.httpBody = try JSONSerialization.data(withJSONObject: body); request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let response = response as? HTTPURLResponse, (200..<300).contains(response.statusCode) else { throw ProbeFailure(message: "Request failed: \(path)") }
            return data
        }
        // Never accept personal terminals: only the companion's explicit /tmp lab.
        let health = try JSONSerialization.jsonObject(with: await request("GET", "v1/health")) as? [String: Any]
        let listing = try JSONSerialization.jsonObject(with: await request("GET", "v1/sessions")) as? [String: Any]
        let sessions = listing?["sessions"] as? [[String: Any]] ?? []
        guard health?["terminalLab"] as? Bool == true, health?["backend"] as? String == "herdr",
              sessions.contains(where: { $0["id"] as? String == arguments[3] && $0["projectId"] as? String == "herdr-lab" && ($0["agent"] as? String)?.lowercased() == "shell" }) else { throw ProbeFailure(message: "Refusing non-lab shell") }
        var results: [[String: Any]] = []
        for mode in ["herdr"] {
            var samples: [Double] = []
            var report: [String: Any] = ["transport": mode]
            for iteration in 0..<3 {
                let started = Date()
                let route = "terminal"
                let lease = try JSONDecoder().decode(Lease.self, from: await request("POST", "v1/sessions/\(arguments[3])/\(route)", ["cols": 80, "rows": 24]))
                var stream: StreamClient?
                do {
                        var url = URLComponents(url: base.appendingPathComponent("v1/leases/\(lease.leaseId)/terminal"), resolvingAgainstBaseURL: false)!
                        url.scheme = "ws"; var socketRequest = URLRequest(url: url.url!); socketRequest.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
                        let client = StreamClient(request: socketRequest); stream = client; await client.start()
                    let screen = HeadlessTerminal(onEnd: { _ in })
                    func drain() async throws -> Data {
                        var bytes = Data()
                        if let stream { bytes = try await stream.drain() }
                        screen.terminal.feed(byteArray: Array(bytes))
                        return bytes
                    }
                    func send(_ text: String) async throws {
                        if let stream { try await stream.command(["type":"terminal.input", "bytes": Data(text.utf8).base64EncodedString()]) }
                    }
                    func expect(_ marker: String, timeout: Double = 10) async throws -> Data {
                        let deadline = Date().addingTimeInterval(timeout); var bytes = Data()
                        while Date() < deadline {
                            bytes.append(try await drain())
                            let plain = String(decoding: screen.terminal.getBufferAsData(kind: .active, encoding: .utf8), as: UTF8.self)
                            if marker.isEmpty ? !bytes.isEmpty : plain.contains(marker) { return bytes }
                            try await Task.sleep(for: .milliseconds(2))
                        }
                        throw ProbeFailure(message: "\(mode) timed out waiting for \(marker)")
                    }
                    _ = try await expect("")
                    if iteration == 0 { report["attachToFirstFrameMs"] = Date().timeIntervalSince(started)*1000 }
                    for sample in 0..<10 {
                        _ = try await drain()
                        let before = Date(); let suffix = "\(mode)_\(iteration)_\(sample)"
                        try await send("printf 'ROUND_%s_END\\n' '\(suffix)'\r")
                        _ = try await expect("ROUND_\(suffix)_END")
                        samples.append(Date().timeIntervalSince(before)*1000)
                    }
                    if iteration == 0 {
                        if let stream { try await stream.command(["type":"terminal.resize","cols":93,"rows":31]) }
                        screen.terminal.resize(cols: 93, rows: 31)
                        try await Task.sleep(for: .milliseconds(300))
                        try await send("set -- $(stty size); printf 'GEOMETRY_%s_%s_END\\n' \"$1\" \"$2\"\r")
                        _ = try await expect("GEOMETRY_31_93_END"); report["resize"] = true
                        try await send("TRILL_COMPARE_STATE=retained; printf '\\033[2J\\033[H\\033]8;;https://example.com/hidden\\033\\\\Repository\\033]8;;\\033\\\\\\nLINK_%s_END\\n' CHECK\r")
                        let linkOutput = try await expect("LINK_CHECK_END")
                        report["labeledHyperlinkPreserved"] = linkOutput.range(of: Data("\u{1b}]8;;https://example.com/hidden".utf8)) != nil
                        if let stream {
                            try await send("i=1; while [ $i -le 100 ]; do printf 'HISTORY_%04d\\n' $i; i=$((i+1)); done; printf 'HISTORY_%s_END\\n' COMPLETE\r")
                            _ = try await expect("HISTORY_COMPLETE_END")
                            try await stream.command(["type":"terminal.scroll","direction":"up","lines":60,"column":0,"row":0])
                            _ = try await expect("HISTORY_0040")
                            report["historyScroll"] = "passed: explicit scroll command"
                            try await stream.command(["type":"terminal.scroll","direction":"down","lines":100,"column":0,"row":0])
                            _ = try await expect("HISTORY_COMPLETE_END")
                        }
                    } else {
                        try await send("printf 'PERSIST_%s_END\\n' \"$TRILL_COMPARE_STATE\"\r")
                        _ = try await expect("PERSIST_retained_END"); report["reattachPreservesShell"] = true
                    }
                    await stream?.stop()
                    _ = try await request("DELETE", "v1/leases/\(lease.leaseId)")
                } catch {
                    await stream?.stop()
                    _ = try? await request("DELETE", "v1/leases/\(lease.leaseId)")
                    throw error
                }
            }
            samples.sort()
            report["roundTrips"] = samples.count
            report["roundTripMedianMs"] = samples[samples.count/2]
            report["roundTripP95Ms"] = samples[Int(Double(samples.count-1)*0.95)]
            results.append(report)
        }
        print(String(decoding: try JSONSerialization.data(withJSONObject: ["scope":"loopback disposable shell; 3 stream attachments; no physical-device roaming", "results":results], options:[.prettyPrinted,.sortedKeys]), as: UTF8.self))
    }
}
