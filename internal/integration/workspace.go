package integration

import (
	"context"

	"radar/internal/tmuxlayout"
)

type Workspace struct {
	Name        string `json:"name,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Base        string `json:"base,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Path        string `json:"path"`
	SessionName string `json:"session_name"`
	SandboxName string `json:"sandbox_name,omitempty"`
}

type WorkspaceBranchMode string

const (
	WorkspaceBranchExisting WorkspaceBranchMode = "existing"
	WorkspaceBranchNew      WorkspaceBranchMode = "new"
)

type CreateWorkspaceRequest struct {
	Repo                    string
	BranchMode              WorkspaceBranchMode
	Name                    string
	Branch                  string
	Base                    string
	Path                    string
	SessionName             string
	WorkspaceRoot           string
	Model                   string
	Thinking                string
	Sandbox                 bool
	SandboxKitName          string
	SandboxKitPath          string
	AdditionalSandboxMounts []string
	Tmux                    tmuxlayout.Config
	Switch                  bool
	ForkPiSession           string
}

type WorkspaceProvider interface {
	Source
	Current(ctx context.Context, cwd string) (Workspace, bool, error)
	Create(ctx context.Context, req CreateWorkspaceRequest) (Workspace, error)
}
