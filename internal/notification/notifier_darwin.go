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
	cmd := exec.Command(s.executable, "--notify", encoded)
	if err := cmd.Start(); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("start radar notifier: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release radar notifier: %w", err)
	}
	return nil
}
