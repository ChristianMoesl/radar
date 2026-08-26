package integration

import (
	"context"
	"log/slog"
)

type WorkspaceManager interface {
	Integration
	WorkspaceCatalog
	ManagedWorkspaceLifecycle
	WorkspaceReconciler
}

type WorkspaceCatalog interface {
	DefaultRoot() (string, error)
	ExpandPath(path string) string
	RegisteredWorkspace(currentDirectory string) (WorkspaceRegistration, bool, error)
	DiscoverRepositories(ctx context.Context, currentDirectory string) ([]string, error)
	RepositoryBranches(ctx context.Context, repository string) ([]string, string, error)
}

type ManagedWorkspaceLifecycle interface {
	SessionName(repositoryName, workspaceName string) string
	OpenWorkspace(ctx context.Context, path string, switchClient bool) (Workspace, error)
	CreateWorkspace(ctx context.Context, req ManagedWorkspaceRequest) (Workspace, error)
	CreateSession(ctx context.Context, req CreateSessionRequest) (Workspace, error)
}

type WorkspaceReconciler interface {
	PreviewReconcile(ctx context.Context, req WorkspaceReconcileRequest) (WorkspaceReconcilePlan, error)
	ApplyReconcile(ctx context.Context, logger *slog.Logger, req WorkspaceReconcileRequest) (WorkspaceReconcileResult, error)
	ReconcileErrorDetails(err error) (WorkspaceReconcileError, bool)
	InspectWorkspace(ctx context.Context, currentDirectory, workspaceRoot string) (any, error)
	InspectRepositoryRefs(ctx context.Context, repository string) (any, error)
}

type WorkspaceRegistration struct {
	ID      string
	Name    string
	Path    string
	Members []WorkspaceMember
}

type WorkspaceMember struct {
	Repository string
	Path       string
	Branch     string
}

type ManagedWorkspaceRequest struct {
	Repo           string
	BranchMode     WorkspaceBranchMode
	Name           string
	Branch         string
	Base           string
	Path           string
	SessionName    string
	WorkspaceRoot  string
	Switch         bool
	ForkPiSession  string
	TaskLinkingKey string
	NotePath       string
}

type CreateSessionRequest struct {
	Path              string
	SessionName       string
	TaskLinkingKey    string
	RuntimeResourceID string
	Switch            bool
}

type WorkspaceReconcileRequest struct {
	Workspace               string
	WorkspaceRoot           string
	AdditionalSandboxMounts []string
	ExpectedPlanID          string
	ExpectedPlanChangeCount *int
	Revision                string                      `json:"revision"`
	Desired                 DesiredWorkspaceDescription `json:"desired"`
}

type DesiredWorkspaceDescription struct {
	Worktrees []DesiredWorkspaceWorktree `json:"worktrees"`
	Sandbox   *DesiredWorkspaceSandbox   `json:"sandbox"`
}

type DesiredWorkspaceWorktree struct {
	Repository string              `json:"repository"`
	BranchMode WorkspaceBranchMode `json:"branch_mode"`
	Name       string              `json:"name,omitempty"`
	Branch     string              `json:"branch,omitempty"`
	Base       string              `json:"base,omitempty"`
}

type DesiredWorkspaceSandbox struct {
	AdditionalMounts []DesiredSandboxMount `json:"additional_mounts"`
	Ports            []SandboxPort         `json:"ports"`
}

type DesiredSandboxMount struct {
	Path     string `json:"path"`
	ReadOnly *bool  `json:"read_only,omitempty"`
}

type SandboxPort struct {
	HostPort    int `json:"host_port"`
	SandboxPort int `json:"sandbox_port"`
}

type WorkspaceChange struct {
	Action      string `json:"action"`
	Resource    string `json:"resource"`
	Summary     string `json:"summary"`
	Repository  string `json:"repository,omitempty"`
	Path        string `json:"path,omitempty"`
	Branch      string `json:"branch,omitempty"`
	HostPort    int    `json:"host_port,omitempty"`
	SandboxPort int    `json:"sandbox_port,omitempty"`
	ReadOnly    *bool  `json:"read_only,omitempty"`
}

type WorkspaceReconcilePlan struct {
	WorkspaceID         string            `json:"workspace_id"`
	WorkspaceName       string            `json:"workspace_name"`
	Revision            string            `json:"revision"`
	NextRevision        string            `json:"next_revision"`
	PlanID              string            `json:"plan_id"`
	AutoConfirm         bool              `json:"auto_confirm,omitempty"`
	EffectiveMountCount int               `json:"effective_sandbox_mount_count,omitempty"`
	Changes             []WorkspaceChange `json:"changes"`
	Warnings            []string          `json:"warnings,omitempty"`
}

type WorkspaceReconcileResult struct {
	OK                bool                    `json:"ok"`
	WorkspaceID       string                  `json:"workspace_id"`
	Revision          string                  `json:"revision,omitempty"`
	WorktreesAdded    int                     `json:"worktrees_added"`
	WorktreesRemoved  int                     `json:"worktrees_removed"`
	SandboxReconciled bool                    `json:"sandbox_reconciled"`
	MountsAdded       int                     `json:"mounts_added"`
	MountsRemoved     int                     `json:"mounts_removed"`
	PortsPublished    int                     `json:"ports_published"`
	PortsUnpublished  int                     `json:"ports_unpublished"`
	Retryable         bool                    `json:"retryable,omitempty"`
	ReconfirmRequired bool                    `json:"reconfirm_required,omitempty"`
	Reason            string                  `json:"reason,omitempty"`
	Plan              *WorkspaceReconcilePlan `json:"plan,omitempty"`
	Warning           string                  `json:"warning,omitempty"`
	Error             string                  `json:"error,omitempty"`
}

type WorkspaceReconcileError struct {
	Reason      string `json:"reason"`
	Message     string `json:"error"`
	Repository  string `json:"repository,omitempty"`
	Path        string `json:"path,omitempty"`
	Branch      string `json:"branch,omitempty"`
	ChangeCount int    `json:"change_count,omitempty"`
}
