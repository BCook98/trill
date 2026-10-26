import Foundation
import MoshCore

struct Lease: Decodable { let host: String; let port: UInt16; let key: String; let leaseId: String }
struct LabSession: Decodable { let id: String; let projectId: String; let agent: String }
struct LabSessions: Decodable { let sessions: [LabSession] }
struct ProbeFailure: Error, CustomStringConvertible { let description: String }

// Only use with scripts/start-lab.sh. The probe sends shell commands to the supplied lab session.
@main struct Probe {
    static func main() async throws {
        let args = CommandLine.arguments
        guard args.count == 4, let base = URL(string: args[1]), base.host == "127.0.0.1" else {
            throw ProbeFailure(description: "Usage: TransportProbe http://127.0.0.1:PORT TOKEN_FILE LAB_SESSION_ID")
        }
        let token = try String(contentsOfFile: args[2], encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        func request(_ method: String, _ path: String, _ body: [String: Int]? = nil) async throws -> Data {
            var req = URLRequest(url: base.appendingPathComponent(path)); req.httpMethod = method
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            if let body { req.httpBody = try JSONSerialization.data(withJSONObject: body); req.setValue("application/json", forHTTPHeaderField: "Content-Type") }
            let (data, response) = try await URLSession.shared.data(for: req)
            guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else { throw ProbeFailure(description: "Sidecar request failed: \(path)") }
            return data
        }
        let listing = try JSONDecoder().decode(LabSessions.self, from: await request("GET", "v1/sessions"))
        guard listing.sessions.contains(where: { $0.id == args[3] && $0.projectId == "terminal-lab" && $0.agent == "Shell" }) else {
            throw ProbeFailure(description: "Refusing input: target is not an isolated terminal-lab shell")
        }
        let lease = try JSONDecoder().decode(Lease.self, from: await request("POST", "v1/sessions/\(args[3])/mosh", ["cols": 80, "rows": 24]))
        let client = MoshClientSession(endpoint: .init(host: lease.host, port: lease.port, keyBase64_22: lease.key))
        do {
            try await client.start()
            try await client.enqueue(.resize(cols: 80, rows: 24))
            try await client.enqueue(.keystrokes(Data("printf 'TRILL_%s_OK\\n' 'MOSH'\n".utf8)))
            try await expect(client, marker: "TRILL_MOSH_OK")
            try await client.enqueue(.resize(cols: 93, rows: 31))
            try await Task.sleep(for: .milliseconds(300))
            try await client.enqueue(.keystrokes(Data("stty size\n".utf8)))
            try await expect(client, marker: "30 93")
            _ = try await client.prepareForApplicationBackground()
            try await client.resumeFromApplicationBackground()
            try await client.enqueue(.keystrokes(Data("printf 'TRILL_%s_OK\\n' 'RESUME'\n".utf8)))
            try await expect(client, marker: "TRILL_RESUME_OK")
            await client.stop()
            _ = try await request("DELETE", "v1/leases/\(lease.leaseId)")
            print("PASS: sidecar bootstrap → encrypted mosh → real tmux; input/output, resize 93×31, suspend/resume, lease disconnect")
        } catch {
            await client.stop()
            _ = try? await request("DELETE", "v1/leases/\(lease.leaseId)")
            throw error
        }
    }
    static func expect(_ client: MoshClientSession, marker: String) async throws {
        let deadline = Date().addingTimeInterval(10)
        var output = ""
        while Date() < deadline {
            for op in await client.drainHostOps() {
                if case .hostBytes(let data) = op { output += String(decoding: data, as: UTF8.self) }
            }
            if output.contains(marker) { return }
            try await Task.sleep(for: .milliseconds(50))
        }
        throw ProbeFailure(description: "Timed out waiting for \(marker)")
    }
}
