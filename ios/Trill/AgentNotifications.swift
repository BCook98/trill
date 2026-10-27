import SwiftUI
import UserNotifications
import UIKit

struct AgentNotificationPreferences: Codable, Equatable {
    var attention = false
    var completion = false
    var enabled: Bool { attention || completion }
}

struct AgentNotificationRoute: Equatable {
    let computerID: UUID
    let sessionID: String
    let eventID: UUID

    init?(userInfo: [AnyHashable: Any]) {
        guard let computer = userInfo["computerId"] as? String, let computerID = UUID(uuidString: computer),
              let sessionID = userInfo["sessionId"] as? String,
              sessionID.range(of: "^herdr-[a-f0-9]{32}$", options: .regularExpression) != nil,
              let event = userInfo["eventId"] as? String, let eventID = UUID(uuidString: event) else { return nil }
        self.computerID = computerID; self.sessionID = sessionID; self.eventID = eventID
    }
}

struct AgentNotificationStatus: Decodable {
    var available: Bool
    var registration: Registration?
    struct Registration: Decodable {
        let attention: Bool
        let completion: Bool
        let computerId: String
        let expiresAt: String
    }
}

@MainActor @Observable
final class AgentNotificationDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    var deviceToken: String?
    var registrationError: String?
    var pendingRoute: AgentNotificationRoute?
    var authorizationStatus: UNAuthorizationStatus = .notDetermined
    // Explicit build configuration: a simulator or an unprovisioned build must
    // never advertise background delivery. See docs/AGENT_NOTIFICATIONS.md.
    static var environment: String? {
        guard let value = Bundle.main.object(forInfoDictionaryKey: "TrillPushEnvironment") as? String,
              ["sandbox", "production"].contains(value) else { return nil }
        return value
    }
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }
    func refreshAuthorization() async {
        authorizationStatus = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
    }
    var authorized: Bool { authorizationStatus == .authorized || authorizationStatus == .provisional || authorizationStatus == .ephemeral }
    func requestPermission() async throws {
        guard Self.environment != nil else { throw TrillError.message("Background alerts are not enabled in this build.") }
        _ = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound])
        await refreshAuthorization()
        guard authorized else { throw TrillError.message("Notifications are off in iOS Settings.") }
        UIApplication.shared.registerForRemoteNotifications()
    }
    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        self.deviceToken = deviceToken.map { String(format: "%02x", $0) }.joined()
        registrationError = nil
    }
    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        deviceToken = nil
        registrationError = "Couldn’t register with Apple for push notifications. Try again when connected."
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        // One APNs path in foreground and background; no competing local alerts.
        completionHandler([.banner, .list, .sound])
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let route = AgentNotificationRoute(userInfo: response.notification.request.content.userInfo)
        Task { @MainActor in
            if response.actionIdentifier == UNNotificationDefaultActionIdentifier { self.pendingRoute = route }
            completionHandler()
        }
    }
    static func removeDelivered(computerID: UUID) async {
        let center = UNUserNotificationCenter.current()
        let delivered = await center.deliveredNotifications()
        let identifiers = delivered.filter {
            AgentNotificationRoute(userInfo: $0.request.content.userInfo)?.computerID == computerID
        }.map { $0.request.identifier }
        center.removeDeliveredNotifications(withIdentifiers: identifiers)
    }
}

struct AgentNotificationSettingsView: View {
    let computerID: UUID
    @Environment(AppStore.self) private var store
    @Environment(AgentNotificationDelegate.self) private var notifications
    @Environment(\.openURL) private var openURL
    @State private var status: AgentNotificationStatus?
    @State private var saving = false
    @State private var failure: String?

    private var preferences: AgentNotificationPreferences { store.computers.first { $0.id == computerID }?.notificationPreferences ?? .init() }
    private var canEnable: Bool { AgentNotificationDelegate.environment != nil && status?.available == true }
    var body: some View {
        Form {
            Section {
                Toggle("Needs attention", isOn: preferenceBinding(\.attention)).disabled(saving || (!preferences.attention && !canEnable))
                Toggle("Finished work", isOn: preferenceBinding(\.completion)).disabled(saving || (!preferences.completion && !canEnable))
            } header: { Text(store.computers.first { $0.id == computerID }?.computer.name ?? "Computer") } footer: {
                Text("Receive alerts when a Herdr agent newly needs input or finishes work. Alerts contain no terminal content or session names.")
            }
            Section {
                if AgentNotificationDelegate.environment == nil {
                    Text("Background alerts are not enabled in this build.")
                } else if status?.available == false {
                    Text("Background alerts are not configured on this computer. An updated companion and a push relay are required.")
                } else if notifications.authorizationStatus == .denied {
                    Text("Notifications are off in iOS Settings.")
                    Button("Open Notification Settings") { if let url = URL(string: UIApplication.openNotificationSettingsURLString) { openURL(url) } }
                } else if preferences.enabled {
                    if status?.registration != nil && notifications.deviceToken != nil && failure == nil {
                        Label("Background alerts registered", systemImage: "checkmark.circle")
                    } else {
                        Text("Waiting for notification registration. Alerts may not arrive yet.")
                    }
                } else { Text("Alerts are off for this computer.") }
                if let message = failure ?? notifications.registrationError ?? store.notificationSyncErrors[computerID] {
                    Text(message).foregroundStyle(.red)
                }
                Button("Check notification connection") { Task { await reload() } }.disabled(saving)
            } footer: {
                Text("Your Mac must be awake, online, and running Herdr and its companion. Delivery is best effort. Open Trill at least once every seven days to renew registration. iOS Focus and notification settings also affect delivery.")
            }
        }
        .navigationTitle("Agent Notifications")
        .task { await reload() }
        .onChange(of: notifications.deviceToken) { _, _ in Task { await reload() } }
    }
    private func preferenceBinding(_ keyPath: WritableKeyPath<AgentNotificationPreferences, Bool>) -> Binding<Bool> {
        Binding(get: { preferences[keyPath: keyPath] }, set: { value in
            Task {
                saving = true; failure = nil
                defer { saving = false }
                do {
                    if value { try await notifications.requestPermission() }
                    var next = preferences; next[keyPath: keyPath] = value
                    try store.setNotificationPreferences(next, computerID: computerID)
                    try await store.syncNotifications(computerID: computerID, notifications: notifications)
                    status = try await store.notificationStatus(computerID: computerID)
                } catch { failure = error.localizedDescription }
            }
        })
    }
    private func reload() async {
        saving = true; defer { saving = false }
        await notifications.refreshAuthorization()
        do {
            if preferences.enabled && notifications.authorized && AgentNotificationDelegate.environment != nil {
                UIApplication.shared.registerForRemoteNotifications()
            }
            try await store.syncNotifications(computerID: computerID, notifications: notifications)
            status = try await store.notificationStatus(computerID: computerID)
            failure = nil
        } catch { failure = error.localizedDescription }
    }
}
