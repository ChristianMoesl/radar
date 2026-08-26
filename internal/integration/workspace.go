package integration

import "context"

type Workspace struct {
	Name        string `json:"name,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Base        string `json:"base,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Path        string `json:"path"`
	SessionName string `json:"session_name"`
	SandboxName string `json:"sandbox_name,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

type WorkspaceBranchMode string

const (
	WorkspaceBranchExisting WorkspaceBranchMode = "existing"
	WorkspaceBranchNew      WorkspaceBranchMode = "new"
)

type WorkspaceProvider interface {
	Source
	Current(ctx context.Context, cwd string) (Workspace, bool, error)
}
