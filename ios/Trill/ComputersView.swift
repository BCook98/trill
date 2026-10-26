import SwiftUI

struct ComputersView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var showPairing = false
    @State private var forgetTarget: SavedComputer?
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    ForEach(store.computers) { saved in
                        Button {
                            Task {
                                do { try await store.selectComputer(id: saved.id); dismiss() }
                                catch { self.error = error.localizedDescription }
                            }
                        } label: {
                            HStack(spacing: 14) {
                                Image(systemName: "laptopcomputer").font(.title3).foregroundStyle(Color.accentColor)
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(saved.computer.name).font(.headline).foregroundStyle(.primary)
                                    Text(saved.computer.address).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                }
                                Spacer(minLength: 8)
                                if saved.id == store.activeComputerID {
                                    Image(systemName: "checkmark").foregroundStyle(Color.accentColor).accessibilityLabel("Selected")
                                }
                            }.padding(.vertical, 8).contentShape(Rectangle())
                        }
                        .accessibilityIdentifier("computer-" + saved.id.uuidString)
                        .swipeActions {
                            Button("Forget", systemImage: "trash", role: .destructive) { forgetTarget = saved }
                        }
                        .contextMenu {
                            Button("Forget computer…", systemImage: "trash", role: .destructive) { forgetTarget = saved }
                        }
                    }
                    Button("Add computer", systemImage: "plus") { showPairing = true }
                        .accessibilityIdentifier("add-computer")
                } footer: {
                    Text("Switching disconnects Trill from the previous computer. Its sessions keep running. Trill keeps drafts separate for each computer.")
                }
                if store.changingComputer {
                    Section { HStack(spacing: 12) { ProgressView(); Text("Changing computer…") } }
                }
                if let error { Section { Text(error).foregroundStyle(.red).font(.subheadline) } }
            }
            .disabled(store.busy)
            .navigationTitle("Computers").navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done", systemImage: "checkmark") { dismiss() }.disabled(store.changingComputer)
                }
            }
            .interactiveDismissDisabled(store.changingComputer)
            .sheet(isPresented: $showPairing) { PairComputerView(prefillCurrent: false) }
            .alert("Forget \(forgetTarget?.computer.name ?? "computer")?", isPresented: Binding(
                get: { forgetTarget != nil }, set: { if !$0 { forgetTarget = nil } }
            )) {
                Button("Cancel", role: .cancel) { forgetTarget = nil }
                if let target = forgetTarget {
                    Button("Forget computer", role: .destructive) {
                        Task {
                            do { try await store.forgetComputer(id: target.id) }
                            catch { self.error = error.localizedDescription }
                        }
                    }
                }
            } message: {
                Text("Trill removes this computer's pairing and drafts from this device. Its sessions keep running. Other saved computers stay paired.")
            }
        }
    }
}
