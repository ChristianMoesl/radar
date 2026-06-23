package protocol

import "encoding/json"

const Version = "0.1.0"

type Request struct {
	Method string `json:"method"`
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

type SourceRef struct {
	ID           string            `json:"id"`
	Source       string            `json:"source"`
	SourceLabel  string            `json:"source_label,omitempty"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title,omitempty"`
	Repo         string            `json:"repo,omitempty"`
	URL          string            `json:"url,omitempty"`
	Path         string            `json:"path,omitempty"`
	Branch       string            `json:"branch,omitempty"`
	Status       string            `json:"status,omitempty"`
	CanonicalKey string            `json:"canonical_key,omitempty"`
	LinkingKeys  []string          `json:"linking_keys,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type Task struct {
	ID         int               `json:"id"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	Repo       string            `json:"repo,omitempty"`
	URL        string            `json:"url,omitempty"`
	Attention  string            `json:"attention"`
	Reason     string            `json:"reason"`
	DoneAt     string            `json:"done_at,omitempty"`
	SourceRefs []SourceRef       `json:"source_refs,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type Response struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Revision int64          `json:"revision,omitempty"`
	Version  string         `json:"version,omitempty"`
	Summary  *Summary       `json:"summary,omitempty"`
	Tasks    []Task         `json:"tasks,omitempty"`
	Sources  []SourceStatus `json:"sources,omitempty"`
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
	if r.Sources != nil {
		fields["sources"] = r.Sources
	}
	return json.Marshal(fields)
}
