import SwiftUI
import PhotosUI

struct IdeaPhoto: Codable, Identifiable, Equatable {
    var id = UUID()
    let jpegData: Data
    var uploadedPath: String? = nil
}

struct SpaceIdea: Codable, Identifiable, Equatable {
    var id = UUID()
    var text: String
    var completed = false
    var photos: [IdeaPhoto]? = nil
}

private struct SpaceIdeas: Codable {
    let computerID: UUID
    let projectID: String
    var ideas: [SpaceIdea]
}

/// Saves before publishing a change so failed writes never discard the user's queue.
@MainActor @Observable
final class IdeasQueueStore {
    private var spaces: [SpaceIdeas] = []
    private(set) var loadError: String?
    private let fileURL: URL

    init(fileURL: URL = URL.applicationSupportDirectory.appending(path: "Trill/ideas.json")) {
        self.fileURL = fileURL
        do {
            if FileManager.default.fileExists(atPath: fileURL.path) {
                spaces = try JSONDecoder().decode([SpaceIdeas].self, from: Data(contentsOf: fileURL))
            }
        } catch {
            loadError = "Your ideas could not be opened. Reopen Trill to try again. \(error.localizedDescription)"
        }
    }

    func ideas(computerID: UUID, projectID: String) -> [SpaceIdea] {
        spaces.first { $0.computerID == computerID && $0.projectID == projectID }?.ideas ?? []
    }

    func save(_ ideas: [SpaceIdea], computerID: UUID, projectID: String) throws {
        if let loadError { throw TrillError.message(loadError) }
        guard ideas.allSatisfy({ (!$0.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !($0.photos ?? []).isEmpty) }),
              Set(ideas.map(\.id)).count == ideas.count else {
            throw TrillError.message("Each idea needs text and a unique identity.")
        }
        var updatedSpaces = spaces.filter { $0.computerID != computerID || $0.projectID != projectID }
        if !ideas.isEmpty { updatedSpaces.append(SpaceIdeas(computerID: computerID, projectID: projectID, ideas: ideas)) }
        let data = try JSONEncoder().encode(updatedSpaces)
        try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUnlessOpen])
        spaces = updatedSpaces
    }
}

struct IdeasQueueView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    let computerID: UUID
    let projectID: String
    let spaceName: String
    var useInDraft: ((SpaceIdea) -> Void)? = nil
    var embedded = false
    @State private var captureText = ""
    @State private var capturePhotos: [IdeaPhoto] = []
    @State private var loadingPhoto = false
    @State private var editingIdea: SpaceIdea?
    @State private var saveError: String?
    @FocusState private var capturingIdea: Bool

    private var ideas: [SpaceIdea] { store.ideasQueue.ideas(computerID: computerID, projectID: projectID) }
    private var queuedIdeas: [SpaceIdea] { ideas.filter { !$0.completed } }
    private var completedIdeas: [SpaceIdea] { ideas.filter(\.completed) }

    var body: some View {
        if embedded { queueContent }
        else {
            NavigationStack { queueContent }
        }
    }

    private var queueContent: some View {
            List {
                if !embedded {
                Section {
                    TextField("An idea for later…", text: $captureText, axis: .vertical)
                        .lineLimit(2...6).focused($capturingIdea)
                        .accessibilityIdentifier("idea-capture")
                    IdeaPhotoAttachments(photos: $capturePhotos, loading: $loadingPhoto)
                    Button("Add to queue", systemImage: "plus") {
                        let text = captureText.trimmingCharacters(in: .whitespacesAndNewlines)
                        if save(ideas + [SpaceIdea(text: text, photos: capturePhotos)]) { captureText = ""; capturePhotos = []; capturingIdea = false }
                    }
                    .disabled((captureText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && capturePhotos.isEmpty) || loadingPhoto || store.ideasQueue.loadError != nil)
                    .accessibilityIdentifier("add-idea")
                } header: { Text(spaceName) } footer: {
                    Text("Saved on this device. Ideas stay here until you mark them done. Adding an idea doesn't send it to an agent.")
                }
                }
                if let error = store.ideasQueue.loadError {
                    Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.secondary) }
                } else {
                    Section {
                        if queuedIdeas.isEmpty {
                            ContentUnavailableView("Room for your next idea", systemImage: "lightbulb", description: Text("Capture follow-ups while your agent keeps working."))
                        }
                        ForEach(queuedIdeas) { idea in ideaRow(idea) }
                            .onMove { source, destination in
                                var reordered = queuedIdeas
                                reordered.move(fromOffsets: source, toOffset: destination)
                                _ = save(reordered + completedIdeas)
                            }
                    } header: {
                        HStack {
                            Text("Up next · \(queuedIdeas.count)")
                            Spacer()
                            if embedded { EditButton().disabled(queuedIdeas.count < 2) }
                        }
                    }
                    if !completedIdeas.isEmpty {
                        Section("Done") { ForEach(completedIdeas) { idea in ideaRow(idea) } }
                    }
                    if useInDraft == nil && !queuedIdeas.isEmpty {
                        Section { Text("Open a terminal in this Space to use an idea in its draft.").font(.footnote).foregroundStyle(.secondary) }
                    }
                }
            }
            .scrollDismissesKeyboard(.interactively)
            .navigationTitle(embedded ? "Write & queue" : "Ideas queue")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if !embedded { ToolbarItem(placement: .topBarLeading) { EditButton().disabled(queuedIdeas.count < 2) } }
                if !embedded { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            }
            .sheet(item: $editingIdea) { idea in
                IdeaEditor(idea: idea) { text, photos in
                    var updated = idea
                    updated.text = text
                    updated.photos = photos
                    try store.ideasQueue.save(ideas.map { $0.id == idea.id ? updated : $0 }, computerID: computerID, projectID: projectID)
                }
            }
            .alert("Couldn't save ideas", isPresented: Binding(get: { saveError != nil }, set: { if !$0 { saveError = nil } })) {
                Button("OK", role: .cancel) { saveError = nil }
            } message: { Text(saveError ?? "") }
    }

    private func ideaRow(_ idea: SpaceIdea) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Button {
                var updated = idea
                updated.completed.toggle()
                _ = save(ideas.map { $0.id == idea.id ? updated : $0 })
            } label: {
                Image(systemName: idea.completed ? "checkmark.circle.fill" : "circle")
                    .frame(minWidth: 44, minHeight: 44)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(idea.completed ? "Return to queue: \(idea.text)" : "Mark done: \(idea.text)")
            Button { editingIdea = idea } label: {
                VStack(alignment: .leading, spacing: 8) {
                    if !idea.text.isEmpty { Text(idea.text).foregroundStyle(idea.completed ? .secondary : .primary) }
                    IdeaPhotoThumbnails(photos: idea.photos ?? [])
                }.frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            }.buttonStyle(.plain)
            Menu {
                Button("Edit", systemImage: "pencil") { editingIdea = idea }
                if let useInDraft, !idea.completed {
                    Button("Use in draft", systemImage: "square.and.pencil") { useInDraft(idea); if !embedded { dismiss() } }
                }
                Button("Delete", systemImage: "trash", role: .destructive) { _ = save(ideas.filter { $0.id != idea.id }) }
            } label: {
                Image(systemName: "ellipsis").frame(minWidth: 44, minHeight: 44)
            }.accessibilityLabel("Actions for \(idea.text)")
        }
    }

    private func save(_ ideas: [SpaceIdea]) -> Bool {
        do {
            try store.ideasQueue.save(ideas, computerID: computerID, projectID: projectID)
            return true
        } catch { saveError = error.localizedDescription; return false }
    }
}

private struct IdeaEditor: View {
    @Environment(\.dismiss) private var dismiss
    let save: (String, [IdeaPhoto]) throws -> Void
    @State private var text: String
    @State private var photos: [IdeaPhoto]
    @State private var loadingPhoto = false
    @State private var saveError: String?

    init(idea: SpaceIdea, save: @escaping (String, [IdeaPhoto]) throws -> Void) {
        self.save = save
        _text = State(initialValue: idea.text)
        _photos = State(initialValue: idea.photos ?? [])
    }

    var body: some View {
        NavigationStack {
            VStack {
                TextEditor(text: $text).accessibilityLabel("Idea")
                IdeaPhotoAttachments(photos: $photos, loading: $loadingPhoto)
            }.padding()
                .alert("Couldn't save idea", isPresented: Binding(get: { saveError != nil }, set: { if !$0 { saveError = nil } })) {
                    Button("OK", role: .cancel) { saveError = nil }
                } message: { Text(saveError ?? "") }
                .navigationTitle("Edit idea").navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Save") {
                            do {
                                try save(text.trimmingCharacters(in: .whitespacesAndNewlines), photos)
                                dismiss()
                            } catch { saveError = error.localizedDescription }
                        }.disabled(loadingPhoto || (text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && photos.isEmpty))
                    }
                }
        }
    }
}


struct IdeaPhotoThumbnails: View {
    let photos: [IdeaPhoto]
    var body: some View {
        if !photos.isEmpty {
            ScrollView(.horizontal) {
                HStack {
                    ForEach(photos) { photo in
                        if let image = UIImage(data: photo.jpegData) {
                            Image(uiImage: image).resizable().scaledToFill().frame(width: 64, height: 64)
                                .clipped().clipShape(RoundedRectangle(cornerRadius: 8)).accessibilityLabel("Attached photo")
                        }
                    }
                }
            }
        }
    }
}

struct IdeaPhotoAttachments: View {
    @Binding var photos: [IdeaPhoto]
    @Binding var loading: Bool
    @State private var selection: [PhotosPickerItem] = []
    @State private var photoError: String?
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !photos.isEmpty {
                ScrollView(.horizontal) {
                    HStack {
                        ForEach(photos) { photo in
                            VStack {
                                IdeaPhotoThumbnails(photos: [photo]).frame(width: 64)
                                Button("Remove", role: .destructive) { photos.removeAll { $0.id == photo.id } }
                                    .font(.caption).accessibilityLabel("Remove attached photo")
                            }
                        }
                    }
                }
            }
            PhotosPicker(selection: $selection, maxSelectionCount: max(1, 6 - photos.count), matching: .images) {
                Label("Add photo", systemImage: "photo.badge.plus")
            }.disabled(loading || photos.count >= 6).accessibilityIdentifier("terminal-add-photo")
            if loading { ProgressView("Preparing photos…") }
        }
        .task(id: selection) {
            guard !selection.isEmpty else { return }
            loading = true
            defer { loading = false }
            do {
                var prepared: [IdeaPhoto] = []
                for selectedPhoto in selection {
                    guard let data = try await selectedPhoto.loadTransferable(type: Data.self) else {
                        throw TrillError.message("This photo couldn't be loaded. Choose another image.")
                    }
                    try Task.checkCancellation()
                    prepared.append(IdeaPhoto(jpegData: try TerminalPhoto.jpeg(from: data)))
                }
                photos += prepared
                selection = []
            } catch is CancellationError { }
            catch { photoError = error.localizedDescription; selection = [] }
        }
        .alert("Couldn't add photo", isPresented: Binding(get: { photoError != nil }, set: { if !$0 { photoError = nil } })) {
            Button("OK", role: .cancel) { photoError = nil }
        } message: { Text(photoError ?? "") }
    }
}
