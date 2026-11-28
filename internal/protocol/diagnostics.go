package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const DiagnosticsVersion = 43
const DiagnosticChildLimit = 16

// DiagnosticText bounds non-identity text in allowlisted diagnostic projections.
func DiagnosticText(value string) string {
	const max = 512
	if len(value) > max {
		return strings.ToValidUTF8(value[:max], "�") + "…"
	}
	return strings.ToValidUTF8(value, "�")
}

// DiagnosticsRequest has no mutation or arbitrary filesystem parameters.
// Workspace is host-resolved scope: lexical filtering for cached reads,
// exact registered identity for probes.
// repository_policy, runtime_probe, agent_probe, retirement_preview, recreation_preview and cleanup_preview require explicit
// diagnostics-probe RPC. CleanupMode/WorkspaceID are cleanup-preview selectors only.
// No request can apply a preview. The selector-free overview is the fixed
// daemon-process configuration projection; no workspace/path override is accepted.
type DiagnosticsRequest struct {
	CleanupMode string `json:"cleanup_mode,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Section     string `json:"section"`
	ParentID    string `json:"parent_id,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	ID          string `json:"id,omitempty"`
	State       string `json:"state,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
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
	GCRetention             DiagnosticGCRetention `json:"gc_retention"`
	AuthoredIdentityCount   int                   `json:"authored_identity_count"`
	LinkingComponentRecords int                   `json:"linking_component_records"`
	LinkingBlocked          bool                  `json:"linking_blocked"`
	Lifecycle               DiagnosticLifecycle   `json:"lifecycle"`
	RecordID                string                `json:"record_id"`
	TaskID                  int                   `json:"task_id"`
	State                   string                `json:"state"`
	DoneAt                  string                `json:"done_at,omitempty"`
	UpdatedAt               string                `json:"updated_at,omitempty"`
	Activity                string                `json:"activity"`
	Review                  bool                  `json:"review"`
	Acknowledged            bool                  `json:"acknowledged"`
	DecisionReasonAvailable bool                  `json:"decision_reason_available"`
	SourceCount             int                   `json:"source_count"`
	Sources                 []DiagnosticRef       `json:"sources"`
	Truncated               bool                  `json:"truncated"`
}

type DiagnosticWorkspace struct {
	MemberCount            int                   `json:"member_count"`
	MountCount             int                   `json:"mount_count"`
	PortCount              int                   `json:"port_count"`
	TaskCount              int                   `json:"task_count"`
	AgentOwnerCount        *int                  `json:"agent_owner_count,omitempty"`
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

// DiagnosticWorkspacePolicy is one registered recipe, not current effective
// policy, historical provenance, live runtime state or execution eligibility.
// Raw recipe values must never enter this projection.
type DiagnosticWorkspacePolicy struct {
	ID                string                   `json:"id"`
	Origin            string                   `json:"origin"`
	SandboxRegistered bool                     `json:"sandbox_registered"`
	Sandbox           *DiagnosticSandboxRecipe `json:"sandbox,omitempty"`
}

type DiagnosticSandboxRecipe struct {
	KitConfigured          bool `json:"kit_configured"`
	KitPathConfigured      bool `json:"kit_path_configured"`
	EnvFileConfigured      bool `json:"env_file_configured"`
	ReadyCommandConfigured bool `json:"ready_command_configured"`
	ManagedMountCount      int  `json:"managed_mount_count"`
	AdditionalMountCount   int  `json:"additional_mount_count"`
	PortCount              int  `json:"port_count"`
}

// DiagnosticRepositoryPolicy observes only the current fixed .radar.yaml of
// one exact registered member repository. It is not operation-effective policy.
type DiagnosticRepositoryPolicy struct {
	ID          string              `json:"id"`
	WorkspaceID string              `json:"workspace_id"`
	Origin      string              `json:"origin"`
	Available   bool                `json:"available"`
	Evidence    string              `json:"evidence"`
	Reason      string              `json:"reason,omitempty"`
	Settings    []DiagnosticSetting `json:"settings"`
}

// DiagnosticWorkspaceResourceID hashes full identities before display clipping.
// Preserve this shape: existing workspace_resources member IDs are selectors.
func DiagnosticWorkspaceResourceID(workspace string, view DiagnosticWorkspaceResource) string {
	identity, _ := json.Marshal(struct {
		Workspace, Kind, Record, Agent string
		Member                         *DiagnosticMember
		Mount                          *DiagnosticMount
		Port                           *PortForward
	}{workspace, view.Kind, view.RecordID, view.AgentID, view.Member, view.Mount, view.Port})
	sum := sha256.Sum256(identity)
	return "workspace-resource:" + hex.EncodeToString(sum[:])
}

type DiagnosticMount struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

type DiagnosticMember struct {
	Path       string `json:"path"`
	Repository string `json:"repository"`
}

// DiagnosticWorkspaceResource pages complete allowlisted registration/ownership
// relationships. Presence is not runtime observation or cleanup authorization.
type DiagnosticWorkspaceResource struct {
	ID          string               `json:"id"`
	WorkspaceID string               `json:"workspace_id"`
	Kind        string               `json:"kind"`
	Evidence    string               `json:"evidence"`
	Occurrences int                  `json:"occurrences"`
	Member      *DiagnosticMember    `json:"member,omitempty"`
	Mount       *DiagnosticMount     `json:"mount,omitempty"`
	Port        *PortForward         `json:"port,omitempty"`
	RecordID    string               `json:"record_id,omitempty"`
	Task        *DiagnosticRetention `json:"task,omitempty"`
	AgentID     string               `json:"agent_id,omitempty"`
}

type DiagnosticRetention struct {
	GCRetention DiagnosticGCRetention `json:"gc_retention"`
	TaskID      int                   `json:"task_id"`
	State       string                `json:"state"`
	DoneAt      string                `json:"done_at,omitempty"`
	ExpiryAt    string                `json:"expiry_at,omitempty"`
	Activity    string                `json:"activity"`
	Review      bool                  `json:"review"`
}

type DiagnosticGCRetention struct {
	Decision string `json:"decision"`
	ReadyAt  string `json:"ready_at,omitempty"`
	Evidence string `json:"evidence"`
}

// DiagnosticLifecycle is a prospective evaluation on cached inputs, not proof
// of the provenance of recorded state or permission to apply a transition.
type DiagnosticLifecycle struct {
	Evidence               string `json:"evidence"`
	Decision               string `json:"decision"`
	SourceState            string `json:"source_state"`
	WorkItemInputs         int    `json:"work_item_inputs"`
	DoneWorkItemInputs     int    `json:"done_work_item_inputs"`
	FallbackWorkItemInputs int    `json:"fallback_work_item_inputs"`
	AuthoritativeRefs      int    `json:"authoritative_refs"`
	ActivityHolds          int    `json:"activity_holds"`
	ReviewHolds            int    `json:"review_holds"`
	RecordedReviewPending  bool   `json:"recorded_review_pending"`
	NextReviewPending      bool   `json:"next_review_pending"`
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

type DiagnosticAgent struct {
	ID                  string                      `json:"id"`
	Registered          bool                        `json:"registered"`
	Desired             bool                        `json:"desired"`
	Workspace           AgentWorkspaceBinding       `json:"workspace"`
	OwnershipHold       string                      `json:"ownership_hold"`
	RunnerEvidence      string                      `json:"runner_evidence"`
	LeaseEvidence       string                      `json:"lease_evidence"`
	SnapshotEvidence    string                      `json:"snapshot_evidence"`
	SnapshotSavedAt     string                      `json:"snapshot_saved_at,omitempty"`
	Snapshot            *DiagnosticAgentSnapshot    `json:"snapshot,omitempty"`
	CollectionEvidence  string                      `json:"collection_evidence"`
	Collection          *DiagnosticAgentCollection  `json:"collection,omitempty"`
	SchedulesEvidence   string                      `json:"schedules_evidence"`
	ScheduleCount       *int                        `json:"schedule_count,omitempty"`
	Schedules           []DiagnosticAgentSchedule   `json:"schedules"`
	Truncated           bool                        `json:"truncated"`
	SupervisionEvidence string                      `json:"supervision_evidence"`
	Supervision         *DiagnosticAgentSupervision `json:"supervision,omitempty"`
}

// DiagnosticAgentSupervision is an ID-keyed current-daemon memory projection,
// not a fresh runner observation, registration-lifetime attestation or history.
type DiagnosticAgentSupervision struct {
	RetryRecordEvidence     string                `json:"retry_record_evidence"`
	Retry                   *DiagnosticAgentRetry `json:"retry,omitempty"`
	ScheduleDeliveryProblem bool                  `json:"schedule_delivery_problem"`
}

type DiagnosticAgentRetry struct {
	FailedStartAttempts int    `json:"failed_start_attempts"`
	RetryNotBefore      string `json:"retry_not_before"`
	StartProblem        bool   `json:"start_problem"`
}

type DiagnosticAgentSnapshot struct {
	DefinitionVersion int  `json:"definition_version"`
	Revision          int  `json:"revision"`
	Pending           int  `json:"pending"`
	Attention         bool `json:"attention"`
	Problem           bool `json:"problem"`
}

// DiagnosticAgentProbe separates a matching live hello from a valid snapshot.
// Neither establishes work execution, workspace runtime health or leases.
type DiagnosticAgentProbe struct {
	ID               string                   `json:"id"`
	Registered       bool                     `json:"registered"`
	Desired          bool                     `json:"desired"`
	OwnershipHold    string                   `json:"ownership_hold"`
	RunnerEvidence   string                   `json:"runner_evidence"`
	SnapshotEvidence string                   `json:"snapshot_evidence"`
	Snapshot         *DiagnosticAgentSnapshot `json:"snapshot,omitempty"`
	Problem          string                   `json:"problem"`
	LeaseEvidence    string                   `json:"lease_evidence"`
}

type DiagnosticAgentCollection struct {
	Source          string `json:"source"`
	Pending         int    `json:"pending"`
	DeliveryProblem bool   `json:"delivery_problem"`
}

type DiagnosticAgentSchedule struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	Enabled         bool                      `json:"enabled"`
	Time            string                    `json:"time"`
	Timezone        string                    `json:"timezone"`
	Weekdays        []string                  `json:"weekdays"`
	NextDue         string                    `json:"next_due,omitempty"`
	PendingAt       string                    `json:"pending_at,omitempty"`
	LastMissedAt    string                    `json:"last_missed_at,omitempty"`
	LastAdmission   *DiagnosticAgentAdmission `json:"last_admission,omitempty"`
	DeliveryProblem bool                      `json:"delivery_problem"`
}

type DiagnosticAgentAdmission struct {
	ScheduledAt string `json:"scheduled_at"`
	AcceptedAt  string `json:"accepted_at"`
	TaskID      int    `json:"task_id"`
	CatchUp     bool   `json:"catch_up"`
}

type DiagnosticAgentScheduleEntry struct {
	ID       string                  `json:"id"`
	AgentID  string                  `json:"agent_id"`
	Schedule DiagnosticAgentSchedule `json:"schedule"`
}

// DiagnosticGCPlan describes production candidate/skip selection from cached
// retained inputs. It is not provider preflight, an executable target or consent.
type DiagnosticGCPlan struct {
	ID                   string                `json:"id"`
	RecordID             string                `json:"record_id"`
	TaskID               int                   `json:"task_id"`
	Decision             string                `json:"decision"`
	Path                 string                `json:"path,omitempty"`
	WorkspaceID          string                `json:"workspace_id,omitempty"`
	NextStage            string                `json:"next_stage,omitempty"`
	ExpiryAt             string                `json:"expiry_at,omitempty"`
	GCRetention          DiagnosticGCRetention `json:"gc_retention"`
	Evidence             string                `json:"evidence"`
	ExecutionEligibility string                `json:"execution_eligibility"`
	Occurrences          int                   `json:"occurrences"`
}

// DiagnosticRuntimeObservation is provider-owned bounded presence evidence.
// No raw listing, mounts, environment, pane contents or provider error is exposed.
type DiagnosticRuntimeObservation struct {
	Presence string `json:"presence"`
	ID       string `json:"id,omitempty"`
	State    string `json:"state"`
	Problem  string `json:"problem,omitempty"`
}

type DiagnosticRuntime struct {
	ID                  string                       `json:"id"`
	WorkspaceID         string                       `json:"workspace_id"`
	Provider            string                       `json:"provider"`
	Name                string                       `json:"registered_name"`
	Observation         DiagnosticRuntimeObservation `json:"observation"`
	CachedIdentityCount int                          `json:"cached_identity_count"`
	IdentityComparison  string                       `json:"identity_comparison"`
	OwnershipEvidence   string                       `json:"ownership_evidence"`
	LeaseEvidence       string                       `json:"lease_evidence"`
}

// DiagnosticRetirementPreview contains only proposed native retirement effects.
// It is neither an observed runtime result nor an executable/accepted plan.
type DiagnosticRetirementPreview struct {
	ID                   string                       `json:"id"`
	RecordID             string                       `json:"record_id"`
	TaskID               int                          `json:"task_id"`
	Stage                string                       `json:"stage"`
	Targets              []DiagnosticRetirementTarget `json:"targets"`
	Preserved            []string                     `json:"preserved"`
	ExecutionEligibility string                       `json:"execution_eligibility"`
	OwnershipEvidence    string                       `json:"ownership_evidence"`
	AgentOwnerCount      *int                         `json:"agent_owner_count,omitempty"`
	LeaseEvidence        string                       `json:"lease_evidence"`
}

type DiagnosticRetirementTarget struct {
	ID                    string `json:"id"`
	Provider              string `json:"provider"`
	Effect                string `json:"effect"`
	SafetyNoticeCount     int    `json:"safety_notice_count"`
	AutomaticBlockerCount int    `json:"automatic_blocker_count"`
}

// Capabilities describe registered interfaces, not tool availability, provider
// configuration, authentication or permission to run any operation.
type DiagnosticIntegrationCapabilities struct {
	Collection                bool `json:"collection"`
	StatusCheck               bool `json:"status_check"`
	InteractiveAuthentication bool `json:"interactive_authentication"`
	BoundResolution           bool `json:"bound_resolution"`
	Cleanup                   bool `json:"cleanup"`
	RuntimeProbe              bool `json:"runtime_probe"`
	RateLimit                 bool `json:"rate_limit"`
}

// DiagnosticCollectionAttempt is the native Status/Collect/ResolveBindings
// envelope, not reconciliation, cache publication or business success.
type DiagnosticCollectionAttempt struct {
	StartedAt           string `json:"started_at"`
	CollectionStartedAt string `json:"collection_started_at,omitempty"`
	FinishedAt          string `json:"finished_at"`
	Outcome             string `json:"outcome"`
}

type DiagnosticCollectionEvidence struct {
	Latest         DiagnosticCollectionAttempt `json:"latest"`
	LastCompleteAt string                      `json:"last_complete_collection_finished_at,omitempty"`
}

type DiagnosticIntegration struct {
	CollectionEvidence           string                             `json:"collection_evidence"`
	Collection                   *DiagnosticCollectionEvidence      `json:"collection,omitempty"`
	ID                           string                             `json:"id"`
	Name                         string                             `json:"name"`
	Label                        string                             `json:"label,omitempty"`
	RegistrationCount            int                                `json:"registration_count"`
	Capabilities                 *DiagnosticIntegrationCapabilities `json:"capabilities,omitempty"`
	CapabilityEvidence           string                             `json:"capability_evidence"`
	CachedStatusCount            int                                `json:"cached_status_count"`
	CachedStatus                 string                             `json:"cached_status"`
	StatusEvidence               string                             `json:"status_evidence"`
	DetailsAvailable             bool                               `json:"details_available"`
	InstallationReportedRefCount *int                               `json:"installation_reported_ref_count,omitempty"`
	ActiveRefs                   int                                `json:"active_refs"`
	InactiveRefs                 int                                `json:"inactive_refs"`
	LatestRefObservedAt          string                             `json:"latest_retained_ref_observed_at,omitempty"`
	UnknownRefObservedAt         int                                `json:"refs_with_unknown_observed_at"`
	AccessEvidence               string                             `json:"access_evidence"`
	FreshnessEvidence            string                             `json:"freshness_evidence"`
}

type DiagnosticCleanupSafety struct {
	Kind            string `json:"kind"`
	BlocksAutomatic bool   `json:"blocks_automatic"`
	Expires         bool   `json:"expires"`
	Count           int    `json:"count"`
}

type DiagnosticCleanupTarget struct {
	LeaseEvidence         string                    `json:"lease_evidence"`
	CleanupMode           string                    `json:"cleanup_mode"`
	WorkspaceID           string                    `json:"workspace_id,omitempty"`
	ExpiryAt              string                    `json:"expiry_at,omitempty"`
	OwnershipEvidence     string                    `json:"ownership_evidence"`
	AgentOwnerCount       *int                      `json:"agent_owner_count,omitempty"`
	ID                    string                    `json:"id"`
	RecordID              string                    `json:"record_id"`
	TaskID                int                       `json:"task_id"`
	Provider              string                    `json:"provider"`
	Effect                string                    `json:"effect"`
	BranchEffect          string                    `json:"branch_effect,omitempty"`
	Preserved             []string                  `json:"preserved"`
	Safety                []DiagnosticCleanupSafety `json:"safety"`
	SafetyNoticeCount     int                       `json:"safety_notice_count"`
	AutomaticBlockerCount int                       `json:"automatic_blocker_count"`
	ExecutionEligibility  string                    `json:"execution_eligibility"`
}

// DiagnosticOperation is a volatile daemon handler observation, never a job,
// replay token, durable audit record or verification of resource outcomes.
type DiagnosticOperation struct {
	CleanupExecutionEvidence string                     `json:"cleanup_execution_evidence"`
	CleanupExecution         *OperationCleanupExecution `json:"cleanup_execution,omitempty"`
	CleanupTargetEvidence    string                     `json:"cleanup_target_evidence"`
	CleanupTargets           []OperationCleanupTarget   `json:"cleanup_targets,omitempty"`
	CleanupTargetsTruncated  *bool                      `json:"cleanup_targets_truncated,omitempty"`
	PhaseTimingEvidence      string                     `json:"phase_timing_evidence"`
	PhaseTimings             []OperationPhaseTiming     `json:"phase_timings,omitempty"`
	TimingEvidence           string                     `json:"timing_evidence"`
	StartedAt                string                     `json:"started_at,omitempty"`
	FinishedAt               string                     `json:"finished_at,omitempty"`
	ElapsedMilliseconds      *int64                     `json:"elapsed_milliseconds,omitempty"`
	ID                       string                     `json:"id"`
	Kind                     string                     `json:"kind"`
	Status                   string                     `json:"status"`
	TaskID                   int                        `json:"task_id,omitempty"`
	WorkspaceID              string                     `json:"workspace_id,omitempty"`
	ExecutionEvidence        string                     `json:"execution_evidence"`
	TargetEvidence           string                     `json:"target_evidence"`
	CompletedTargets         *int                       `json:"completed_targets,omitempty"`
	TotalTargets             *int                       `json:"total_targets,omitempty"`
}

type DiagnosticItem struct {
	WorkspacePolicy   *DiagnosticWorkspacePolicy    `json:"workspace_policy,omitempty"`
	RepositoryPolicy  *DiagnosticRepositoryPolicy   `json:"repository_policy,omitempty"`
	Operation         *DiagnosticOperation          `json:"operation,omitempty"`
	CleanupPreview    *DiagnosticCleanupTarget      `json:"cleanup_preview,omitempty"`
	RetirementPreview *DiagnosticRetirementPreview  `json:"retirement_preview,omitempty"`
	RecreationPreview *DiagnosticRecreationPreview  `json:"recreation_preview,omitempty"`
	AgentProbe        *DiagnosticAgentProbe         `json:"agent_probe,omitempty"`
	Integration       *DiagnosticIntegration        `json:"integration,omitempty"`
	Runtime           *DiagnosticRuntime            `json:"runtime,omitempty"`
	GCPlan            *DiagnosticGCPlan             `json:"gc_plan,omitempty"`
	AgentSchedule     *DiagnosticAgentScheduleEntry `json:"agent_schedule,omitempty"`
	WorkspaceResource *DiagnosticWorkspaceResource  `json:"workspace_resource,omitempty"`
	Agent             *DiagnosticAgent              `json:"agent,omitempty"`
	Ref               *DiagnosticRef                `json:"source_ref,omitempty"`
	Binding           *DiagnosticBinding            `json:"binding,omitempty"`
	ID                string                        `json:"id"`
	Task              *DiagnosticTask               `json:"task,omitempty"`
	Workspace         *DiagnosticWorkspace          `json:"workspace,omitempty"`
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

// DiagnosticSetting contains only a fixed boolean, mode or count, never a
// configuration string, command, path, identity, endpoint or secret value.
type DiagnosticSetting struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Origin      string `json:"origin"`
	Declaration string `json:"declaration"`
}

type DiagnosticPolicy struct {
	Available      bool                `json:"available"`
	LocationOrigin string              `json:"location_origin"`
	Scope          string              `json:"scope"`
	Evidence       string              `json:"evidence"`
	Reason         string              `json:"reason,omitempty"`
	CapturedAt     string              `json:"captured_at"`
	Revision       string              `json:"revision,omitempty"`
	Settings       []DiagnosticSetting `json:"settings"`
}

// DiagnosticOverview is one version-matched daemon-process projection, not an
// environment dump, loaded-settings attestation or workspace-effective policy.
type DiagnosticOverview struct {
	Schema    int                        `json:"diagnostics_schema"`
	Settings  DiagnosticPolicy           `json:"settings"`
	Providers []DiagnosticProviderStatus `json:"providers"`
}

// DiagnosticProviderStatus is cached installation-wide status, not a fresh
// access check. Private detail text and ref counts are deliberately absent.
type DiagnosticProviderStatus struct {
	Name             string `json:"name"`
	Status           string `json:"status"`
	DetailsAvailable bool   `json:"details_available"`
}
