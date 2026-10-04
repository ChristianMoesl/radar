package protocol

// TaskDeletionPreview is a confirmed, source-owned deletion plan. Revision is
// opaque to frontends; the provider must revalidate it before moving any data.
type TaskDeletionPreview struct {
	TaskID         int    `json:"task_id"`
	TaskTitle      string `json:"task_title"`
	SourceRefID    string `json:"source_ref_id"`
	Path           string `json:"path"`
	TrashDirectory string `json:"trash_directory"`
	Description    string `json:"description"`
	Revision       string `json:"revision"`
}

type TaskDeletionResult struct {
	TaskID       int    `json:"task_id"`
	SourceRefID  string `json:"source_ref_id"`
	OriginalPath string `json:"original_path"`
	TrashPath    string `json:"trash_path"`
}
