import Foundation

struct PairingInvitation: Codable, Equatable {
    let version: Int
    let address: String
    let name: String
    let fingerprint: String
    let code: String
    let expiresAt: Date

    static func parse(_ text: String, now: Date = .now) throws -> Self {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.utf8.count <= 8192 else { throw TrillError.message("This pairing code is too large.") }
        let data: Data
        if trimmed.hasPrefix("{") {
            data = Data(trimmed.utf8)
        } else {
            guard let url = URLComponents(string: trimmed), url.scheme == "trill", url.host == "pair",
                  url.path.isEmpty, url.user == nil, url.password == nil, url.port == nil, url.fragment == nil,
                  let items = url.queryItems, items.count == 1, items[0].name == "data", let value = items[0].value else {
                throw TrillError.message("Scan a Trill pairing code or paste the pairing link from your Mac.")
            }
            var encoded = value.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
            encoded += String(repeating: "=", count: (4 - encoded.count % 4) % 4)
            guard let decoded = Data(base64Encoded: encoded) else { throw TrillError.message("This pairing link is incomplete.") }
            data = decoded
        }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let value = try decoder.singleValueContainer().decode(String.self)
            let formatter = ISO8601DateFormatter()
            if let date = formatter.date(from: value) { return date }
            formatter.formatOptions.insert(.withFractionalSeconds)
            if let date = formatter.date(from: value) { return date }
            throw TrillError.message("This pairing code has an invalid expiry date.")
        }
        let invitation: Self
        do { invitation = try decoder.decode(Self.self, from: data) }
        catch { throw TrillError.message("This is not a complete Trill pairing code.") }
        guard invitation.version == 1 else { throw TrillError.message("Update Trill to use this pairing code.") }
        guard invitation.expiresAt > now else { throw TrillError.message("This pairing code has expired. Create a new one on your Mac.") }
        guard !invitation.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              invitation.name.utf8.count <= 200, !invitation.name.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }),
              invitation.code.count == 64, invitation.code.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }) else {
            throw TrillError.message("This pairing code contains invalid computer details.")
        }
        _ = try Computer.validate(address: invitation.address, token: invitation.code, fingerprint: invitation.fingerprint)
        return invitation
    }
}

enum CertificateFingerprint {
    static func normalized(_ value: String) -> String {
        var value = value.trimmingCharacters(in: .whitespacesAndNewlines)
        if let equals = value.firstIndex(of: "="), value[..<equals].lowercased() == "sha256 fingerprint" {
            value = String(value[value.index(after: equals)...])
        }
        return value.filter { !$0.isWhitespace && $0 != ":" }.lowercased()
    }
}

enum ConnectionDiagnostic {
    static func message(for error: Error, hasPin: Bool, pinFailure: String? = nil) -> String {
        if let pinFailure { return pinFailure }
        let e = error as NSError
        guard e.domain == NSURLErrorDomain else { return error.localizedDescription }
        let code = URLError.Code(rawValue: e.code)
        switch code {
        case .serverCertificateUntrusted, .serverCertificateHasUnknownRoot, .serverCertificateHasBadDate, .serverCertificateNotYetValid:
            return hasPin ? "Your Mac's certificate could not be verified. Create a new pairing code on the Mac. (TLS \(e.code))" : "Your Mac's certificate is not trusted. Scan its Trill pairing code, or enter its certificate fingerprint for manual pairing. (TLS \(e.code))"
        case .secureConnectionFailed:
            let underlying = (e.userInfo[NSUnderlyingErrorKey] as? NSError).map { "; \($0.domain) \($0.code)" } ?? ""
            return "The secure connection failed before pairing. Use the full .ts.net hostname with a trusted certificate, or scan a new local-network pairing code from your Mac. Self-signed IP addresses may be blocked by iOS. (TLS \(e.code)\(underlying))"
        case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed, .notConnectedToInternet, .networkConnectionLost:
            return "Cannot reach your Mac. Keep the companion running and connect both devices to the same Wi-Fi or Tailscale network. (Network \(e.code))"
        case .timedOut:
            return "Your Mac did not respond. Check that the companion is running and reachable over Wi-Fi or Tailscale."
        default: return e.localizedDescription
        }
    }
}
