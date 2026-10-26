package openurl

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func ValidateHTTPS(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Host != u.Hostname() ||
		u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "\x00\r\n\t ") {
		return fmt.Errorf("opening requires a canonical provider-owned HTTPS destination")
	}
	return nil
}

// Launch waits for and reaps the OS launcher, not the browser/page. Success says
// nothing about page load. This strict path is separate from legacy TUI opening.
func Launch(ctx context.Context, value string) error {
	return launch(ctx, value, runtime.GOOS, func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = 250 * time.Millisecond
		return cmd.Run()
	})
}

func launch(ctx context.Context, value, goos string, run func(context.Context, string, ...string) error) error {
	if err := ValidateHTTPS(value); err != nil {
		return err
	}
	name := "xdg-open"
	switch goos {
	case "darwin":
		name = "open"
	case "linux":
	default:
		return fmt.Errorf("browser opening is unsupported on this platform")
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := run(bounded, name, value)
	if bounded.Err() != nil {
		return bounded.Err()
	}
	return err
}
