package protocol

// ManagedOperation is current-daemon evidence, not a durable job or permission
// to replay an action. Reported changes were executed by a client, not daemon.
type ManagedOperation struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	TaskID           int    `json:"task_id,omitempty"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	Status           string `json:"status"`
	CompletedTargets int    `json:"completed_targets,omitempty"`
	TotalTargets     int    `json:"total_targets,omitempty"`
}

// ResourceMutation identifies a client-reported change. It invalidates cached
// observations only; neither the target nor the report authorizes execution.
type ResourceMutation struct {
	TaskID      int    `json:"task_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}
