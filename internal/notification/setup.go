package notification

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const PrivacySettings = "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension"
const NotificationSettings = "x-apple.systempreferences:com.apple.Notifications-Settings.extension"
const launchServicesRegister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
const SetupInstructions = `Radar's small notification companion supplies the macOS app identity,
native task alerts, and notification clicks. It is not another dashboard.
Radar remains usable if you defer notification setup.

Choosing Set up & test verifies the companion's code signature, registers it
with Launch Services, then launches the test. Homebrew cannot register the app
from its sandboxed install hooks; this user-initiated step works for all installs.
Registration does not grant either macOS permission or change Gatekeeper policy.

There are TWO independent approvals:

1. Launch approval (Gatekeeper)
   Approve only RadarNotifier.app from the official release you chose to trust.
   Apple cannot verify the developer/notarization of this ad-hoc-signed build.
   If blocked, choose Done (not Move to Trash), then:
   Apple menu → System Settings → Privacy & Security → Security → Open Anyway.
   Authenticate and confirm Open, then return here and retry the test.
   Open Anyway appears after a blocked launch. A changed helper can need it again.

2. Notification permission
   Choose Allow in the notification prompt, or open:
   Apple menu → System Settings → Notifications → Radar → Allow notifications.
   Focus and alert settings can suppress banners even when permission is granted.

Settings buttons open a pane only; they do not grant either permission.
Labels and pane routing can vary by macOS version. Use the directions above
if a direct link fails. Never disable Gatekeeper or remove quarantine.
`

func Setup(in io.Reader, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS notification setup is not needed on this platform")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(executable); err == nil {
		executable = p
	}
	helper := filepath.Clean(filepath.Join(filepath.Dir(executable), "../libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier"))
	return setup(in, out, helper, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	})
}

type setupCommand func(context.Context, string, ...string) ([]byte, error)

const privacyDirections = "System Settings → Privacy & Security → Security → Open Anyway."
const notificationDirections = "System Settings → Notifications → Radar → Allow notifications."

func setup(in io.Reader, out io.Writer, helper string, run setupCommand) error {
	fmt.Fprint(out, "\nNotifications · Optional\n\nGet desktop alerts when tasks need attention.\nmacOS may ask you to approve RadarNotifier and allow notifications.\n")
	scanner := bufio.NewScanner(in)
	attempted, help := false, false
	settings, diagnostics := "", ""
	for {
		primary := "Set up & test"
		if attempted {
			primary = "Retry"
		}
		fmt.Fprintf(out, "\n[t/Enter] %s", primary)
		if help || settings == PrivacySettings {
			fmt.Fprint(out, "  [p] Privacy & Security")
		}
		if help || settings == NotificationSettings {
			fmt.Fprint(out, "  [n] Notification Settings")
		}
		fmt.Fprint(out, "  [s] Skip for now  [h] Help\n> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		action := strings.ToLower(strings.TrimSpace(scanner.Text()))
		switch action {
		case "s":
			fmt.Fprintln(out, "Skipped — notification settings left as they are. Resume with radar setup notifications.")
			return nil
		case "h":
			fmt.Fprint(out, "\n", SetupInstructions)
			fmt.Fprintf(out, "Companion: %s\n", helper)
			if diagnostics != "" {
				fmt.Fprintf(out, "\nLast check:\n%s\n", diagnostics)
			}
			help = true
			continue
		case "p", "n":
			link, directions := PrivacySettings, privacyDirections
			if action == "n" {
				link, directions = NotificationSettings, notificationDirections
			}
			if !help && settings != link {
				fmt.Fprintln(out, "Choose Help for settings shortcuts and approval instructions.")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			output, err := run(ctx, "open", link)
			cancel()
			if err != nil {
				diagnostics = setupFailure(out, "Could not open Settings", err, output)
				fmt.Fprintln(out, "Open manually:", directions)
			} else {
				fmt.Fprintln(out, directions)
				fmt.Fprintln(out, "Opening Settings does not grant permission. Return here when ready.")
			}
			continue
		case "", "t":
			// Enter is an explicit selection of the visible primary action.
			// EOF, Help, Skip and settings links never launch a test.
		default:
			fmt.Fprintln(out, "Choose one of the actions above.")
			continue
		}

		attempted, help = true, false
		settings, diagnostics = "", ""
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		// Homebrew's install sandbox deliberately blocks Launch Services. Perform
		// registration only after the user selects the foreground test, never
		// from an install hook, daemon, settings link or deferred setup.
		bundle := filepath.Dir(filepath.Dir(filepath.Dir(helper)))
		if output, err := run(ctx, "/usr/bin/codesign", "--verify", "--strict", bundle); err != nil {
			cancel()
			diagnostics = setupFailure(out, "Could not verify the notification companion", err, output)
			fmt.Fprintln(out, "Repair the installed companion before retrying. No registration or test was attempted.")
			continue
		}
		if output, err := run(ctx, launchServicesRegister, "-f", bundle); err != nil {
			cancel()
			diagnostics = setupFailure(out, "Could not register the notification companion", err, output)
			fmt.Fprintln(out, "Run radar setup notifications in a normal host terminal, then retry. No test was launched.")
			continue
		}
		payload, _ := json.Marshal(Notification{Title: "Radar notification test", Body: "Click to open Radar's GitHub page.", URL: "https://github.com/ChristianMoesl/radar"})
		output, err := run(ctx, helper, "--test", base64.StdEncoding.EncodeToString(payload))
		cancel()
		if err != nil {
			diagnostics = setupFailure(out, "Could not run the notification test", err, output)
			// A launch error can also mean a missing file or another failure. Do
			// not claim Gatekeeper blocked it or notification permission is denied.
			fmt.Fprintln(out, "If macOS blocked RadarNotifier, choose Done (not Move to Trash), then:")
			fmt.Fprintln(out, privacyDirections)
			fmt.Fprintln(out, "Approve only the app you trust. Otherwise fix the reported error before retrying.")
			settings = PrivacySettings
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		output, err = run(ctx, helper, "--status")
		cancel()
		if err != nil {
			diagnostics = setupFailure(out, "Could not check notification permission", err, output)
			fmt.Fprintln(out, "Retry or check Radar in Notification Settings.")
			settings = NotificationSettings
			continue
		}
		diagnostics = "macOS status: " + strings.TrimSpace(ansi.Strip(string(output)))
		switch notificationAuthorization(output) {
		case "authorized", "provisional", "ephemeral":
			// Permission alone does not prove delivery or click handling.
		case "denied":
			fmt.Fprintln(out, "! Notifications are turned off for Radar.")
			fmt.Fprintln(out, notificationDirections)
			settings = NotificationSettings
			continue
		case "not_determined":
			fmt.Fprintln(out, "! Notification permission has not been granted yet.")
			fmt.Fprintln(out, "Choose Allow when macOS asks, or enable Radar in Notification Settings.")
			settings = NotificationSettings
			continue
		default:
			fmt.Fprintln(out, "! Could not determine notification permission. Retry or check Notification Settings; details are in Help.")
			settings = NotificationSettings
			continue
		}
		fmt.Fprint(out, "Did the notification arrive and open the test page when clicked? [y/N] ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		if strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
			clearLaunchFailure(helper)
			fmt.Fprintln(out, "✓ Notifications ready")
			return nil
		}
		fmt.Fprintln(out, "Not confirmed. Check Notification Center, Focus and banner settings, then retry.")
		settings = NotificationSettings
	}
}

// Preserve the full diagnostic in Help, but keep the normal failure readable.
func setupFailure(out io.Writer, message string, err error, output []byte) string {
	details := message + ": " + ansi.Strip(err.Error())
	if body := strings.TrimSpace(ansi.Strip(string(output))); body != "" {
		details += "\n" + body
	}
	brief := strings.Join(strings.Fields(details), " ")
	fmt.Fprintf(out, "! %s\n", ansi.Truncate(brief, 160, "… (details in Help)"))
	return details
}

func notificationAuthorization(output []byte) string {
	status := ""
	for _, field := range strings.Fields(string(output)) {
		if value, ok := strings.CutPrefix(field, "authorization="); ok {
			if status != "" || value == "" {
				return "" // Missing, duplicate or malformed status is not approval.
			}
			status = value
		}
	}
	return status
}

func failureMarker(helper string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(helper)))), "notifier-launch-failure")
}

// These helpers are shared with the Darwin sender; no existing macOS permission
// or system preference is modified. A successful explicit test clears the hint.
func clearLaunchFailure(helper string) { _ = os.Remove(failureMarker(helper)) }
