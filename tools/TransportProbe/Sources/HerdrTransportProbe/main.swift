import Foundation
import MoshCore

struct Failure: Error, CustomStringConvertible { let description: String }
struct Health: Decodable { let backend: String; let terminalLab: Bool; let sessionManagement: Bool }
struct Session: Decodable { let id: String; let projectId: String; let agent: String; let attachable: Bool }
struct Listing: Decodable { let sessions: [Session] }
struct Lease: Decodable { let host: String; let port: UInt16; let key: String; let leaseId: String }

// Deliberately separate from TransportProbe: this accepts only an opted-in Herdr
// lab, never the ordinary Herdr backend, Xirp, or an arbitrary supplied terminal.
@main struct Probe {
    static func main() async {
        do { try await run() }
        catch {
            FileHandle.standardError.write(Data("FAIL: \(error)\n".utf8))
            exit(1)
        }
    }
    static func run() async throws {
        let args = CommandLine.arguments
        guard args.count == 5, let base = URL(string: args[1]), base.scheme == "http",
              base.host == "127.0.0.1", ["shell", "claude", "codex"].contains(args[4]) else {
            throw Failure(description: "Usage: HerdrTransportProbe http://127.0.0.1:PORT TOKEN_FILE SESSION_ID shell|claude|codex")
        }
        let token = try String(contentsOfFile: args[2], encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        func request(_ method: String, _ path: String, _ body: [String: Int]? = nil) async throws -> Data {
            var req = URLRequest(url: base.appendingPathComponent(path))
            req.httpMethod = method
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            if let body {
                req.httpBody = try JSONSerialization.data(withJSONObject: body)
                req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            }
            let (data, response) = try await URLSession.shared.data(for: req)
            guard let response = response as? HTTPURLResponse, (200..<300).contains(response.statusCode) else {
                throw Failure(description: "Sidecar request failed: \(path)")
            }
            return data
        }
        let health = try JSONDecoder().decode(Health.self, from: await request("GET", "v1/health"))
        let listing = try JSONDecoder().decode(Listing.self, from: await request("GET", "v1/sessions"))
        let expectedAgent = args[4]
        guard health.backend == "herdr", health.terminalLab, !health.sessionManagement,
              listing.sessions.contains(where: { $0.id == args[3] && $0.projectId == "herdr-lab" && $0.agent.lowercased() == expectedAgent && $0.attachable }) else {
            throw Failure(description: "Refusing input: target is not the expected agent in an opted-in Herdr lab")
        }
        var lease: Lease?
        var client: MoshClientSession?
        func connect() async throws -> MoshClientSession {
            let next = try JSONDecoder().decode(Lease.self, from: await request("POST", "v1/sessions/\(args[3])/mosh", ["cols": 80, "rows": 24]))
            lease = next
            let connection = MoshClientSession(endpoint: .init(host: next.host, port: next.port, keyBase64_22: next.key))
            client = connection
            try await connection.start()
            try await connection.enqueue(.resize(cols: 80, rows: 24))
            try await Task.sleep(for: .seconds(1))
            return connection
        }
        func send(_ text: String, to client: MoshClientSession) async throws {
            try await client.enqueue(.keystrokes(Data(text.utf8)))
        }
        func release() async throws {
            if let client { await client.stop() }
            if let lease { _ = try await request("DELETE", "v1/leases/\(lease.leaseId)") }
            lease = nil; client = nil
        }
        do {
            let first = try await connect()
            if args[4] == "shell" {
                try await send("TRILL_PERSIST=swift; printf 'SWIFT_%s_OK\\n' MOSH\r", to: first)
                try await expect(first, marker: "SWIFT_MOSH_OK")
                print("PASS Swift MoshCore → Go lease runner → Herdr shell input/output")
                try await first.enqueue(.resize(cols: 93, rows: 31))
                try await Task.sleep(for: .milliseconds(400))
                try await send("set -- $(stty size); printf 'SIZE_%s_%s\\n' \"$1\" \"$2\"\r", to: first)
                try await expect(first, marker: "SIZE_31_93")
                print("PASS Swift resize reaches Herdr shell (31×93)")
                _ = try await first.prepareForApplicationBackground()
                try await first.resumeFromApplicationBackground()
                try await send("printf 'SWIFT_%s_OK\\n' RESUME\r", to: first)
                try await expect(first, marker: "SWIFT_RESUME_OK")
                print("PASS Swift transport suspend/resume")
            } else {
                // The response marker never appears verbatim in the submitted text.
                let prompt = "Reply with TRILL followed immediately by _AGENT_OK, without spaces. Do not use tools."
                try await send("\u{1b}[200~\(prompt)\u{1b}[201~\r", to: first)
                try await expect(first, marker: "TRILL_AGENT_OK", timeout: 90)
                print("PASS full-screen \(args[4]) prompt/response via Swift mosh and bracketed paste")
                try await first.enqueue(.resize(cols: 93, rows: 31))
                _ = try await first.prepareForApplicationBackground()
            }
            // Match Trill foreground recovery: end the old transport and release
            // its server before bootstrapping a new connection to the same terminal.
            try await release()
            let fresh = try await connect()
            if args[4] == "shell" {
                try await send("printf 'FRESH_%s\\n' \"$TRILL_PERSIST\"\r", to: fresh)
                try await expect(fresh, marker: "FRESH_swift")
                print("PASS fresh Go lease preserves shell process state without takeover")
            } else {
                try await send("\u{1b}[200~Reply with TRILL followed immediately by _RECOVERED_OK, without spaces. Do not use tools.\u{1b}[201~\r", to: fresh)
                try await expect(fresh, marker: "TRILL_RECOVERED_OK", timeout: 90)
                print("PASS \(args[4]) responds after Swift transport/Go lease replacement")
            }
            try await release()
            print("PASS lease cleanup; Herdr retains ownership of the terminal")
        } catch {
            try? await release()
            throw error
        }
    }
    static func expect(_ client: MoshClientSession, marker: String, timeout: TimeInterval = 15) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        var output = ""
        while Date() < deadline {
            for op in await client.drainHostOps() {
                if case .hostBytes(let data) = op { output += String(decoding: data, as: UTF8.self) }
            }
            let plain = output.replacingOccurrences(of: "\u{1b}\\[[0-?]*[ -/]*[@-~]", with: "", options: .regularExpression)
            if plain.contains(marker) { return }
            try await Task.sleep(for: .milliseconds(50))
        }
        throw Failure(description: "Timed out waiting for \(marker); rendered tail: \(output.suffix(1200))")
    }
}
