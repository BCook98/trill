import SwiftUI
import WebKit
import ImageIO
import AVKit

struct WorkspaceEntry: Decodable, Identifiable, Hashable {
    let name: String
    let path: String
    let kind: String
    let size: Int64
    var id: String { path }
    var isVideo: Bool { kind == "file" && ["mp4", "mov", "m4v"].contains((name as NSString).pathExtension.lowercased()) }
    var canRender: Bool { kind == "file" && ["html", "htm", "svg"].contains((name as NSString).pathExtension.lowercased()) }
}
struct WorkspaceListing: Decodable {
    let path: String
    let entries: [WorkspaceEntry]
    let truncated: Bool
}
struct WorkspaceFile {
    let data: Data
    let contentType: String
}
struct PreviewGrant: Decodable {
    let id: String
    let token: String
    let expiresAt: String
    func cookie(for base: URL) -> HTTPCookie? {
        guard let expiry, expiry > .now, token.count == 64,
              token.allSatisfy({ $0.isASCII && $0.isHexDigit }), let host = base.host else { return nil }
        return HTTPCookie(properties: [
            .name: "trill_preview", .value: token, .domain: host, .path: "/", .secure: "TRUE", .expires: expiry,
            HTTPCookiePropertyKey("HttpOnly"): "TRUE", .sameSitePolicy: HTTPCookieStringPolicy.sameSiteStrict.rawValue
        ])
    }
    var expiry: Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.date(from: expiresAt) ?? ISO8601DateFormatter().date(from: expiresAt)
    }
}

enum WorkspaceTab: String, CaseIterable { case files = "Files", browser = "Browser" }

/// Session-owned state outlives the inspector's sheet/column presentation.
@MainActor @Observable
final class WorkspaceState {
    let browser: WorkspaceBrowser
    var selectedFile: WorkspaceEntry?
    var browsingPhotos = false
    var projectPath = ""
    var photosPath = ""
    var path: String {
        get { browsingPhotos ? photosPath : projectPath }
        set { if browsingPhotos { photosPath = newValue } else { projectPath = newValue } }
    }
    private var navigation: Task<Void, Never>?

    init(api: APIClient, sessionID: String) {
        browser = WorkspaceBrowser(api: api, sessionID: sessionID)
    }
    func open(_ address: String) {
        selectedFile = nil
        browser.draftAddress = address
        navigation?.cancel()
        navigation = Task { await browser.open(address) }
    }
    func close() {
        navigation?.cancel(); navigation = nil
        browser.close()
    }
}

struct WorkspacePanel: View {
    let api: APIClient
    let session: RemoteSession
    @Binding var tab: WorkspaceTab
    @Binding var presented: Bool
    @Binding var initialAddress: String?
    @Bindable var state: WorkspaceState
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    private var browser: WorkspaceBrowser { state.browser }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if tab == .files {
                    WorkspaceFiles(api: api, session: session, photos: $state.browsingPhotos, selection: $state.selectedFile, render: render, path: $state.path)
                }
                else { WorkspaceBrowserView(browser: browser) }
            }
            .navigationTitle(tab.rawValue).navigationBarTitleDisplayMode(.inline)
            .navigationDestination(item: $state.selectedFile) { entry in
                WorkspaceFilePreview(api: api, sessionID: session.id, entry: entry, photos: state.browsingPhotos) { render(entry) }
            }
            .toolbar {
                // Let pushed file previews keep their own title and back button.
                if state.selectedFile == nil {
                    ToolbarItem(placement: .principal) {
                        Group {
                            if dynamicTypeSize.isAccessibilitySize {
                                workspacePicker.pickerStyle(.menu)
                            } else {
                                workspacePicker.pickerStyle(.segmented)
                            }
                        }
                        .frame(maxWidth: 220)
                        .accessibilityIdentifier("workspace-tab")
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Close", systemImage: "xmark") { presented = false }
                        .accessibilityLabel("Close workspace")
                }
            }
        }
        .onAppear { openRequestedAddress() }
        .onChange(of: initialAddress) { _, _ in openRequestedAddress() }
        .inspectorColumnWidth(min: 280, ideal: 380, max: 600)
    }
    private var workspacePicker: some View {
        Picker("Workspace", selection: $tab) {
            ForEach(WorkspaceTab.allCases, id: \.self) { Text($0.rawValue).tag($0) }
        }
    }
    private func openRequestedAddress() {
        guard let address = initialAddress else { return }
        initialAddress = nil
        tab = .browser
        state.open(address)
    }
    private func render(_ entry: WorkspaceEntry) {
        // Keep the navigation binding alive while moving from source to Browser.
        state.selectedFile = nil
        tab = .browser
        Task { await browser.openFile(entry) }
    }
}

private struct WorkspaceFiles: View {
    let api: APIClient
    let session: RemoteSession
    @Binding var photos: Bool
    @Binding var selection: WorkspaceEntry?
    let render: (WorkspaceEntry) -> Void
    @Binding var path: String
    @State private var listing: WorkspaceListing?
    @State private var error: String?
    @State private var loading = false
    @State private var requestID = UUID()
    var body: some View {
        VStack(spacing: 0) {
            Picker("Files to browse", selection: $photos) {
                Text("Project").tag(false)
                Text("Photos").tag(true)
            }.pickerStyle(.segmented).padding(.horizontal).padding(.bottom, 10)
            HStack(spacing: 8) {
                Button {
                    path = path.split(separator: "/").dropLast().joined(separator: "/")
                } label: { Image(systemName: "arrow.up").frame(minWidth: 44, minHeight: 44) }
                    .disabled(path.isEmpty).accessibilityLabel("Parent folder")
                Text(path.isEmpty ? (photos ? "Session photos" : session.projectName) : path)
                    .font(.subheadline).lineLimit(2).truncationMode(.middle)
                Spacer(minLength: 0)
                if loading { ProgressView().padding(.trailing) }
            }.padding(.horizontal, 8)
            if let error {
                ContentUnavailableView {
                    Label("Files unavailable", systemImage: "folder.badge.questionmark")
                } description: { Text(error) } actions: { Button("Try again") { Task { await load() } } }
            } else if let listing, listing.entries.isEmpty {
                ContentUnavailableView(photos ? "No photos yet" : "Empty folder", systemImage: photos ? "photo" : "folder",
                    description: Text(photos ? "Photos added through Write appear here." : "There are no files in this folder."))
            } else {
                List(listing?.entries ?? []) { entry in
                    Button {
                        if entry.kind == "directory" { path = entry.path }
                        else { selection = entry }
                    } label: {
                        HStack(spacing: 12) {
                            Image(systemName: entry.kind == "directory" ? "folder" : fileSymbol(entry.name))
                                .foregroundStyle(entry.kind == "directory" ? Color.accentColor : .secondary)
                                .frame(width: 24)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(entry.name).foregroundStyle(.primary).lineLimit(2)
                                if entry.kind == "file" {
                                    Text(ByteCountFormatter.string(fromByteCount: entry.size, countStyle: .file))
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                                if entry.kind == "unsupported" { Text("Unavailable outside this folder").font(.caption).foregroundStyle(.secondary) }
                            }
                            Spacer(minLength: 0)
                            Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                        }.padding(.vertical, 3)
                    }.disabled(entry.kind == "unsupported")
                    .contextMenu {
                        if entry.kind == "file" {
                            Button(entry.isVideo ? "Play video" : "View file", systemImage: entry.isVideo ? "play.rectangle" : "doc.text") { selection = entry }
                            if !photos && entry.canRender {
                                Button("Preview in browser", systemImage: "globe") { render(entry) }
                            }
                        }
                    }
                }.listStyle(.plain).refreshable { await load() }
                if listing?.truncated == true {
                    Text("Showing the first 1,000 items.").font(.footnote).foregroundStyle(.secondary).padding(8)
                }
            }
        }
        .task(id: "\(photos):\(path)") { await load() }
        .onChange(of: photos) { _, _ in listing = nil }
    }
    private func load() async {
        let id = UUID(); requestID = id
        let requestedPath = path
        loading = true; error = nil; listing = nil
        do {
            let result = try await api.workspaceFiles(sessionID: session.id, path: requestedPath, photos: photos)
            guard requestID == id, !Task.isCancelled else { return }
            listing = result
        } catch {
            guard requestID == id, !Task.isCancelled else { return }
            self.error = error.localizedDescription
        }
        if requestID == id { loading = false }
    }
    private func fileSymbol(_ name: String) -> String {
        switch (name as NSString).pathExtension.lowercased() {
        case "png", "jpg", "jpeg", "gif", "webp", "heic": "photo"
        case "mp4", "mov", "m4v": "video"
        case "swift", "go", "js", "ts", "tsx", "jsx", "html", "css", "json", "py", "rb", "sh": "chevron.left.forwardslash.chevron.right"
        default: "doc.text"
        }
    }
}

private struct WorkspaceFilePreview: View {
    let api: APIClient
    let sessionID: String
    let entry: WorkspaceEntry
    let photos: Bool
    let render: () -> Void
    @State private var file: WorkspaceFile?
    @State private var previewImage: UIImage?
    @State private var error: String?
    var body: some View {
        Group {
            if entry.isVideo && !photos {
                WorkspaceVideoPreview(api: api, sessionID: sessionID, entry: entry).id(entry.id)
            } else if let error {
                ContentUnavailableView("Preview unavailable", systemImage: "doc.questionmark", description: Text(error))
            } else if let file {
                if file.contentType.hasPrefix("image/"), let image = previewImage {
                    ScrollView {
                        Image(uiImage: image).resizable().scaledToFit().frame(maxWidth: .infinity)
                            .accessibilityLabel(entry.name).padding()
                    }
                } else if let text = String(data: file.data, encoding: .utf8) {
                    ScrollView([.horizontal, .vertical]) {
                        Text(text).font(.system(.footnote, design: .monospaced)).textSelection(.enabled)
                            .fixedSize(horizontal: true, vertical: true).frame(maxWidth: .infinity, alignment: .topLeading).padding()
                    }
                } else { ContentUnavailableView("No text preview", systemImage: "doc", description: Text("Trill can preview images and UTF-8 text. This file uses another format.")) }
            } else { ProgressView("Loading file…").frame(maxWidth: .infinity, maxHeight: .infinity) }
        }
        .navigationTitle(entry.name).navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if !photos && entry.canRender {
                ToolbarItem(placement: .primaryAction) {
                    Button("Preview in browser", systemImage: "globe", action: render)
                        .accessibilityIdentifier("file-render-preview")
                }
            }
        }
        .task(id: entry.id) {
            guard !entry.isVideo || photos else { return }
            do {
                let result = try await api.workspaceFile(sessionID: sessionID, path: entry.path, photos: photos)
                if result.contentType.hasPrefix("image/") {
                    let data = try await Task.detached(priority: .userInitiated) { try WorkspaceImage.thumbnail(result.data) }.value
                    guard !Task.isCancelled else { return }
                    previewImage = UIImage(data: data)
                }
                guard !Task.isCancelled else { return }
                file = result
            }
            catch { if !Task.isCancelled { self.error = error.localizedDescription } }
        }
    }
}

private struct WorkspaceVideoPreview: View {
    let api: APIClient
    let sessionID: String
    let entry: WorkspaceEntry
    @StateObject private var model = WorkspaceVideoPlayer()
    @State private var attempt = UUID()
    @Environment(\.scenePhase) private var scenePhase
    var body: some View {
        Group {
            if let error = model.error {
                ContentUnavailableView {
                    Label("Video unavailable", systemImage: "video.slash")
                } description: { Text(error) } actions: {
                    Button("Try again") { attempt = UUID() }
                }
            } else if let player = model.player {
                VideoPlayer(player: player)
                    .accessibilityIdentifier("file-video-player")
            } else {
                VStack(spacing: 12) {
                    ProgressView(value: model.progress).frame(maxWidth: 180)
                    Text(model.progress == 1 ? "Preparing video…" : "Downloading video…").font(.subheadline)
                    Text(ByteCountFormatter.string(fromByteCount: entry.size, countStyle: .file))
                        .font(.caption).foregroundStyle(.secondary)
                }.frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: attempt) { await model.load(api: api, sessionID: sessionID, entry: entry) }
        .onDisappear { model.close() }
        .onChange(of: scenePhase) { _, phase in if phase != .active { model.player?.pause() } }
    }
}

@MainActor final class WorkspaceVideoPlayer: ObservableObject {
    @Published private(set) var player: AVPlayer?
    @Published private(set) var progress: Double?
    @Published private(set) var error: String?
    private var file: WorkspaceVideoFile?
    private var observation: NSKeyValueObservation?
    private var requestID = UUID()
    func load(api: APIClient, sessionID: String, entry: WorkspaceEntry) async {
        close()
        let id = requestID
        error = nil
        progress = nil
        do {
            guard entry.size <= WorkspaceVideoFile.maximumSize else { throw TrillError.message(WorkspaceVideoFile.sizeMessage) }
            let downloaded = try await api.workspaceVideo(sessionID: sessionID, path: entry.path) { [weak self] value in
                Task { @MainActor in
                    guard let self, self.requestID == id else { return }
                    self.progress = value
                }
            }
            try Task.checkCancellation()
            guard requestID == id else { return }
            let asset = Self.asset(for: downloaded.url)
            guard try await asset.load(.isPlayable) else {
                throw TrillError.message("iOS cannot play this video's format. Try an MP4 with H.264 or HEVC video.")
            }
            try Task.checkCancellation()
            guard requestID == id else { return }
            let item = AVPlayerItem(asset: asset)
            let newPlayer = AVPlayer(playerItem: item)
            newPlayer.allowsExternalPlayback = false
            file = downloaded
            player = newPlayer
            // A container can load successfully but fail later during decoding.
            observation = item.observe(\.status, options: [.initial, .new]) { [weak self] item, _ in
                guard item.status == .failed else { return }
                Task { @MainActor in
                    guard let self, self.requestID == id else { return }
                    self.close()
                    self.error = "iOS could not play this video. Try an MP4 with H.264 or HEVC video."
                }
            }
        } catch {
            guard requestID == id, !Task.isCancelled else { return }
            self.error = error.localizedDescription
        }
    }
    static func asset(for url: URL) -> AVURLAsset {
        AVURLAsset(url: url, options: [
            AVURLAssetReferenceRestrictionsKey: AVAssetReferenceRestrictions.forbidAll.rawValue
        ])
    }
    func close() {
        requestID = UUID()
        observation = nil
        player?.pause()
        player?.replaceCurrentItem(with: nil)
        player = nil
        file = nil
    }
}

enum WorkspaceImage {
    static func thumbnail(_ data: Data) throws -> Data {
        guard data.count <= 10 * 1024 * 1024,
              let source = CGImageSourceCreateWithData(data as CFData, [kCGImageSourceShouldCache: false] as CFDictionary),
              let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
              let width = properties[kCGImagePropertyPixelWidth] as? NSNumber,
              let height = properties[kCGImagePropertyPixelHeight] as? NSNumber,
              width.doubleValue > 0, height.doubleValue > 0,
              width.doubleValue * height.doubleValue <= 40_000_000,
              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceThumbnailMaxPixelSize: 2048,
                kCGImageSourceShouldCacheImmediately: true
              ] as CFDictionary) else { throw TrillError.message("Trill cannot preview this image. It may be too large or use an unsupported format.") }
        let output = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(output, "public.png" as CFString, 1, nil) else {
            throw TrillError.message("Trill could not open this image.")
        }
        CGImageDestinationAddImage(destination, image, nil)
        guard CGImageDestinationFinalize(destination) else { throw TrillError.message("Trill could not open this image.") }
        return output as Data
    }
}

/// A private WebKit origin mapped to the selected document's containing folder.
/// Only presentation assets are readable, through the existing authenticated API.
struct WorkspaceDocument {
    static let scheme = "trill-file"
    static let policy = "default-src 'none'; script-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
    let folder: String
    let url: URL

    init(entry: WorkspaceEntry) throws {
        guard entry.canRender, Self.validPath(entry.path) else { throw TrillError.message("This file cannot be rendered in the browser.") }
        let components = entry.path.split(separator: "/").map(String.init)
        folder = components.dropLast().joined(separator: "/")
        var parts = URLComponents()
        parts.scheme = Self.scheme; parts.host = UUID().uuidString.lowercased()
        parts.path = "/" + components.last!
        guard let url = parts.url else { throw TrillError.message("This file address is invalid.") }
        self.url = url
    }
    static func validPath(_ path: String) -> Bool {
        !path.isEmpty && !path.hasPrefix("/") && !path.contains("\\") &&
        !path.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) &&
        path.split(separator: "/", omittingEmptySubsequences: false).allSatisfy { !$0.isEmpty && !$0.hasPrefix(".") }
    }
    func resource(_ url: URL) throws -> (path: String, relative: String, mime: String) {
        guard let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
              parts.scheme == Self.scheme, parts.host == self.url.host,
              parts.user == nil, parts.password == nil, parts.port == nil,
              let decoded = parts.percentEncodedPath.removingPercentEncoding, decoded.hasPrefix("/") else {
            throw TrillError.message("This address is outside the file preview.")
        }
        let relative = String(decoded.dropFirst())
        guard Self.validPath(relative), let mime = Self.mime(relative) else {
            throw TrillError.message("This file type is unavailable in static previews.")
        }
        return (folder.isEmpty ? relative : folder + "/" + relative, relative, mime)
    }
    static func mime(_ path: String) -> String? {
        switch (path as NSString).pathExtension.lowercased() {
        case "html", "htm": "text/html"
        case "css": "text/css"
        case "js", "mjs": "text/javascript"
        case "svg": "image/svg+xml"
        case "png": "image/png"
        case "jpg", "jpeg": "image/jpeg"
        case "gif": "image/gif"
        case "webp": "image/webp"
        default: nil
        }
    }
}

@MainActor
final class WorkspaceFileHandler: NSObject, WKURLSchemeHandler {
    let document: WorkspaceDocument
    private let fetch: (String) async throws -> WorkspaceFile
    private var tasks: [ObjectIdentifier: Task<Void, Never>] = [:]
    private var closed = false
    private var requests = 0
    private var bytes = 0

    init(document: WorkspaceDocument, fetch: @escaping (String) async throws -> WorkspaceFile) {
        self.document = document; self.fetch = fetch
    }
    func close() {
        closed = true
        let pending = tasks.values; tasks.removeAll()
        for task in pending { task.cancel() }
    }
    func webView(_ webView: WKWebView, start urlSchemeTask: any WKURLSchemeTask) {
        let id = ObjectIdentifier(urlSchemeTask)
        guard !closed, tasks.count < 8, requests < 128, bytes < 32 * 1024 * 1024,
              let url = urlSchemeTask.request.url,
              urlSchemeTask.request.httpMethod == nil || urlSchemeTask.request.httpMethod == "GET" else {
            urlSchemeTask.didFailWithError(TrillError.message("Static preview limit reached. Reload to try again.")); return
        }
        requests += 1
        tasks[id] = Task { [weak self] in
            guard let self else { return }
            do {
                let resource = try document.resource(url)
                let file = try await fetch(resource.relative)
                guard tasks[id] != nil, !closed, !Task.isCancelled else { return }
                // The companion already bounds each response. Also bound the
                // complete document and reject disguised binary/text assets.
                guard file.data.count <= 10 * 1024 * 1024, bytes + file.data.count <= 32 * 1024 * 1024 else {
                    throw TrillError.message("This page is too large for a static preview.")
                }
                if ["image/png", "image/jpeg", "image/gif", "image/webp"].contains(resource.mime) {
                    guard file.contentType == resource.mime,
                          let source = CGImageSourceCreateWithData(file.data as CFData, [kCGImageSourceShouldCache: false] as CFDictionary),
                          let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
                          let width = properties[kCGImagePropertyPixelWidth] as? NSNumber,
                          let height = properties[kCGImagePropertyPixelHeight] as? NSNumber,
                          width.doubleValue > 0, height.doubleValue > 0,
                          width.doubleValue * height.doubleValue <= 40_000_000 else {
                        throw TrillError.message("This image is too large or invalid.")
                    }
                } else {
                    guard file.contentType.hasPrefix("text/"), file.data.count <= 2 * 1024 * 1024,
                          String(data: file.data, encoding: .utf8) != nil else {
                        throw TrillError.message("This asset is not supported UTF-8 text.")
                    }
                }
                bytes += file.data.count
                let response = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: [
                    "Content-Type": resource.mime + (resource.mime.hasPrefix("text/") || resource.mime == "image/svg+xml" ? "; charset=utf-8" : ""),
                    "Content-Security-Policy": WorkspaceDocument.policy,
                    "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store"
                ])!
                urlSchemeTask.didReceive(response)
                urlSchemeTask.didReceive(file.data)
                urlSchemeTask.didFinish()
            } catch {
                guard tasks[id] != nil, !closed, !Task.isCancelled else { return }
                urlSchemeTask.didFailWithError(error)
            }
            tasks[id] = nil
        }
    }
    func webView(_ webView: WKWebView, stop urlSchemeTask: any WKURLSchemeTask) {
        tasks.removeValue(forKey: ObjectIdentifier(urlSchemeTask))?.cancel()
    }
}

/// Only known computer addresses become localhost on the companion.
enum TerminalLinkRoute: Equatable {
    case preview(URL)
    case external(URL)

    static func webURL(_ text: String) -> URL? {
        guard text.utf8.count <= 8192,
              !text.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }),
              let parts = URLComponents(string: text),
              ["http", "https"].contains(parts.scheme?.lowercased() ?? ""),
              let host = parts.host, !host.isEmpty,
              parts.user == nil, parts.password == nil,
              parts.port == nil || (1...65535).contains(parts.port!) else { return nil }
        return parts.url
    }

    static func destination(for url: URL, computer: URL?) -> Self? {
        guard webURL(url.absoluteString) != nil,
              var parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let host = parts.host?.lowercased() else { return nil }
        let loopback = ["localhost", "localhost.", "127.0.0.1", "::1", "[::1]", "0.0.0.0", "::", "[::]"].contains(host)
        let computerDevelopmentPort = host == computer?.host?.lowercased()
            && parts.scheme?.lowercased() == "http"
            && (parts.port.map { (1024...65535).contains($0) && $0 != (computer?.port ?? 443) } ?? false)
        if loopback || computerDevelopmentPort {
            // Preserve scheme, port and encoded path/query/fragment. Unsupported
            // local HTTPS/privileged ports get the preview's clear error, never
            // a request to the phone's localhost or a silent HTTPS downgrade.
            parts.host = "localhost"
            guard let address = parts.url else { return nil }
            return .preview(address)
        }
        return .external(url)
    }
}

struct WorkspaceAddress: Equatable {
    let url: URL
    let localPort: Int?
    static func parse(_ value: String) throws -> WorkspaceAddress {
        var text = value.trimmingCharacters(in: .whitespacesAndNewlines)
        if !text.contains("://") {
            text = (text.hasPrefix("localhost:") || text.hasPrefix("127.0.0.1:") || text.hasPrefix("[::1]:") ? "http://" : "https://") + text
        }
        guard let parts = URLComponents(string: text), let url = parts.url,
              ["http", "https"].contains(parts.scheme?.lowercased() ?? ""),
              let host = parts.host, !host.isEmpty, parts.user == nil, parts.password == nil else {
            throw TrillError.message("Enter a web address, such as localhost:3000 or https://example.com.")
        }
        let local = ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host.lowercased())
        if local {
            guard parts.scheme?.lowercased() == "http", let port = parts.port, (1024...65535).contains(port) else {
                throw TrillError.message("Use an HTTP localhost address with your development server's port, such as localhost:3000.")
            }
            return WorkspaceAddress(url: url, localPort: port)
        }
        return WorkspaceAddress(url: url, localPort: nil)
    }
    static func isDirectLoopback(_ url: URL, authenticatedProxy: URL?) -> Bool {
        guard let host = url.host,
              ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host.lowercased()) else { return false }
        guard let proxy = authenticatedProxy else { return true }
        return url.scheme != proxy.scheme || url.host != proxy.host || (url.port ?? 443) != (proxy.port ?? 443)
    }
    func through(_ base: URL) -> URL? {
        guard var proxy = URLComponents(url: base, resolvingAgainstBaseURL: false),
              let original = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
        proxy.percentEncodedPath = original.percentEncodedPath.isEmpty ? "/" : original.percentEncodedPath
        proxy.percentEncodedQuery = original.percentEncodedQuery
        proxy.percentEncodedFragment = original.percentEncodedFragment
        return proxy.url
    }
}

enum WorkspacePreviewFailure {
    static func message(for response: HTTPURLResponse, companion: URL, port: Int) -> String? {
        guard response.statusCode == 502, let url = response.url,
              url.scheme == companion.scheme, url.host == companion.host,
              (url.port ?? 443) == (companion.port ?? 443) else { return nil }
        switch response.value(forHTTPHeaderField: "X-Trill-Preview-Error") {
        case "upstream-unavailable":
            return "Couldn’t reach localhost:\(port) on your Mac. Check that its development server is running on this port, then reload."
        case "response-rejected":
            return "The development server returned a response that can’t be previewed. Check its redirects, then reload."
        default: return nil
        }
    }
}

@MainActor @Observable
final class WorkspaceBrowser: NSObject, WKNavigationDelegate, WKUIDelegate {
    let api: APIClient
    let sessionID: String
    var webView: WKWebView
    var address = ""
    var draftAddress = ""
    var loading = false
    var error: String?
    var canGoBack = false
    var canGoForward = false
    private var generation = UUID()
    private var grant: PreviewGrant?
    private var localPort: Int?
    private var fileHandler: WorkspaceFileHandler?
    private(set) var fileEntry: WorkspaceEntry?
    private let trust: PinnedSessionDelegate

    init(api: APIClient, sessionID: String) {
        self.api = api; self.sessionID = sessionID
        trust = PinnedSessionDelegate(fingerprint: api.computer.fingerprint)
        let config = WKWebViewConfiguration(); config.websiteDataStore = .nonPersistent()
        webView = WKWebView(frame: .zero, configuration: config)
        super.init()
        configure()
    }
    private func configure() {
        webView.navigationDelegate = self; webView.uiDelegate = self
        webView.allowsBackForwardNavigationGestures = true
        webView.isOpaque = false
        webView.backgroundColor = .systemBackground
        webView.scrollView.backgroundColor = .systemBackground
        webView.accessibilityLabel = "Page preview"
    }
    private func resetWebView(configuration: WKWebViewConfiguration? = nil) {
        fileHandler?.close(); fileHandler = nil; fileEntry = nil
        webView.stopLoading(); webView.navigationDelegate = nil; webView.uiDelegate = nil
        let config = configuration ?? WKWebViewConfiguration(); config.websiteDataStore = .nonPersistent()
        webView = WKWebView(frame: .zero, configuration: config)
        configure(); canGoBack = false; canGoForward = false
    }
    private func releaseGrant() {
        guard let grant else { return }
        self.grant = nil
        let api = api
        Task { try? await api.closePreview(id: grant.id) }
    }
    func close() {
        generation = UUID(); releaseGrant(); localPort = nil
        resetWebView(); loading = false
    }
    func openFile(_ entry: WorkspaceEntry) async {
        let attempt = UUID(); generation = attempt
        releaseGrant(); localPort = nil; resetWebView()
        error = nil; loading = true; address = entry.path
        do {
            let document = try WorkspaceDocument(entry: entry)
            // Static documents disable JavaScript, including WebRTC, and web
            // resource requests. Native authenticated reads supply local assets.
            let rules: [[String: Any]] = [
                ["trigger": ["url-filter": "^https?://"], "action": ["type": "block"]],
                ["trigger": ["url-filter": "^wss?://"], "action": ["type": "block"]]
            ]
            let json = String(data: try JSONSerialization.data(withJSONObject: rules), encoding: .utf8)!
            let rule = try await WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "trill-file-" + attempt.uuidString, encodedContentRuleList: json)
            try? await WKContentRuleListStore.default().removeContentRuleList(forIdentifier: "trill-file-" + attempt.uuidString)
            guard generation == attempt, !Task.isCancelled else { return }
            guard let rule else { throw TrillError.message("Trill could not isolate this file preview.") }
            let handler = WorkspaceFileHandler(document: document) { [api, sessionID] path in
                try await api.workspaceStaticFile(sessionID: sessionID, root: document.folder, path: path)
            }
            let config = WKWebViewConfiguration()
            config.defaultWebpagePreferences.allowsContentJavaScript = false
            config.setURLSchemeHandler(handler, forURLScheme: WorkspaceDocument.scheme)
            config.userContentController.add(rule)
            resetWebView(configuration: config)
            fileHandler = handler; fileEntry = entry
            webView.load(URLRequest(url: document.url))
        } catch {
            guard generation == attempt, !Task.isCancelled else { return }
            self.error = error.localizedDescription; loading = false
        }
    }
    func enterWebAddress() { close(); address = ""; error = nil }
    func open(_ text: String) async {
        let attempt = UUID(); generation = attempt
        error = nil; loading = true
        do {
            let target = try WorkspaceAddress.parse(text)
            if fileEntry != nil { resetWebView() }
            address = target.url.absoluteString
            if let port = target.localPort {
                guard port != (api.baseURL.port ?? 443) else { throw TrillError.message("Enter the port your development server uses. The companion's own port cannot be previewed.") }
                if localPort != port || (grant?.expiry ?? .distantPast) <= .now {
                    releaseGrant(); resetWebView(); localPort = port
                    // Cookies are site-scoped, not port-scoped. Block same-host
                    // requests to other ports while the preview credential exists.
                    let host = NSRegularExpression.escapedPattern(for: api.baseURL.host ?? "")
                    let origin = NSRegularExpression.escapedPattern(for: api.baseURL.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")))
                    let rules: [[String: Any]] = [
                        ["trigger": ["url-filter": "^https?://" + host + "[:/]"], "action": ["type": "block"]],
                        ["trigger": ["url-filter": "^wss?://" + host + "[:/]"], "action": ["type": "block"]],
                        ["trigger": ["url-filter": "^" + origin + "/"], "action": ["type": "ignore-previous-rules"]],
                        ["trigger": ["url-filter": "^" + origin.replacingOccurrences(of: "https:", with: "wss:").replacingOccurrences(of: "http:", with: "ws:") + "/"], "action": ["type": "ignore-previous-rules"]]
                    ]
                    let json = String(data: try JSONSerialization.data(withJSONObject: rules), encoding: .utf8)!
                    let rule = try await WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "trill-preview-" + attempt.uuidString, encodedContentRuleList: json)
                    try? await WKContentRuleListStore.default().removeContentRuleList(forIdentifier: "trill-preview-" + attempt.uuidString)
                    guard generation == attempt, !Task.isCancelled else { return }
                    guard let rule else { throw TrillError.message("Trill could not keep this preview connection separate from other pages.") }
                    webView.configuration.userContentController.add(rule)
                    let next = try await api.openPreview(sessionID: sessionID, port: port)
                    guard generation == attempt, !Task.isCancelled else { try? await api.closePreview(id: next.id); return }
                    guard let cookie = next.cookie(for: api.baseURL) else {
                        try? await api.closePreview(id: next.id)
                        throw TrillError.message("The companion returned an invalid preview connection.")
                    }
                    grant = next
                    await webView.configuration.websiteDataStore.httpCookieStore.setCookie(cookie)
                    guard generation == attempt, !Task.isCancelled else { return }
                }
                guard let url = target.through(api.baseURL) else { throw TrillError.message("This preview address is invalid.") }
                webView.load(URLRequest(url: url))
            } else {
                if localPort != nil { releaseGrant(); localPort = nil; resetWebView() }
                webView.load(URLRequest(url: target.url))
            }
        } catch {
            guard generation == attempt, !Task.isCancelled else { return }
            self.error = error.localizedDescription; loading = false
        }
    }
    func reload() { Task { if let fileEntry { await openFile(fileEntry) } else { await open(address) } } }
    func back() { webView.goBack() }
    func forward() { webView.goForward() }
    private func update(_ webView: WKWebView) {
        canGoBack = webView.canGoBack; canGoForward = webView.canGoForward
        if let url = webView.url, url.scheme != "about" {
            if let fileHandler, let resource = try? fileHandler.document.resource(url) {
                address = resource.path
            } else if let port = localPort, sameOrigin(url, api.baseURL), var parts = URLComponents(url: url, resolvingAgainstBaseURL: false) {
                parts.scheme = "http"; parts.host = "localhost"; parts.port = port
                address = parts.url?.absoluteString ?? address
            } else { address = url.absoluteString }
        }
    }
    private func sameOrigin(_ first: URL, _ second: URL) -> Bool {
        first.scheme == second.scheme && first.host == second.host && (first.port ?? 443) == (second.port ?? 443)
    }
    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) { loading = true; error = nil }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { loading = false; update(webView) }
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { navigationFailed(webView, error: error) }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { navigationFailed(webView, error: error) }
    private func navigationFailed(_ webView: WKWebView, error: Error) {
        guard webView === self.webView else { return }
        // Policy cancellation can report a WebKit "Frame load interrupted"
        // error after we deliberately replaced a proxy failure with native UI.
        // Keep that actionable message; a new navigation clears it on start.
        guard self.error == nil else { loading = false; return }
        let nsError = error as NSError
        if nsError.domain == NSURLErrorDomain && nsError.code == NSURLErrorCancelled { return }
        loading = false; self.error = error.localizedDescription
    }
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) { loading = false; error = "The page closed. Reload to open it again." }
    // A cancelled main-frame response never reaches didFinish. Keep the failed
    // destination so Reload retries a followed link, including its path/query.
    func recordPreviewFailure(_ response: HTTPURLResponse, port: Int, message: String) {
        if let url = response.url, sameOrigin(url, api.baseURL),
           var parts = URLComponents(url: url, resolvingAgainstBaseURL: false) {
            parts.scheme = "http"; parts.host = "localhost"; parts.port = port
            address = parts.url?.absoluteString ?? address
        }
        loading = false; error = message
    }
    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse, decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        guard webView === self.webView else { decisionHandler(.cancel); return }
        if response.isForMainFrame, grant != nil, let port = localPort,
           let http = response.response as? HTTPURLResponse,
           let message = WorkspacePreviewFailure.message(for: http, companion: api.baseURL, port: port) {
            recordPreviewFailure(http, port: port, message: message)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let fileHandler {
            if let url = action.request.url, (try? fileHandler.document.resource(url)) != nil,
               action.request.httpMethod == nil || action.request.httpMethod == "GET" {
                decisionHandler(.allow)
            } else {
                error = "Static file previews stay in this folder. Use a development server for external links or network features."
                decisionHandler(.cancel)
            }
            return
        }
        guard let url = action.request.url, ["http", "https"].contains(url.scheme?.lowercased() ?? ""), url.user == nil, url.password == nil else {
            decisionHandler(.cancel); return
        }
        if WorkspaceAddress.isDirectLoopback(url, authenticatedProxy: grant == nil ? nil : api.baseURL) {
            error = "Enter this localhost address in the address bar to open it from your Mac."
            decisionHandler(.cancel); return
        }
        // Never send the preview cookie to another port on the companion host.
        if localPort != nil, url.host == api.baseURL.host, !sameOrigin(url, api.baseURL) {
            error = "Enter this address in the address bar to open a separate page."
            decisionHandler(.cancel); return
        }
        if grant != nil, action.targetFrame?.isMainFrame != false, !sameOrigin(url, api.baseURL) {
            // A top-level departure must not carry an active local capability
            // into another page, including a same-site sibling hostname.
            decisionHandler(.cancel)
            Task { await open(url.absoluteString) }
            return
        }
        decisionHandler(.allow)
    }
    func webView(_ webView: WKWebView, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.host == api.baseURL.host,
              challenge.protectionSpace.port == (api.baseURL.port ?? 443), !api.computer.fingerprint.isEmpty else {
            completionHandler(.performDefaultHandling, nil); return
        }
        trust.urlSession(api.session, didReceive: challenge, completionHandler: completionHandler)
    }
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        // A page may open a link, but cannot mint a grant for a new local port.
        if action.targetFrame == nil { webView.load(action.request) }
        return nil
    }
}

private struct WorkspaceBrowserView: View {
    @Bindable var browser: WorkspaceBrowser
    @FocusState private var editing: Bool
    private var canSubmit: Bool {
        !browser.draftAddress.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
    private var showsGo: Bool {
        browser.fileEntry == nil && (editing || browser.address.isEmpty || browser.draftAddress != browser.address)
    }

    var body: some View {
        VStack(spacing: 0) {
            addressBar
            if browser.loading { ProgressView().progressViewStyle(.linear) }
            if let error = browser.error {
                VStack(alignment: .leading, spacing: 8) {
                    Text(error).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled)
                    Button("Reload", systemImage: "arrow.clockwise") { browser.reload() }
                        .disabled(browser.loading).frame(minHeight: 44)
                }.padding().frame(maxWidth: .infinity, alignment: .leading).background(.bar)
            }
            if browser.address.isEmpty {
                ContentUnavailableView("Preview your work", systemImage: "globe", description: Text("Start a development server on your Mac and enter its localhost address here. You can also enter an HTTPS website address."))
            } else {
                BrowserSurface(webView: browser.webView).id(ObjectIdentifier(browser.webView))
            }
        }
        .onAppear { if browser.draftAddress.isEmpty { browser.draftAddress = browser.address } }
        .onChange(of: browser.address) { _, value in if !editing { browser.draftAddress = value } }
    }

    private var addressBar: some View {
        HStack(spacing: 0) {
            if !editing {
                Button { browser.back() } label: {
                    Image(systemName: "chevron.left").frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .accessibilityLabel("Back").disabled(!browser.canGoBack)
            }
            if browser.fileEntry != nil {
                Label(browser.address, systemImage: "doc")
                    .font(.subheadline).lineLimit(1).truncationMode(.middle)
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .accessibilityLabel("Static preview, scripts disabled: \(browser.address)")
            } else {
                TextField("localhost:3000 or https://…", text: $browser.draftAddress)
                    .font(.subheadline)
                    .textContentType(.URL).keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .submitLabel(.go).focused($editing).onSubmit { navigate() }
                    .frame(minHeight: 44)
                    .accessibilityLabel("Preview address").accessibilityIdentifier("preview-address")
            }
            if showsGo {
                Button(action: navigate) {
                    Image(systemName: "arrow.right").frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .accessibilityLabel("Go").disabled(!canSubmit)
            } else {
                Button { browser.reload() } label: {
                    Image(systemName: "arrow.clockwise").frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .accessibilityLabel("Reload").disabled(browser.address.isEmpty)
            }
            if editing {
                Button {
                    browser.draftAddress = browser.address
                    editing = false
                } label: {
                    Image(systemName: "xmark.circle").font(.subheadline)
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .accessibilityLabel("Cancel address editing")
            } else { browserMenu }
        }
        .buttonStyle(.plain).padding(.horizontal, 8).background(.bar)
    }

    private var browserMenu: some View {
        Menu {
            Button("Forward", systemImage: "chevron.right") { browser.forward() }
                .disabled(!browser.canGoForward)
            if browser.fileEntry != nil {
                Button("Enter a web address", systemImage: "globe") {
                    browser.enterWebAddress()
                    browser.draftAddress = ""
                    editing = true
                }
                Section("Static preview") {
                    Label("Scripts disabled", systemImage: "nosign")
                    Label("Network requests blocked", systemImage: "network.slash")
                }
            } else {
                Section("Localhost previews") {
                    Label("Authenticated through your Mac", systemImage: "desktopcomputer")
                }
            }
        } label: {
            Image(systemName: "ellipsis.circle").frame(width: 44, height: 44).contentShape(Rectangle())
        }
        .accessibilityLabel("Browser options")
        .accessibilityHint("Forward navigation and preview details")
        .accessibilityIdentifier("browser-options")
    }

    private func navigate() {
        guard canSubmit else { return }
        editing = false
        Task { await browser.open(browser.draftAddress) }
    }
}
private struct BrowserSurface: UIViewRepresentable {
    let webView: WKWebView
    func makeUIView(context: Context) -> WKWebView { webView }
    func updateUIView(_ uiView: WKWebView, context: Context) {}
}
