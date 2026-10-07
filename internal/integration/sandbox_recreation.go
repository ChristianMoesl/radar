package integration

import (
	"context"
	"log/slog"
)

// WorkspaceSandboxRecreator is a CLI operation, not a change to desired state.
type WorkspaceSandboxRecreator interface {
	PreviewRecreateSandboxes(context.Context, SandboxRecreateRequest) (SandboxRecreatePlan, error)
	RecreateSandboxes(context.Context, *slog.Logger, SandboxRecreateRequest) (SandboxRecreateResult, error)
}

type SandboxRecreateRequest struct {
	WorkspaceRoot  string
	Workspace      string
	ExpectedPlanID string
}

type SandboxRecreateTarget struct {
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceName  string `json:"workspace_name"`
	WorkspacePath  string `json:"workspace_path"`
	SandboxName    string `json:"sandbox_name"`
	SandboxID      string `json:"sandbox_id,omitempty"`
	PreviousStatus string `json:"previous_status,omitempty"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
}

type SandboxRecreatePlan struct {
	PlanID  string                  `json:"plan_id"`
	Targets []SandboxRecreateTarget `json:"targets"`
}

type SandboxRecreateResult struct {
	OK        bool                    `json:"ok"`
	Cancelled bool                    `json:"cancelled,omitempty"`
	Recreated int                     `json:"recreated"`
	Failed    int                     `json:"failed"`
	Skipped   int                     `json:"skipped"`
	Targets   []SandboxRecreateTarget `json:"targets"`
}
