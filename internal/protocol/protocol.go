package protocol

import "encoding/json"

const Version = "0.1.0"

type Request struct {
	Method       string          `json:"method"`
	TaskID       int             `json:"task_id,omitempty"`
	TaskMutation *TaskMutation   `json:"task_mutation,omitempty"`
	Cleanup      *CleanupPreview `json:"cleanup,omitempty"`
}

type TaskMutation struct {
	TaskID   int    `json:"task_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// CurrentContext contains client-side hints that the daemon can use when an
// action should target the current shell/tmux context instead of the first
// matching source ref on a task.
type CurrentContext struct {
	CWD         string `json:"cwd,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

func (c CurrentContext) Empty() bool {
	return c.CWD == "" && c.Worktree == "" && c.SessionName == "" && c.SessionID == ""
}

type CleanupTarget struct {
	SourceRefID       string            `json:"source_ref_id"`
	Source            string            `json:"source"`
	Kind              string            `json:"kind"`
	Title             string            `json:"title,omitempty"`
	Description       string            `json:"description,omitempty"`
	ResourceRole      string            `json:"resource_role,omitempty"`
	ResourceID        string            `json:"resource_id,omitempty"`
	Path              string            `json:"path,omitempty"`
	ProvidesWorkspace bool              `json:"provides_workspace,omitempty"`
	WorkspaceID       string            `json:"workspace_id,omitempty"`
	Branch            string            `json:"branch,omitempty"`
	Safety            []CleanupSafety   `json:"safety,omitempty"`
	Operation         map[string]string `json:"operation,omitempty"`
}

type CleanupSafety struct {
	Kind            string `json:"kind"`
	Message         string `json:"message"`
	BlocksAutomatic bool   `json:"blocks_automatic,omitempty"`
}

type CleanupPreview struct {
	TaskID    int             `json:"task_id"`
	TaskTitle string          `json:"task_title"`
	Targets   []CleanupTarget `json:"targets"`
}

type CleanupResult struct {
	TaskID  int             `json:"task_id"`
	Targets []CleanupTarget `json:"targets"`
}

type GarbageCollectionItem struct {
	TaskID int    `json:"task_id"`
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

type GarbageCollectionResult struct {
	Deleted []GarbageCollectionItem `json:"deleted"`
	Skipped []GarbageCollectionItem `json:"skipped"`
}

type Summary struct {
	Immediate   int `json:"immediate"`
	Attention   int `json:"attention"`
	InProgress  int `json:"in_progress"`
	Done        int `json:"done"`
	LowPriority int `json:"low_priority"`
}

type SourceStatus struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	Detail         string `json:"detail,omitempty"`
	SourceRefCount int    `json:"source_ref_count,omitempty"`
}

type SourceRefRole string

const (
	SourceRefRoleAuthoritative SourceRefRole = "authoritative"
	SourceRefRoleInformational SourceRefRole = "informational"
)

type SourceRefLifecycle string

const (
	SourceRefLifecycleWorkItem  SourceRefLifecycle = "work_item"
	SourceRefLifecycleWorkspace SourceRefLifecycle = "workspace"
	SourceRefLifecycleResource  SourceRefLifecycle = "resource"
)

type SourceRefAuthority string

const (
	SourceRefAuthorityPrimary      SourceRefAuthority = "primary"
	SourceRefAuthorityContributing SourceRefAuthority = "contributing"
	SourceRefAuthorityNone         SourceRefAuthority = "none"
)

type SourceRefPresentation struct {
	Label         string `json:"label,omitempty"`
	PreferTitle   bool   `json:"prefer_title,omitempty"`
	TitleOrder    *int   `json:"title_order,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type SourceRefAcknowledgement struct {
	Cursor               string `json:"cursor,omitempty"`
	Blocking             bool   `json:"blocking,omitempty"`
	FallbackSignal       string `json:"fallback_signal,omitempty"`
	FallbackReason       string `json:"fallback_reason,omitempty"`
	HideWhenAcknowledged bool   `json:"hide_when_acknowledged,omitempty"`
}

type SourceRef struct {
	ID                  string                    `json:"id"`
	Busy                bool                      `json:"busy,omitempty"`
	InUse               bool                      `json:"in_use,omitempty"`
	Authored            bool                      `json:"authored,omitempty"`
	Source              string                    `json:"source"`
	SourceLabel         string                    `json:"source_label,omitempty"`
	Kind                string                    `json:"kind"`
	Role                SourceRefRole             `json:"role"`
	Title               string                    `json:"title,omitempty"`
	Repo                string                    `json:"repo,omitempty"`
	URL                 string                    `json:"url,omitempty"`
	Path                string                    `json:"path,omitempty"`
	ProvidesWorkspace   bool                      `json:"provides_workspace,omitempty"`
	WorkspaceAnchorPath string                    `json:"workspace_anchor_path,omitempty"`
	WorkspaceEntry      bool                      `json:"workspace_entry,omitempty"`
	WorkspaceID         string                    `json:"workspace_id,omitempty"`
	Branch              string                    `json:"branch,omitempty"`
	Status              string                    `json:"status,omitempty"`
	Signal              string                    `json:"signal,omitempty"`
	CanonicalKey        string                    `json:"canonical_key,omitempty"`
	LinkingKeys         []string                  `json:"linking_keys,omitempty"`
	Metadata            map[string]string         `json:"metadata,omitempty"`
	EntityID            string                    `json:"entity_id,omitempty"`
	Lifecycle           SourceRefLifecycle        `json:"lifecycle,omitempty"`
	Authority           SourceRefAuthority        `json:"authority,omitempty"`
	RetainInactive      bool                      `json:"retain_inactive,omitempty"`
	Presentation        SourceRefPresentation     `json:"presentation,omitempty"`
	DisplayOrder        int                       `json:"display_order,omitempty"`
	Acknowledgement     *SourceRefAcknowledgement `json:"acknowledgement,omitempty"`
}

type Task struct {
	ID                    int               `json:"id"`
	TargetTaskID          int               `json:"-"`
	Busy                  bool              `json:"busy,omitempty"`
	Kind                  string            `json:"kind"`
	Title                 string            `json:"title"`
	Repo                  string            `json:"repo,omitempty"`
	URL                   string            `json:"url,omitempty"`
	Attention             string            `json:"attention"`
	Reason                string            `json:"reason"`
	DoneAt                string            `json:"done_at,omitempty"`
	SourceRefs            []SourceRef       `json:"source_refs,omitempty"`
	Metadata              map[string]string `json:"metadata,omitempty"`
	AcknowledgementCursor string            `json:"acknowledgement_cursor,omitempty"`
}

type Response struct {
	OK                      bool                     `json:"ok"`
	Error                   string                   `json:"error,omitempty"`
	Revision                int64                    `json:"revision,omitempty"`
	Version                 string                   `json:"version,omitempty"`
	Summary                 *Summary                 `json:"summary,omitempty"`
	Tasks                   []Task                   `json:"tasks,omitempty"`
	Task                    *Task                    `json:"task,omitempty"`
	Sources                 []SourceStatus           `json:"sources,omitempty"`
	CleanupPreview          *CleanupPreview          `json:"cleanup_preview,omitempty"`
	CleanupResult           *CleanupResult           `json:"cleanup_result,omitempty"`
	GarbageCollectionResult *GarbageCollectionResult `json:"garbage_collection_result,omitempty"`
}

func (r Response) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"ok": r.OK}
	if r.Error != "" {
		fields["error"] = r.Error
	}
	if r.Revision != 0 {
		fields["revision"] = r.Revision
	}
	if r.Version != "" {
		fields["version"] = r.Version
	}
	if r.Summary != nil {
		fields["summary"] = r.Summary
	}
	if r.Tasks != nil {
		fields["tasks"] = r.Tasks
	}
	if r.Task != nil {
		fields["task"] = r.Task
	}
	if r.Sources != nil {
		fields["sources"] = r.Sources
	}
	if r.CleanupPreview != nil {
		fields["cleanup_preview"] = r.CleanupPreview
	}
	if r.CleanupResult != nil {
		fields["cleanup_result"] = r.CleanupResult
	}
	if r.GarbageCollectionResult != nil {
		fields["garbage_collection_result"] = r.GarbageCollectionResult
	}
	return json.Marshal(fields)
}
