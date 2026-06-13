package protocol

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
	ID       string            `json:"id"`
	Source   string            `json:"source"`
	Kind     string            `json:"kind"`
	Title    string            `json:"title,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	URL      string            `json:"url,omitempty"`
	Path     string            `json:"path,omitempty"`
	Branch   string            `json:"branch,omitempty"`
	Status   string            `json:"status,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
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
	OK      bool           `json:"ok"`
	Error   string         `json:"error,omitempty"`
	Summary *Summary       `json:"summary,omitempty"`
	Tasks   []Task         `json:"tasks,omitempty"`
	Sources []SourceStatus `json:"sources,omitempty"`
}
