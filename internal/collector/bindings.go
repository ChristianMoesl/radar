package collector

import (
	"radar/internal/protocol"
	"sort"
)

// Fresh authoring observations supersede cached bindings. A failed authoring
// read retains tracking intent but can never provide completion evidence.
func collectedBindings(previous []protocol.Task, collections []sourceCollection) []protocol.SourceBinding {
	complete := map[string]bool{}
	bindings := map[string]protocol.SourceBinding{}
	add := func(ref protocol.SourceRef) {
		if !ref.Authored || ref.Role != protocol.SourceRefRoleAuthoritative {
			return
		}
		for _, binding := range ref.Bindings {
			bindings[binding.LinkingKey()] = binding
		}
	}
	for _, collection := range collections {
		if collection.descriptor.Name == "" {
			continue
		}
		complete[collection.descriptor.Name] = collection.status.CanRun && collection.result.Complete
		for _, observation := range collection.result.Observations {
			add(observation.Ref)
		}
	}
	for _, task := range previous {
		for _, ref := range task.SourceRefs {
			if !complete[ref.Source] {
				add(ref)
			}
		}
	}
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]protocol.SourceBinding, 0, len(keys))
	for _, key := range keys {
		result = append(result, bindings[key])
	}
	return result
}

// Completed adopted history is still resolver input, but unchanged bindings
// must not trigger a vault-wide ownership scan for every note on every poll.
// The fresh owner observation already came from a validated authoring scan.
func hasNewBindings(owner protocol.SourceRef, task protocol.Task) bool {
	known := map[string]protocol.SourceBinding{}
	for _, binding := range owner.Bindings {
		known[binding.LinkingKey()] = binding
	}
	for _, ref := range task.SourceRefs {
		if ref.Role != protocol.SourceRefRoleAuthoritative {
			continue
		}
		if ref.Authored {
			if ref.ID != owner.ID {
				return true
			} // Provider must report ambiguous ownership.
			continue
		}
		if ref.BindingError != "" {
			return true
		}
		binding := ref.Binding()
		previous, found := known[binding.LinkingKey()]
		if !found || (binding.WorkItem && !previous.WorkItem) {
			return true
		}
	}
	return false
}
