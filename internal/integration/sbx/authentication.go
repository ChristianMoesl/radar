package sbx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"radar/internal/command"
	"radar/internal/integration"
	"radar/internal/integration/sbx/auth"
	sbxclient "radar/internal/integration/sbx/client"
)

func (Source) EnsureAuthentication(ctx context.Context, req integration.AuthenticationRequest) (integration.AuthenticationResult, error) {
	if !authenticationRequired(req) {
		return integration.AuthenticationResult{}, nil
	}
	executable, err := sbxclient.New(sbxclient.ExecRunner{}).Executable()
	if err != nil {
		return integration.AuthenticationResult{}, nil
	}
	return authenticate(ctx, executable)
}

// Keep the same executable for the probe and interactive login. In particular,
// Windows SBX credentials must be refreshed by sbx.exe, not a native installation.
func authenticate(ctx context.Context, executable string) (integration.AuthenticationResult, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := command.CommandContext(checkCtx, executable, "ls", "--json")
	if executable == "sbx.exe" {
		check.Dir = "/"
	}
	output, err := check.CombinedOutput()
	if err == nil || !auth.IsRequired(string(output)+"\n"+err.Error()) {
		return integration.AuthenticationResult{}, nil
	}
	fmt.Fprintf(os.Stderr, "radar: sbx is not signed in; starting %s login\n", executable)
	login := exec.CommandContext(ctx, executable, "login")
	if executable == "sbx.exe" {
		login.Dir = "/"
	}
	login.Stdin = os.Stdin
	login.Stdout = os.Stdout
	login.Stderr = os.Stderr
	if err := login.Run(); err != nil {
		return integration.AuthenticationResult{}, fmt.Errorf("%s login failed: %w", executable, err)
	}
	return integration.AuthenticationResult{Changed: true}, nil
}

func authenticationRequired(req integration.AuthenticationRequest) bool {
	switch req.Operation {
	case "create", "fork":
		return true
	case "startup":
		for _, status := range req.SourceStatuses {
			if status.Name == "sbx" && status.Status == "error" && auth.IsRequired(status.Detail) {
				return true
			}
		}
	case "cleanup":
		for _, target := range req.CleanupTargets {
			if target.Source == "sbx" {
				return true
			}
		}
	}
	return false
}

var _ integration.InteractiveAuthenticator = Source{}
