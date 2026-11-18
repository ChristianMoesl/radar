package protocol

// ManagedOperation is current-daemon evidence, not a durable job or permission
// to replay an action. Reported changes were executed by a client, not daemon.
type ManagedOperation struct {
	// Captured only at the native cleanup handler return, never from reports.
	CleanupTargets            []OperationCleanupTarget `json:"cleanup_targets,omitempty"`
	CleanupTargetsCaptured    bool                     `json:"cleanup_targets_captured,omitempty"`
	CleanupTargetsUnavailable bool                     `json:"cleanup_targets_unavailable,omitempty"`
	CleanupTargetsTruncated   bool                     `json:"cleanup_targets_truncated,omitempty"`
	// Bounded native completed cleanup intervals only; no paths or phase payloads.
	PhaseTimings            []OperationPhaseTiming `json:"phase_timings,omitempty"`
	PhaseTimingsUnavailable bool                   `json:"phase_timings_unavailable,omitempty"`
	// Native daemon-handler boundaries only. ElapsedMilliseconds uses the
	// monotonic clock, not subtraction of wall timestamps. Client reports have
	// no timing; a running operation has only StartedAt. Never persisted.
	StartedAt           string `json:"started_at,omitempty"`
	FinishedAt          string `json:"finished_at,omitempty"`
	ElapsedMilliseconds int64  `json:"elapsed_milliseconds,omitempty"`
	ID                  string `json:"id"`
	Kind                string `json:"kind"`
	TaskID              int    `json:"task_id,omitempty"`
	WorkspaceID         string `json:"workspace_id,omitempty"`
	Status              string `json:"status"`
	CompletedTargets    int    `json:"completed_targets,omitempty"`
	TotalTargets        int    `json:"total_targets,omitempty"`
}

// OperationCleanupTarget identifies an ordinal in the handler's original target
// bundle. IDs are operation-local opaque observations, not preview/apply tokens.
// Return evidence never proves absence, reconciliation or a resource outcome.
type OperationCleanupTarget struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Outcome  string `json:"outcome"`
}

func ManagedCleanupTargetKind(provider, kind string) bool {
	return provider == "git" && kind == "worktree" ||
		provider == "sbx" && kind == "sandbox" ||
		provider == "tmux" && kind == "session" ||
		provider == "workspace" && (kind == "workspace" || kind == "missing_member")
}

// ResourceMutation identifies a client-reported change. It invalidates cached
// observations only; neither the target nor the report authorizes execution.
type ResourceMutation struct {
	TaskID      int    `json:"task_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// OperationPhaseTiming sums individually millisecond-truncated completed
// intervals. Nested phases overlap; sums are not handler duration or progress.
type OperationPhaseTiming struct {
	Phase               string `json:"phase"`
	CompletedIntervals  int64  `json:"completed_intervals"`
	ElapsedMilliseconds int64  `json:"elapsed_milliseconds"`
}

const MaxOperationPhaseIntervals int64 = 1_000_000
const MaxOperationPhaseMilliseconds int64 = 9223372036854

func ManagedCleanupPhase(phase string) bool {
	switch phase {
	case "cleanup_execution", "cleanup_bundle_preflight", "cleanup_resource", "cleanup_worktree", "cleanup_runtime", "cleanup_anchor", "cleanup_protected_scan", "cleanup_member_scan", "managed_worktree_removal", "worktree_removal", "branch_publication_verification":
		return true
	}
	return false
}
