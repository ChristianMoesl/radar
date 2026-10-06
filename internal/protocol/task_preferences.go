package protocol

// SourceBinding is authored tracking intent, not a source observation. ID is
// owned by its provider; Key optionally distinguishes concrete resource lifetimes.
// WorkItem bindings must be resolved before an authored task can auto-complete.
type SourceBinding struct {
	Source   string `json:"source" yaml:"source"`
	Kind     string `json:"kind" yaml:"kind"`
	ID       string `json:"id" yaml:"id"`
	Key      string `json:"key,omitempty" yaml:"key,omitempty"`
	WorkItem bool   `json:"work_item,omitempty" yaml:"work_item,omitempty"`
}

func (b SourceBinding) LinkingKey() string {
	identity := b.ID
	if b.Key != "" {
		identity = b.Key
	}
	return "source-ref:" + b.Source + ":" + b.Kind + ":" + identity
}

func (r SourceRef) Binding() SourceBinding {
	return SourceBinding{Source: r.Source, Kind: r.Kind, ID: r.ID, Key: r.BindingKey,
		WorkItem: r.Lifecycle == SourceRefLifecycleWorkItem && r.Authority == SourceRefAuthorityContributing}
}

// DisplayGroup never changes lifecycle or the underlying attention signal.
func (t Task) DisplayGroup() string {
	if t.Attention == "done" {
		return "done"
	}
	if t.Muted {
		return "muted"
	}
	return t.Attention
}
