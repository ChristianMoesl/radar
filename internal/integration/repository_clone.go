package integration

import "context"

// RepositoryCloneProvider provisions source checkouts, never task worktrees.
// Confirmation belongs to the trusted caller; a plan ID only detects drift.
type RepositoryCloneProvider interface {
	Integration
	PreviewRepositoryClone(context.Context, RepositoryCloneRequest) (RepositoryClonePlan, error)
	ApplyRepositoryClone(context.Context, RepositoryCloneRequest, string) (RepositoryCloneResult, error)
}

type RepositoryCloneRequest struct {
	Repository  string `json:"repository"`
	Destination string `json:"destination"`
	Workspace   string `json:"-"`
}

type RepositoryClonePlan struct {
	PlanID               string `json:"plan_id"`
	WorkspaceID          string `json:"workspace_id"`
	Remote               string `json:"remote"`
	Identity             string `json:"identity"`
	RepositoryDir        string `json:"repository_dir"`
	RootIdentity         string `json:"root_identity"`
	Destination          string `json:"destination"`
	Repository           string `json:"repository"`
	Status               string `json:"status"`
	RequiresConfirmation bool   `json:"requires_confirmation"`
}

type RepositoryCloneResult struct {
	OK          bool   `json:"ok"`
	Status      string `json:"status"`
	Repository  string `json:"repository"`
	Remote      string `json:"remote"`
	Error       string `json:"error,omitempty"`
	PartialPath string `json:"partial_path,omitempty"`
}
