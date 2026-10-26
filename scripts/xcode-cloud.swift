// Manage Xcode Cloud workflow configuration with App Store Connect API credentials.
// No build-start, deletion, or distribution endpoints are supported.
import Foundation
import CryptoKit

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8)); exit(1)
}
let args = Array(CommandLine.arguments.dropFirst())
if args.isEmpty || args == ["--help"] {
    print("""
    Usage: swift scripts/xcode-cloud.swift GET /v1/path
           swift scripts/xcode-cloud.swift POST /v1/ciWorkflows workflow.json
           swift scripts/xcode-cloud.swift PATCH /v1/ciWorkflows/ID workflow.json

    Authentication: ASC_KEY_ID, ASC_ISSUER_ID and ASC_KEY_PEM (or ASC_KEY_PATH).
    Inject credentials using your secret manager; never commit keys or JWTs.
    GET reads App Store Connect resources. Writes only configure workflows.
    No automatic retries: read back server state before retrying a failed write.
    """)
    exit(0)
}
guard args.count == 2 || args.count == 3 else { fail("Usage: scripts/xcode-cloud.swift GET|POST|PATCH /v1/path [body.json]") }
let method = args[0], path = args[1]
guard path.hasPrefix("/v1/"), !path.contains(".."), !path.contains("#"),
      let url = URL(string: "https://api.appstoreconnect.apple.com" + path),
      url.host == "api.appstoreconnect.apple.com" else { fail("Expected an App Store Connect /v1/ path") }
guard ["GET", "POST", "PATCH"].contains(method) else { fail("Unsupported method") }
if method != "GET" {
    let workflowID = path.replacingOccurrences(of: "/v1/ciWorkflows/", with: "")
    let allowed = (method == "POST" && path == "/v1/ciWorkflows") ||
        (method == "PATCH" && path.hasPrefix("/v1/ciWorkflows/") && UUID(uuidString: workflowID) != nil)
    guard args.count == 3, allowed else { fail("Writes are limited to workflow creation and updates") }
} else if args.count != 2 {
    fail("GET does not accept a request body")
}
let env = ProcessInfo.processInfo.environment
func required(_ name: String) -> String {
    guard let value = env[name], !value.isEmpty else { fail("Missing " + name) }; return value
}
let keyID = required("ASC_KEY_ID"), issuerID = required("ASC_ISSUER_ID")
let pem: String
if let inline = env["ASC_KEY_PEM"], !inline.isEmpty { pem = inline }
else {
    let path = NSString(string: required("ASC_KEY_PATH")).expandingTildeInPath
    guard let value = try? String(contentsOfFile: path, encoding: .utf8) else { fail("Cannot read ASC_KEY_PATH") }
    pem = value
}
guard let key = try? P256.Signing.PrivateKey(pemRepresentation: pem) else { fail("Invalid App Store Connect signing key") }
func base64url(_ data: Data) -> String {
    data.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
}
func segment(_ object: [String: Any]) -> String {
    base64url(try! JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]))
}
let now = Int(Date().timeIntervalSince1970)
let input = segment(["alg":"ES256", "kid":keyID, "typ":"JWT"]) + "." + segment(["iss":issuerID, "iat":now, "exp":now+600, "aud":"appstoreconnect-v1"])
guard let signature = try? key.signature(for: Data(input.utf8)) else { fail("Could not sign API request") }
var request = URLRequest(url: url)
request.httpMethod = method; request.timeoutInterval = 45
request.setValue("Bearer " + input + "." + base64url(signature.rawRepresentation), forHTTPHeaderField:"Authorization")
request.setValue("application/json", forHTTPHeaderField:"Accept")
if method != "GET" {
    guard let body = try? Data(contentsOf: URL(fileURLWithPath:args[2])), (try? JSONSerialization.jsonObject(with:body)) != nil else { fail("Body must be a JSON file") }
    request.httpBody = body; request.setValue("application/json", forHTTPHeaderField:"Content-Type")
}
final class NoRedirect: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}
let configuration = URLSessionConfiguration.ephemeral
configuration.timeoutIntervalForResource = 60
let session = URLSession(configuration:configuration, delegate:NoRedirect(), delegateQueue:nil)
let done = DispatchSemaphore(value:0)
var status = 0, responseBody = Data(), failed = false
session.dataTask(with:request) { data, response, error in
    status = (response as? HTTPURLResponse)?.statusCode ?? 0
    responseBody = data ?? Data(); failed = error != nil; done.signal()
}.resume()
done.wait(); session.invalidateAndCancel()
if failed { fail("Network request failed; no automatic retry. Check server state before retrying a write.") }
guard (200...299).contains(status) else {
    FileHandle.standardError.write(Data(("App Store Connect HTTP " + String(status) + "\n").utf8))
    FileHandle.standardError.write(responseBody); exit(1)
}
FileHandle.standardOutput.write(responseBody)
