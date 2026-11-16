package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"radar/internal/protocol"
	"radar/internal/state"
)

func sourceView(record state.SourceRefRecord, recordID string, taskID int, input bool) protocol.DiagnosticRef {
	ref := record.Snapshot
	return protocol.DiagnosticRef{ID: referenceID(record.ID), ProviderID: clip(ref.ID), RecordID: clip(recordID), TaskID: taskID, LifecycleInput: input, Source: clip(ref.Source), Kind: clip(ref.Kind), Role: ref.Role, Lifecycle: ref.Lifecycle, Authority: ref.Authority, Active: record.Active, ObservedAt: clip(record.ObservedAt), Path: clip(ref.Path), WorkspaceID: clip(ref.WorkspaceID), AgentID: clip(ref.AgentID), Signal: clip(ref.Signal), Activity: ref.Activity.String(), Authored: ref.Authored, Review: ref.AgentReview, Muted: ref.Muted, BindingCount: len(ref.Bindings), BindingProblem: ref.BindingError != "", CleanupProblemCount: len(ref.CleanupIssues)}
}

func referenceItems(req protocol.DiagnosticsRequest, records []state.TaskRecord, refs []state.SourceRefRecord, byRecord map[string][]state.SourceRefRecord) []protocol.DiagnosticItem {
	recordsByID := make(map[string]state.TaskRecord, len(records))
	inputs := map[string]map[string]bool{}
	for _, record := range records {
		recordsByID[record.ID] = record
		inputs[record.ID] = state.LifecycleInputs(record, byRecord[record.ID])
	}
	items := []protocol.DiagnosticItem{}
	for _, ref := range refs {
		record, owned := recordsByID[ref.TaskRecordID]
		if req.State != "" && (!owned || req.State != record.State) {
			continue
		}
		if req.Workspace != "" {
			related := byRecord[ref.TaskRecordID]
			if !owned {
				related = []state.SourceRefRecord{ref}
			}
			if !recordInWorkspace(related, req.Workspace) {
				continue
			}
		}
		if req.Section == "source_refs" {
			if req.ParentID != "" && (!owned || (req.ParentID != record.ID && req.ParentID != strconv.Itoa(record.NumericID))) {
				continue
			}
			if req.ID != "" && req.ID != referenceID(ref.ID) {
				continue
			}
			view := sourceView(ref, ref.TaskRecordID, record.NumericID, inputs[ref.TaskRecordID][ref.ID])
			items = append(items, protocol.DiagnosticItem{ID: view.ID, Ref: &view})
			continue
		}
		if req.ParentID != "" && req.ParentID != referenceID(ref.ID) {
			continue
		}
		// Identical authored entries have one stable semantic ID and an explicit
		// occurrence count, rather than duplicate pagination identities.
		bindings := map[string]*protocol.DiagnosticBinding{}
		for _, binding := range ref.Snapshot.Bindings {
			identity, _ := json.Marshal(struct {
				Ref     string
				Binding protocol.SourceBinding
			}{ref.ID, binding})
			sum := sha256.Sum256(identity)
			id := "binding:" + hex.EncodeToString(sum[:])
			if req.ID != "" && req.ID != id {
				continue
			}
			if existing := bindings[id]; existing != nil {
				existing.Occurrences++
				continue
			}
			view := &protocol.DiagnosticBinding{ID: id, SourceRefID: referenceID(ref.ID), RecordID: clip(ref.TaskRecordID), TaskID: record.NumericID, Source: clip(binding.Source), Kind: clip(binding.Kind), TargetID: clip(binding.ID), Key: clip(binding.Key), WorkItem: binding.WorkItem, Occurrences: 1}
			bindings[id] = view
			items = append(items, protocol.DiagnosticItem{ID: id, Binding: view})
		}
	}
	return items
}

// Reference records may contain long provider paths or lifetime suffixes. Opaque
// diagnostic IDs remain selectable without clipping or exposing that identity.
func referenceID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "ref:" + hex.EncodeToString(sum[:])
}
