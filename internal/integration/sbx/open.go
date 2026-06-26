package sbx

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"radar/internal/protocol"
	"radar/internal/workspace"
)

const OpenShellAction = "sbx_shell"

type OpenShellOptions struct {
	SessionTarget string
	SwitchClient  bool
}

type OpenShellResult struct {
	SessionName    string
	CreatedSession bool
}

func IsSandboxRef(ref protocol.SourceRef) bool {
	return ref.Source == "sbx" && ref.Kind == "sandbox"
}

func SandboxName(ref protocol.SourceRef) string {
	if !IsSandboxRef(ref) {
		return ""
	}
	if name := strings.TrimSpace(ref.Metadata["name"]); name != "" {
		return name
	}
	if name := strings.TrimSpace(ref.Title); name != "" {
		return name
	}
	return strings.TrimPrefix(ref.ID, "sbx:sandbox:")
}

func SandboxSessionName(ref protocol.SourceRef) string {
	path := strings.TrimSpace(ref.Path)
	if path != "" {
		return workspace.SessionName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	return workspace.WorktreeName(SandboxName(ref))
}

func OpenShell(ctx context.Context, runner workspace.Runner, ref protocol.SourceRef, options OpenShellOptions) (OpenShellResult, error) {
	name := SandboxName(ref)
	if name == "" {
		return OpenShellResult{}, fmt.Errorf("sbx sandbox name is required")
	}
	for _, dependency := range []string{"tmux", "sbx"} {
		if err := runner.LookPath(dependency); err != nil {
			return OpenShellResult{}, fmt.Errorf("open sbx sandbox requires %q: %w", dependency, err)
		}
	}

	sessionName := strings.TrimSpace(options.SessionTarget)
	if sessionName == "" {
		sessionName = SandboxSessionName(ref)
	}
	if sessionName == "" {
		return OpenShellResult{}, fmt.Errorf("tmux session name is required")
	}

	command := "sbx run --name " + shellQuote(name)
	path := strings.TrimSpace(ref.Path)
	result := OpenShellResult{SessionName: sessionName}
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", sessionName); err != nil {
		args := []string{"new-session", "-d", "-s", sessionName, "-n", "sbx"}
		if path != "" {
			args = append(args, "-c", path)
		}
		args = append(args, command)
		if _, err := runner.Run(ctx, "", "tmux", args...); err != nil {
			return OpenShellResult{}, err
		}
		result.CreatedSession = true
	} else {
		args := []string{"new-window", "-t", sessionName + ":", "-n", "sbx"}
		if path != "" {
			args = append(args, "-c", path)
		}
		args = append(args, command)
		if _, err := runner.Run(ctx, "", "tmux", args...); err != nil {
			return OpenShellResult{}, err
		}
	}

	if options.SwitchClient {
		if _, err := runner.Run(ctx, "", "tmux", "switch-client", "-t", sessionName); err != nil {
			return OpenShellResult{}, err
		}
	}
	return result, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
