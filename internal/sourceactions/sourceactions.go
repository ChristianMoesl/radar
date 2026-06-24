package sourceactions

import (
	"context"
	"fmt"
	"os"

	"radar/internal/protocol"
	"radar/internal/sbx"
	"radar/internal/taskrefs"
	"radar/internal/workspace"
)

type Action struct {
	PreferredKey string
	Source       string
	Label        string
	Detail       string
	Action       string
	Ref          protocol.SourceRef
}

type Result struct {
	Message string
	Refresh bool
	Quit    bool
}

func SourceRefActions(ref protocol.SourceRef, label string) []Action {
	if sbx.IsSandboxRef(ref) {
		return []Action{{
			PreferredKey: "s",
			Source:       "Docker sbx",
			Label:        label,
			Detail:       "sbx run --name " + sbx.SandboxName(ref),
			Action:       sbx.OpenShellAction,
			Ref:          ref,
		}}
	}
	return nil
}

func Open(ctx context.Context, task protocol.Task, action string, ref protocol.SourceRef) (Result, error) {
	switchClient := os.Getenv("TMUX") != ""
	switch action {
	case sbx.OpenShellAction:
		result, err := sbx.OpenShell(ctx, workspace.ExecRunner{}, ref, sbx.OpenShellOptions{
			SessionTarget: taskrefs.SessionTarget(task),
			SwitchClient:  switchClient,
		})
		if err != nil {
			return Result{}, err
		}
		message := "Opened sbx sandbox in " + result.SessionName
		if result.CreatedSession {
			message = "Created " + result.SessionName + " and opened sbx sandbox"
		}
		return Result{Message: message, Refresh: true, Quit: switchClient}, nil
	default:
		return Result{}, fmt.Errorf("unknown source action: %s", action)
	}
}
