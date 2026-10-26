package integration

import (
	"context"
	"radar/internal/protocol"
)

// ResourceOpenProvider advertises canonical browser destinations from existing
// task facts. It cannot supply executables or arbitrary action arguments.
type ResourceOpenProvider interface {
	Integration
	OpenResources(context.Context, protocol.SourceRef) ([]OpenResource, error)
}

type OpenResource struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	SourceRefID string `json:"source_ref_id"`
	Label       string `json:"label"`
	URL         string `json:"url"`
}

type ResourceOpenRequest struct {
	ResourceIDs []string `json:"resource_ids"`
}

type ResourceOpenPlan struct {
	PlanID               string         `json:"plan_id"`
	WorkspaceID          string         `json:"workspace_id"`
	TaskID               int            `json:"task_id"`
	Resources            []OpenResource `json:"resources"`
	RequiresConfirmation bool           `json:"requires_confirmation"`
}

type ResourceOpenResult struct {
	OK        bool                  `json:"ok"`
	Cancelled bool                  `json:"cancelled,omitempty"`
	Results   []ResourceOpenOutcome `json:"results"`
}

type ResourceOpenOutcome struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
