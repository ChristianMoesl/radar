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
)

const PrivacySettings = "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension"
const NotificationSettings = "x-apple.systempreferences:com.apple.Notifications-Settings.extension"
const SetupInstructions = `Radar's small notification companion supplies the macOS app identity,
native task alerts, and notification clicks. It is not another dashboard.
Radar remains usable if you defer notification setup.

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

func setup(in io.Reader, out io.Writer, helper string, run setupCommand) error {
	fmt.Fprint(out, SetupInstructions)
	fmt.Fprintf(out, "Companion: %s\n", helper)
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "\n[t] Set up / test  [p] Privacy & Security  [n] Notifications Settings  [d] Done / defer\n> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		action := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if action == "d" || action == "" {
			fmt.Fprintln(out, "You can return with radar setup notifications. Your notification preference was not changed by deferring.")
			return nil
		}
		if action != "t" && action != "p" && action != "n" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if action != "t" {
			link := PrivacySettings
			if action == "n" {
				link = NotificationSettings
			}
			if _, err := run(ctx, "open", link); err != nil {
				fmt.Fprintf(out, "Could not open Settings: %v. Follow the written directions instead.\n", err)
			}
			cancel()
			continue
		}
		payload, _ := json.Marshal(Notification{Title: "Radar notification test", Body: "Click to open Radar's GitHub page.", URL: "https://github.com/ChristianMoesl/radar"})
		output, err := run(ctx, helper, "--test", base64.StdEncoding.EncodeToString(payload))
		cancel()
		if err != nil {
			fmt.Fprintf(out, "Helper launch/test did not finish: %v %s\nThis may be missing files, launch approval, or another error—not proof of a notification-permission denial. Follow the instructions and retry when ready.\n", err, strings.TrimSpace(string(output)))
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		output, err = run(ctx, helper, "--status")
		cancel()
		if err != nil {
			fmt.Fprintf(out, "Could not check notification authorization: %v\n", err)
			continue
		}
		fmt.Fprintf(out, "macOS status: %s\n", strings.TrimSpace(string(output)))
		if !strings.Contains(string(output), "authorization=authorized") && !strings.Contains(string(output), "authorization=provisional") && !strings.Contains(string(output), "authorization=ephemeral") {
			fmt.Fprintln(out, "Notification setup is not authorized yet. Enable Radar in Notifications Settings and retry.")
			continue
		}
		fmt.Fprint(out, "Did you receive the test and did clicking it open Radar's GitHub page? [y/N] ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		if strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
			clearLaunchFailure(helper)
			fmt.Fprintln(out, "Notification delivery and click confirmed. Notifications are ready.")
		} else {
			fmt.Fprintln(out, "Not confirmed. Check Notification Center, Focus and banner settings; then retry. Authorization alone does not prove delivery/click behavior.")
		}
	}
}

func failureMarker(helper string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(helper)))), "notifier-launch-failure")
}

// These helpers are shared with the Darwin sender; no existing macOS permission
// or system preference is modified. A successful explicit test clears the hint.
func clearLaunchFailure(helper string) { _ = os.Remove(failureMarker(helper)) }
