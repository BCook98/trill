import SwiftUI

struct MacFolder: Decodable, Identifiable, Hashable {
    let name: String
    let path: String
    var id: String { path }
}
struct MacFolderListing: Decodable {
    let path: String
    let parentPath: String?
    let folders: [MacFolder]
    let truncated: Bool
}

/// The system Files picker cannot browse a companion's filesystem. These native
/// navigation/list controls browse directory names through the paired API only.
struct MacFolderField: View {
    @Environment(AppStore.self) private var store
    @Binding var directory: String
    var initialDirectory: String? = nil
    var allowsSpaceDefault = false
    @State private var showingPicker = false

    var body: some View {
        Button { showingPicker = true } label: {
            HStack(spacing: 12) {
                Image(systemName: "folder").foregroundStyle(.tint)
                VStack(alignment: .leading, spacing: 3) {
                    Text("Folder").foregroundStyle(.primary)
                    Text(directory.isEmpty ? (allowsSpaceDefault ? "Space default" : "Choose a folder…") : directory)
                        .font(.subheadline).foregroundStyle(.secondary)
                        .lineLimit(2).truncationMode(.middle)
                }
                Spacer(minLength: 0)
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
            }
        }
        .disabled(store.api == nil || store.changingComputer || store.readOnly)
        .accessibilityHint("Browse folders on \(store.computer?.name ?? "your Mac")")
        .sheet(isPresented: $showingPicker) {
            if let api = store.api {
                MacFolderPicker(api: api, initialDirectory: directory.isEmpty ? initialDirectory : directory) { directory = $0 }
                    .id(store.activeComputerID)
            }
        }
        if allowsSpaceDefault && !directory.isEmpty {
            Button("Use space default") { directory = "" }
        }
    }
}

struct MacFolderPicker: View {
    let api: APIClient
    let initialDirectory: String?
    let choose: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var navigation: [MacFolder] = []

    var body: some View {
        NavigationStack(path: $navigation) {
            MacFolderContents(api: api, directory: initialDirectory, cancel: { dismiss() }, choose: select)
                .navigationDestination(for: MacFolder.self) { folder in
                    MacFolderContents(api: api, directory: folder.path, cancel: { dismiss() }, choose: select)
                }
        }
    }
    private func select(_ directory: String) {
        choose(directory)
        dismiss()
    }
}

private struct MacFolderContents: View {
    let api: APIClient
    let directory: String?
    let cancel: () -> Void
    let choose: (String) -> Void
    @State private var listing: MacFolderListing?
    @State private var error: String?
    @State private var search = ""

    private var folders: [MacFolder] {
        (listing?.folders ?? []).filter { search.isEmpty || $0.name.localizedStandardContains(search) }
    }
    private var title: String {
        guard let path = listing?.path else { return "Choose a folder" }
        return path == "/" ? api.computer.name : URL(fileURLWithPath: path).lastPathComponent
    }
    var body: some View {
        List {
            Section {
                Label(api.computer.name, systemImage: "laptopcomputer")
                if let listing {
                    Text(listing.path).font(.footnote).foregroundStyle(.secondary)
                        .textSelection(.enabled).accessibilityIdentifier("mac-folder-path")
                }
            }
            if let error {
                Section {
                    Text(error).foregroundStyle(.secondary)
                    Button("Try again") { Task { await load() } }
                    if directory != nil {
                        NavigationLink(value: MacFolder(name: "Home", path: "")) { Label("Home folder", systemImage: "house") }
                    }
                }
            } else if let listing {
                if search.isEmpty, let parent = listing.parentPath {
                    Section {
                        NavigationLink(value: MacFolder(name: "Parent folder", path: parent)) {
                            Label("Parent folder", systemImage: "arrow.up.doc")
                        }
                    }
                }
                Section {
                    ForEach(folders) { folder in
                        NavigationLink(value: folder) { Label(folder.name, systemImage: "folder") }
                    }
                    if folders.isEmpty {
                        Text(search.isEmpty ? "No folders" : "No matching folders").foregroundStyle(.secondary)
                    }
                }
                if listing.truncated {
                    Section { Text("This folder has too many items to display. Open a subfolder to continue.").font(.footnote).foregroundStyle(.secondary) }
                }
            } else {
                ProgressView("Loading folders…")
            }
        }
        .navigationTitle(title).navigationBarTitleDisplayMode(.inline)
        .searchable(text: $search, prompt: "Folder name")
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel", action: cancel) }
            ToolbarItem(placement: .confirmationAction) {
                Button("Choose") { if let listing { choose(listing.path) } }
                    .disabled(listing == nil || error != nil).accessibilityIdentifier("choose-mac-folder")
            }
        }
        .task { await load() }
        .refreshable { await load() }
    }
    @MainActor private func load() async {
        error = nil
        do {
            let result = try await api.folders(directory: directory)
            try Task.checkCancellation()
            listing = result
        } catch {
            guard !Task.isCancelled else { return }
            self.error = error.localizedDescription
        }
    }
}
