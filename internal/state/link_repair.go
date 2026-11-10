package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"radar/internal/protocol"
)

// LinkRepairPlan requires explicit attribution for facts an old aggregate lost.
// No default chooses a note, clears pending review, or reassigns a binding.
type LinkRepairPlan struct {
	ProjectionSHA256 string           `json:"projection_sha256"`
	Conflicts        []LinkConflict   `json:"conflicts"`
	Resolutions      []LinkResolution `json:"resolutions"`
}

type LinkResolution struct {
	RecordID         string   `json:"record_id"`
	KeepAuthoredID   string   `json:"keep_authored_id"`
	AcknowledgedIDs  []string `json:"acknowledged_authored_ids"`
	PendingReviewIDs []string `json:"pending_review_authored_ids"`
	// Every non-authored observation needs an explicit entry. Empty means a
	// separate, unowned task, not an inferred owner or a deleted observation.
	Assignments map[string]string `json:"assignments"`
}

func projectionDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func PlanLinkRepair(data []byte) (LinkRepairPlan, error) {
	conflicts, err := InspectLinkConflicts(data)
	plan := LinkRepairPlan{ProjectionSHA256: projectionDigest(data), Conflicts: conflicts, Resolutions: []LinkResolution{}}
	for _, conflict := range conflicts {
		plan.Resolutions = append(plan.Resolutions, LinkResolution{RecordID: conflict.RecordID, Assignments: map[string]string{}})
	}
	return plan, err
}

// RepairTaskLinks is a pure, fingerprint-checked transformation. The caller
// must hold the offline state lock, back up SQLite, validate current bindings
// in the same transaction, and journal the original projection before writing.
func RepairTaskLinks(data []byte, plan LinkRepairPlan, bindingOwners map[string]string) ([]byte, error) {
	if plan.ProjectionSHA256 != projectionDigest(data) {
		return nil, fmt.Errorf("projection changed; export a fresh repair plan")
	}
	conflicts, err := InspectLinkConflicts(data)
	if err != nil {
		return nil, err
	}
	if len(conflicts) == 0 || len(plan.Resolutions) != len(conflicts) {
		return nil, fmt.Errorf("repair requires exactly one resolution per conflicting record")
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	resolutions := map[string]LinkResolution{}
	for _, resolution := range plan.Resolutions {
		if _, exists := resolutions[resolution.RecordID]; exists {
			return nil, fmt.Errorf("duplicate repair resolution %s", resolution.RecordID)
		}
		resolutions[resolution.RecordID] = resolution
	}
	for _, record := range state.Records {
		state.NextTaskID = max(state.NextTaskID, record.NumericID)
	}
	for _, conflict := range conflicts {
		r, ok := resolutions[conflict.RecordID]
		if !ok || !slices.Contains(conflict.AuthoredIDs, r.KeepAuthoredID) {
			return nil, fmt.Errorf("%s requires an explicit authored identity to retain its task number/history", conflict.RecordID)
		}
		original := *recordByID(state.Records, conflict.RecordID)
		if r.AcknowledgedIDs == nil || r.PendingReviewIDs == nil {
			return nil, fmt.Errorf("%s requires explicit acknowledgement and pending-review attribution (use [] only when absent)", conflict.RecordID)
		}
		if original.Ack.Cursor != "" && len(r.AcknowledgedIDs) == 0 || original.AgentReviewPending && len(r.PendingReviewIDs) == 0 {
			return nil, fmt.Errorf("%s cannot discard acknowledgement or pending review history", conflict.RecordID)
		}
		for _, id := range append(slices.Clone(r.AcknowledgedIDs), r.PendingReviewIDs...) {
			if !slices.Contains(conflict.AuthoredIDs, id) {
				return nil, fmt.Errorf("%s attributes history to an unknown authored identity", conflict.RecordID)
			}
		}
		byOwner := map[string][]int{}
		seen, usedAssignments := map[string]bool{}, map[string]bool{}
		for i, ref := range state.SourceRefs {
			if ref.TaskRecordID != conflict.RecordID {
				continue
			}
			seen[ref.ID] = true
			owner := authoredIdentity(ref.Snapshot)
			if owner == "" {
				var assigned bool
				owner, assigned = r.Assignments[ref.ID]
				if !assigned {
					return nil, fmt.Errorf("%s needs an explicit owner or empty separate assignment for %s", conflict.RecordID, ref.ID)
				}
				usedAssignments[ref.ID] = true
				if bound := bindingOwners[ref.Snapshot.Binding().LinkingKey()]; bound != "" && bound != owner {
					return nil, fmt.Errorf("%s assignment conflicts with existing authored binding for %s", conflict.RecordID, ref.ID)
				}
			}
			if owner != "" && !slices.Contains(conflict.AuthoredIDs, owner) {
				return nil, fmt.Errorf("unknown assignment owner for %s", ref.ID)
			}
			byOwner[owner] = append(byOwner[owner], i)
		}
		if len(usedAssignments) != len(r.Assignments) {
			return nil, fmt.Errorf("%s contains unknown or authored-reference assignments", conflict.RecordID)
		}
		for _, id := range conflict.SourceRefIDs {
			if !seen[id] {
				return nil, fmt.Errorf("%s has snapshot-only reference %s; retain the backup and recover its observation provenance before repair", conflict.RecordID, id)
			}
		}
		for _, id := range conflict.AuthoredIDs {
			if len(byOwner[id]) == 0 {
				return nil, fmt.Errorf("%s has no stored observation for %s; recover its provenance before repair", conflict.RecordID, id)
			}
		}
		owners := slices.Clone(conflict.AuthoredIDs)
		if len(byOwner[""]) > 0 {
			owners = append(owners, "")
		}
		var replacement []TaskRecord
		for _, owner := range owners {
			record := original
			if owner != r.KeepAuthoredID {
				state.NextTaskID++
				record.NumericID, record.ID = state.NextTaskID, fmt.Sprintf("task:%d", state.NextTaskID)
			}
			var refs []protocol.SourceRef
			for _, i := range byOwner[owner] {
				state.SourceRefs[i].TaskRecordID = record.ID
				refs = append(refs, state.SourceRefs[i].Snapshot)
			}
			record.Snapshot = cloneTask(original.Snapshot)
			record.Snapshot.SourceRefs = cloneSourceRefs(refs)
			record.Snapshot.AcknowledgementCursor = ""
			record.SourceRefIDs = sourceRefIDs(refs)
			record.CanonicalKey = canonicalTaskKey(record.Snapshot)
			record.Kind = recordKind(record.Snapshot, record.CanonicalKey)
			if title := preferredTitle(refs); title != "" {
				record.Snapshot.Title = title
			}
			if !slices.Contains(r.AcknowledgedIDs, owner) {
				record.Ack = TaskAckState{}
			}
			record.AgentReviewPending = original.AgentReviewPending && slices.Contains(r.PendingReviewIDs, owner)
			replacement = append(replacement, record)
		}
		updateRecordLifecycles(replacement, state.SourceRefs, original.UpdatedAt)
		// Reconciliation may recognize a previously saved hold, but a repair
		// must not erase the explicitly attributed pending intent itself.
		for i := range replacement {
			ids := authoredIdentities(replacement[i].Snapshot.SourceRefs)
			replacement[i].AgentReviewPending = original.AgentReviewPending && len(ids) == 1 && slices.Contains(r.PendingReviewIDs, ids[0])
		}
		for i, record := range state.Records {
			if record.ID == original.ID {
				state.Records = append(append(slices.Clone(state.Records[:i]), state.Records[i+1:]...), replacement...)
				break
			}
		}
	}
	if err := validateAuthoredLinks(state); err != nil {
		return nil, err
	}
	slices.SortFunc(state.Records, func(a, b TaskRecord) int { return a.NumericID - b.NumericID })
	return json.Marshal(state)
}
