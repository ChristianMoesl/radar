package protocol

import "strings"

const DiagnosticsVersion = 2
const DiagnosticChildLimit = 16

// DiagnosticText bounds non-identity text in allowlisted diagnostic projections.
func DiagnosticText(value string) string {
	const max = 512
	if len(value) > max {
		return strings.ToValidUTF8(value[:max], "�") + "…"
	}
	return strings.ToValidUTF8(value, "�")
}

// DiagnosticsRequest has no execution or arbitrary filesystem parameters.
// Workspace is a lexical cache filter; the host facade resolves registered scope.
type DiagnosticsRequest struct {
	Section   string `json:"section"`
	ParentID  string `json:"parent_id,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	ID        string `json:"id,omitempty"`
	State     string `json:"state,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

type DiagnosticRef struct {
	ProviderID          string             `json:"provider_id"`
	RecordID            string             `json:"record_id,omitempty"`
	TaskID              int                `json:"task_id,omitempty"`
	LifecycleInput      bool               `json:"lifecycle_input"`
	ID                  string             `json:"id"`
	Source              string             `json:"source"`
	Kind                string             `json:"kind"`
	Role                SourceRefRole      `json:"role"`
	Lifecycle           SourceRefLifecycle `json:"lifecycle"`
	Authority           SourceRefAuthority `json:"authority"`
	Active              bool               `json:"active"`
	ObservedAt          string             `json:"observed_at,omitempty"`
	Path                string             `json:"path,omitempty"`
	WorkspaceID         string             `json:"workspace_id,omitempty"`
	AgentID             string             `json:"agent_id,omitempty"`
	Signal              string             `json:"signal,omitempty"`
	Activity            string             `json:"activity"`
	Authored            bool               `json:"authored"`
	Review              bool               `json:"review"`
	Muted               bool               `json:"muted"`
	BindingCount        int                `json:"binding_count"`
	BindingProblem      bool               `json:"binding_problem"`
	CleanupProblemCount int                `json:"cleanup_problem_count"`
}

type DiagnosticTask struct {
	AuthoredIdentityCount   int             `json:"authored_identity_count"`
	LinkingComponentRecords int             `json:"linking_component_records"`
	LinkingBlocked          bool            `json:"linking_blocked"`
	DecisionEvidence        string          `json:"decision_evidence"`
	RecordID                string          `json:"record_id"`
	TaskID                  int             `json:"task_id"`
	State                   string          `json:"state"`
	DoneAt                  string          `json:"done_at,omitempty"`
	UpdatedAt               string          `json:"updated_at,omitempty"`
	Activity                string          `json:"activity"`
	Review                  bool            `json:"review"`
	Acknowledged            bool            `json:"acknowledged"`
	DecisionReasonAvailable bool            `json:"decision_reason_available"`
	SourceCount             int             `json:"source_count"`
	Sources                 []DiagnosticRef `json:"sources"`
	Truncated               bool            `json:"truncated"`
}

type DiagnosticWorkspace struct {
	Feedback               bool                  `json:"feedback"`
	FeedbackOrigin         string                `json:"feedback_origin,omitempty"`
	Mounts                 []DiagnosticMount     `json:"registered_mounts"`
	Ports                  []PortForward         `json:"registered_ports"`
	ID                     string                `json:"id"`
	Path                   string                `json:"path"`
	TaskIDs                []int                 `json:"task_ids"`
	Members                []DiagnosticMember    `json:"members"`
	NotePath               string                `json:"note_path,omitempty"`
	SandboxName            string                `json:"sandbox_name,omitempty"`
	SessionName            string                `json:"session_name,omitempty"`
	SharedDirectory        string                `json:"shared_directory,omitempty"`
	Registered             bool                  `json:"registered"`
	RuntimeEvidence        string                `json:"runtime_evidence"`
	AgentOwners            []string              `json:"agent_owners"`
	AgentOwnershipEvidence string                `json:"agent_ownership_evidence"`
	Retention              []DiagnosticRetention `json:"retention"`
	ExecutionEligibility   string                `json:"execution_eligibility"`
	Truncated              bool                  `json:"truncated"`
}

type DiagnosticMount struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

type DiagnosticMember struct {
	Path       string `json:"path"`
	Repository string `json:"repository"`
}

type DiagnosticRetention struct {
	TaskID   int    `json:"task_id"`
	State    string `json:"state"`
	DoneAt   string `json:"done_at,omitempty"`
	ExpiryAt string `json:"expiry_at,omitempty"`
	Activity string `json:"activity"`
	Review   bool   `json:"review"`
}

type DiagnosticBinding struct {
	ID          string `json:"id"`
	SourceRefID string `json:"source_ref_id"`
	RecordID    string `json:"record_id,omitempty"`
	TaskID      int    `json:"task_id,omitempty"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	TargetID    string `json:"target_id"`
	Key         string `json:"key,omitempty"`
	WorkItem    bool   `json:"work_item"`
	Occurrences int    `json:"occurrences"`
}

type DiagnosticItem struct {
	Ref       *DiagnosticRef       `json:"source_ref,omitempty"`
	Binding   *DiagnosticBinding   `json:"binding,omitempty"`
	ID        string               `json:"id"`
	Task      *DiagnosticTask      `json:"task,omitempty"`
	Workspace *DiagnosticWorkspace `json:"workspace,omitempty"`
}

type DiagnosticsPage struct {
	Schema                 int              `json:"diagnostics_schema"`
	Section                string           `json:"section"`
	Evidence               string           `json:"evidence"`
	ObservedAt             string           `json:"observed_at"`
	Revision               int64            `json:"revision"`
	Snapshot               string           `json:"snapshot"`
	Total                  int              `json:"total"`
	Items                  []DiagnosticItem `json:"items"`
	NextCursor             string           `json:"next_cursor,omitempty"`
	Complete               bool             `json:"pagination_complete"`
	ResourceRefreshPending bool             `json:"resource_refresh_pending"`
	Limitations            []string         `json:"limitations"`
}
