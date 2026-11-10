package state

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"radar/internal/protocol"
)

// Authored identity is provider-owned, never a shared ticket, branch or path.
func authoredIdentity(ref protocol.SourceRef) string {
	if authoritativeRef(ref) && (ref.Authored || (ref.Source == "obsidian" && ref.Kind == "task" && ref.Authority == protocol.SourceRefAuthorityPrimary)) {
		return ref.ID
	}
	return ""
}

func authoredIdentities(refs []protocol.SourceRef) []string {
	var ids []string
	for _, ref := range refs {
		if id := authoredIdentity(ref); id != "" {
			ids = mergeStringSet(ids, []string{id})
		}
	}
	slices.Sort(ids)
	return ids
}

func recordAuthoredIdentities(state persistedState) map[string][]string {
	owners := map[string][]string{}
	for _, record := range state.Records {
		owners[record.ID] = authoredIdentities(record.Snapshot.SourceRefs)
		if strings.HasPrefix(record.CanonicalKey, "obsidian:task:") {
			owners[record.ID] = mergeStringSet(owners[record.ID], []string{record.CanonicalKey})
		}
	}
	// Include inactive history: a missing note is not permission to adopt its
	// task number, acknowledgement or pending review for a different note.
	for _, ref := range state.SourceRefs {
		if id := authoredIdentity(ref.Snapshot); id != "" {
			owners[ref.TaskRecordID] = mergeStringSet(owners[ref.TaskRecordID], []string{id})
		}
	}
	for _, ids := range owners {
		slices.Sort(ids)
	}
	return owners
}

func combinedAuthoredIdentities(owners map[string][]string, records []string) []string {
	var ids []string
	for _, record := range records {
		ids = mergeStringSet(ids, owners[record])
	}
	return ids
}

// LinkConflict describes a preserved invalid aggregate, not an automatic repair.
// The full export retains history and authored binding intent alongside it.
type LinkConflict struct {
	RecordID     string   `json:"record_id"`
	NumericID    int      `json:"numeric_id"`
	AuthoredIDs  []string `json:"authored_ids"`
	SourceRefIDs []string `json:"source_ref_ids"`
	Decisions    []string `json:"decisions_required"`
}

func linkConflicts(state persistedState) []LinkConflict {
	owners := recordAuthoredIdentities(state)
	conflicts := []LinkConflict{}
	for _, record := range state.Records {
		if len(owners[record.ID]) < 2 {
			continue
		}
		refs := mergeStringSet(record.SourceRefIDs, sourceRefIDs(record.Snapshot.SourceRefs))
		refs = mergeStringSet(refs, sourceRefIDsForRecord(record.ID, state.SourceRefs))
		slices.Sort(refs)
		conflicts = append(conflicts, LinkConflict{RecordID: record.ID, NumericID: record.NumericID,
			AuthoredIDs: owners[record.ID], SourceRefIDs: refs, Decisions: []string{
				"Choose which authored identity retains this numeric task identity and its history; allocate fresh IDs for the others",
				"Assign each unowned/shared contributor explicitly or leave it separate; preserve existing authored bindings",
				"Decide acknowledgement and pending-review attribution where the aggregate has lost provenance; never clear review intent implicitly",
			}})
	}
	slices.SortFunc(conflicts, func(a, b LinkConflict) int { return strings.Compare(a.RecordID, b.RecordID) })
	return conflicts
}

// InspectLinkConflicts is pure: it neither opens state nor rewrites projections.
func InspectLinkConflicts(data []byte) ([]LinkConflict, error) {
	if len(data) == 0 || string(data) == "null" {
		return []LinkConflict{}, nil
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Version != stateVersion {
		return nil, fmt.Errorf("unsupported projection version %d; state preserved", state.Version)
	}
	return linkConflicts(state), nil
}

func validateAuthoredLinks(state persistedState) error {
	conflicts := linkConflicts(state)
	if len(conflicts) == 0 {
		return nil
	}
	var records []string
	for _, conflict := range conflicts {
		records = append(records, fmt.Sprintf("%s [%s]", conflict.RecordID, strings.Join(conflict.AuthoredIDs, ", ")))
	}
	return fmt.Errorf("conflicting authored-note identities in %s; state preserved, no automatic split: stop Radar, use radar state-backup, radar state-export and radar repair-task-links to plan an explicit repair (docs/task-link-repair.md)", strings.Join(records, "; "))
}
