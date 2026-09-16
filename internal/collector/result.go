package collector

import "radar/internal/protocol"

// ReplaceSource replaces a stale source snapshot without discarding other
// sources' observations, statuses, or full-collection completeness evidence.
func (r *Result) ReplaceSource(name string, fresh Result) {
	tasks := make([]protocol.Task, 0, len(r.Tasks)+len(fresh.Tasks))
	for _, task := range r.Tasks {
		refs := make([]protocol.SourceRef, 0, len(task.SourceRefs))
		for _, ref := range task.SourceRefs {
			if ref.Source != name {
				refs = append(refs, ref)
			}
		}
		if len(refs) > 0 {
			task.SourceRefs = refs
			tasks = append(tasks, task)
		}
	}
	r.Tasks = append(tasks, fresh.Tasks...)
	for _, status := range fresh.Sources {
		found := false
		for i := range r.Sources {
			if r.Sources[i].Name == status.Name {
				r.Sources[i] = status
				found = true
				break
			}
		}
		if !found {
			r.Sources = append(r.Sources, status)
		}
	}
	found := false
	for _, source := range r.SourceNames {
		found = found || source == name
	}
	if !found {
		r.SourceNames = append(r.SourceNames, name)
	}
	if r.Complete != nil {
		r.Complete[name] = fresh.Complete[name]
	}
}
