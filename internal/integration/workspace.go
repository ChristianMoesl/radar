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
}

type CreateWorkspaceRequest struct {
	Repo            string
	Name            string
	Branch          string
	Base            string
	Path            string
	SessionName     string
	WorkspaceRoot   string
	Model           string
	Thinking        string
	Sandbox         bool
	SandboxTemplate string
	Switch          bool
	ForkPiSession   string
}

type DeleteWorkspaceRequest struct {
	Path        string
	SessionName string
	Force       bool
	Sandbox     bool
}

type WorkspaceProvider interface {
	Source
	Current(ctx context.Context, cwd string) (Workspace, bool, error)
	Create(ctx context.Context, req CreateWorkspaceRequest) (Workspace, error)
	DeleteWorkspace(ctx context.Context, req DeleteWorkspaceRequest) (Workspace, error)
}
