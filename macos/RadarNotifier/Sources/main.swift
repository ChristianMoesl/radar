import AppKit
import Foundation
import UserNotifications

private struct NotificationPayload: Decodable {
    let title: String
    let body: String
    let url: String?
}

private final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    private let payload: NotificationPayload?
    private let reportsStatus: Bool
    private let center = UNUserNotificationCenter.current()

    init(payload: NotificationPayload?, reportsStatus: Bool) {
        self.payload = payload
        self.reportsStatus = reportsStatus
        super.init()
        center.delegate = self
    }

    func applicationDidFinishLaunching(_ notification: Foundation.Notification) {
        NSApp.setActivationPolicy(.accessory)

        if reportsStatus {
            reportStatus()
            return
        }

        guard let payload else {
            // Notification Center launches the app again to deliver a click.
            // Give the delegate time to receive it, then leave quietly if this
            // was an unrelated launch.
            DispatchQueue.main.asyncAfter(deadline: .now() + 10) {
                NSApp.terminate(nil)
            }
            return
        }

        deliver(payload)
    }

    private func reportStatus() {
        center.getNotificationSettings { settings in
            self.center.getDeliveredNotifications { notifications in
                let status: String
                switch settings.authorizationStatus {
                case .notDetermined: status = "not_determined"
                case .denied: status = "denied"
                case .authorized: status = "authorized"
                case .provisional: status = "provisional"
                case .ephemeral: status = "ephemeral"
                @unknown default: status = "unknown"
                }
                print("authorization=\(status) delivered=\(notifications.count)")
                fflush(stdout)
                self.finish()
            }
        }
    }

    private func deliver(_ payload: NotificationPayload) {
        center.getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .authorized, .provisional, .ephemeral:
                self.add(payload)
            case .notDetermined:
                self.center.requestAuthorization(options: [.alert]) { granted, error in
                    if let error {
                        self.fail("could not request notification permission: \(error)")
                    } else if granted {
                        self.add(payload)
                    } else {
                        self.finish()
                    }
                }
            case .denied:
                self.finish()
            @unknown default:
                self.finish()
            }
        }
    }

    private func add(_ payload: NotificationPayload) {
        let content = UNMutableNotificationContent()
        content.title = payload.title
        content.body = payload.body
        if let url = payload.url, !url.isEmpty {
            content.userInfo = ["url": url]
        }

        let request = UNNotificationRequest(
            identifier: UUID().uuidString,
            content: content,
            trigger: nil
        )
        center.add(request) { error in
            if let error {
                self.fail("could not deliver notification: \(error)")
            } else {
                self.finish()
            }
        }
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        defer {
            completionHandler()
            finish()
        }

        guard response.actionIdentifier == UNNotificationDefaultActionIdentifier,
              let value = response.notification.request.content.userInfo["url"] as? String,
              let url = URL(string: value),
              let scheme = url.scheme?.lowercased(),
              scheme == "https" || scheme == "http"
        else {
            return
        }

        NSWorkspace.shared.open(url)
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .list])
    }

    private func fail(_ message: String) {
        FileHandle.standardError.write(Data("radar-notifier: \(message)\n".utf8))
        finish()
    }

    private func finish() {
        DispatchQueue.main.async {
            NSApp.terminate(nil)
        }
    }
}

private func notificationPayload() -> NotificationPayload? {
    let arguments = CommandLine.arguments
    guard arguments.count == 3, arguments[1] == "--notify" else {
        return nil
    }
    guard let data = Data(base64Encoded: arguments[2]) else {
        FileHandle.standardError.write(Data("radar-notifier: invalid notification payload\n".utf8))
        return nil
    }
    do {
        return try JSONDecoder().decode(NotificationPayload.self, from: data)
    } catch {
        FileHandle.standardError.write(Data("radar-notifier: invalid notification payload: \(error)\n".utf8))
        return nil
    }
}

let application = NSApplication.shared
private let delegate = AppDelegate(
    payload: notificationPayload(),
    reportsStatus: CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--status"
)
application.delegate = delegate
application.run()
