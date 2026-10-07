//go:build darwin

package notification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"radar/internal/update"
)

const notifierRelativePath = "../libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier"

type platformSender struct {
	executable string
}

func newPlatformSender() Sender {
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return newPlatformSenderForExecutable(executable)
}

func newPlatformSenderForExecutable(radarExecutable string) Sender {
	notifier := notifierPath(radarExecutable)
	info, err := os.Stat(notifier)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil
	}
	return platformSender{executable: notifier}
}

func notifierPath(radarExecutable string) string {
	return filepath.Clean(filepath.Join(filepath.Dir(radarExecutable), notifierRelativePath))
}

func (s platformSender) Send(ctx context.Context, notification Notification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(payload)
	identity, err := update.FileDigest(s.executable)
	if err != nil {
		return err
	}
	if blocked, err := os.ReadFile(failureMarker(s.executable)); err == nil && string(blocked) == identity {
		return fmt.Errorf("notifier launch previously failed; run radar setup notifications")
	}
	// Do not detach an unobserved process and repeatedly trigger blocked-launch
	// alerts. Background delivery never requests notification authorization.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, s.executable, "--notify", encoded).Run(); err != nil {
		_ = os.WriteFile(failureMarker(s.executable), []byte(identity), 0600)
		return fmt.Errorf("notifier launch failed; run radar setup notifications to retry setup: %w", err)
	}
	return nil
}
