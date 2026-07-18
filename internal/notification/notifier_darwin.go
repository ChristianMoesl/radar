//go:build darwin

package notification

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const notificationAppleScript = `on run argv
	display notification (item 2 of argv) with title (item 1 of argv)
end run`

type platformSender struct{}

func newPlatformSender() Sender {
	return platformSender{}
}

func (platformSender) Send(ctx context.Context, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "osascript", "-e", notificationAppleScript, title, body).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("osascript: %w: %s", err, detail)
		}
		return fmt.Errorf("osascript: %w", err)
	}
	return nil
}
