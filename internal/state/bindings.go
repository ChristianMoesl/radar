package state

import "radar/internal/protocol"

// A concrete resource can disappear and be replaced at the same name/path.
// Provider IDs used by cleanup stay stable, but an old explicit note binding
// must not cause a replacement lifetime to inherit its cached record ownership.
func detachReplacedBindings(state persistedState, observed []protocol.Task) persistedState {
	incoming := map[string]protocol.SourceRef{}
	for _, task := range observed {
		for _, ref := range task.SourceRefs {
			incoming[ref.ID] = ref
		}
	}
	changedRecords := map[string]bool{}
	for i := range state.SourceRefs {
		old := &state.SourceRefs[i]
		fresh, seen := incoming[old.ID]
		if !seen || old.Snapshot.BindingKey == fresh.BindingKey {
			continue
		}
		explicitlyBound := false
		for _, owner := range state.SourceRefs {
			if owner.TaskRecordID != old.TaskRecordID || !owner.Snapshot.Authored {
				continue
			}
			for _, binding := range owner.Snapshot.Bindings {
				if binding.LinkingKey() == old.Snapshot.Binding().LinkingKey() {
					explicitlyBound = true
				}
			}
		}
		if !explicitlyBound {
			continue
		}
		changedRecords[old.TaskRecordID] = true
		old.TaskRecordID = ""
		old.Active = false
	}
	for i := range state.Records {
		record := &state.Records[i]
		if !changedRecords[record.ID] {
			continue
		}
		record.SourceRefIDs = sourceRefIDsForRecord(record.ID, state.SourceRefs)
		remaining := []protocol.SourceRef{}
		for _, ref := range state.SourceRefs {
			if ref.TaskRecordID == record.ID && (ref.Active || ref.Snapshot.RetainInactive) {
				remaining = append(remaining, ref.Snapshot)
			}
		}
		record.CanonicalKey = canonicalTaskKey(protocol.Task{SourceRefs: remaining})
	}
	return state
}
