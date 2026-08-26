package sbx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/sbx/auth"
)

func (Source) EnsureAuthentication(ctx context.Context, req integration.AuthenticationRequest) (integration.AuthenticationResult, error) {
	if !authenticationRequired(req) {
		return integration.AuthenticationResult{}, nil
	}
	if _, err := exec.LookPath("sbx"); err != nil {
		return integration.AuthenticationResult{}, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := exec.CommandContext(checkCtx, "sbx", "ls", "--json")
	output, err := check.CombinedOutput()
	if err == nil || !auth.IsRequired(string(output)+"\n"+err.Error()) {
		return integration.AuthenticationResult{}, nil
	}
	fmt.Fprintln(os.Stderr, "radar: sbx is not signed in; starting sbx login")
	login := exec.CommandContext(ctx, "sbx", "login")
	login.Stdin = os.Stdin
	login.Stdout = os.Stdout
	login.Stderr = os.Stderr
	if err := login.Run(); err != nil {
		return integration.AuthenticationResult{}, fmt.Errorf("sbx login failed: %w", err)
	}
	return integration.AuthenticationResult{Changed: true}, nil
}

func authenticationRequired(req integration.AuthenticationRequest) bool {
	switch req.Operation {
	case "create", "fork":
		return true
	case "cleanup":
		for _, target := range req.CleanupTargets {
			if target.Source == "sbx" {
				return true
			}
		}
	default:
		for _, status := range req.SourceStatuses {
			if status.Name == "sbx" && status.Status == "error" && auth.IsRequired(status.Detail) {
				return true
			}
		}
	}
	return false
}

var _ integration.InteractiveAuthenticator = Source{}
