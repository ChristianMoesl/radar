package state

import (
	"encoding/json"
	"fmt"
	"strings"

	"radar/internal/protocol"
)

// ValidateLegacy is used only by the one-time importer, never ordinary readers.
func ValidateLegacy(data []byte) error {
	if len(data) > maxStateFileSize {
		return fmt.Errorf("legacy state exceeds size limit")
	}
	var old persistedState
	if err := json.Unmarshal(data, &old); err != nil {
		return err
	}
	if old.Version != stateVersion {
		return fmt.Errorf("unsupported legacy state version %d", old.Version)
	}
	ids := map[string]bool{}
	numeric := map[int]bool{}
	for _, record := range old.Records {
		if record.ID == "" || ids[record.ID] || record.NumericID <= 0 || numeric[record.NumericID] {
			return fmt.Errorf("invalid or duplicate legacy task identity")
		}
		ids[record.ID] = true
		numeric[record.NumericID] = true
		if old.NextTaskID < record.NumericID {
			return fmt.Errorf("legacy next_task_id would reuse a task identity")
		}
	}
	refs := map[string]bool{}
	for _, ref := range old.SourceRefs {
		if ref.ID == "" || refs[ref.ID] || !ids[ref.TaskRecordID] {
			return fmt.Errorf("invalid legacy source reference %s", ref.ID)
		}
		if ref.Snapshot.ID != "" && ref.Snapshot.ID != ref.ID {
			return fmt.Errorf("legacy source snapshot identity mismatch: %s", ref.ID)
		}
		refs[ref.ID] = true
	}
	return nil
}

// LegacyBindings recovers associations that previously survived only in the
// projection. Unprovable local lifetimes are retained in imported observations,
// but are never promoted into unsafe durable selectors.
func LegacyBindings(data []byte) (map[string][]protocol.SourceBinding, []string, error) {
	result := map[string][]protocol.SourceBinding{}
	var warnings []string
	if len(data) == 0 {
		return result, warnings, nil
	}
	if err := ValidateLegacy(data); err != nil {
		return nil, nil, err
	}
	var old persistedState
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, nil, err
	}
	for _, record := range old.Records {
		ids := map[string]bool{}
		if strings.HasPrefix(record.CanonicalKey, "obsidian:task:") {
			ids[strings.TrimPrefix(record.CanonicalKey, "obsidian:task:")] = true
		}
		for _, ref := range old.SourceRefs {
			if ref.TaskRecordID == record.ID && ref.Snapshot.Authored && strings.HasPrefix(ref.ID, "obsidian:task:") {
				ids[strings.TrimPrefix(ref.ID, "obsidian:task:")] = true
			}
		}
		for _, ref := range record.Snapshot.SourceRefs {
			if ref.Authored && strings.HasPrefix(ref.ID, "obsidian:task:") {
				ids[strings.TrimPrefix(ref.ID, "obsidian:task:")] = true
			}
		}
		for id := range ids {
			if _, ok := result[id]; !ok {
				result[id] = nil
			}
		}
		if len(ids) > 1 {
			// A linked projection can group distinct authored notes. Keep every
			// identity for note validation, but do not guess which note owns the
			// group's cache-only associations. Explicit note bindings and the
			// entire legacy projection are imported separately and unchanged.
			warnings = append(warnings, fmt.Sprintf("legacy task %s groups multiple authored notes; retaining observations without promoting cache-only bindings", record.ID))
			continue
		}
		for id := range ids {
			for _, stored := range old.SourceRefs {
				ref := stored.Snapshot
				if stored.TaskRecordID != record.ID || ref.Authored || ref.Role != protocol.SourceRefRoleAuthoritative || ref.Source == "obsidian" {
					continue
				}
				if ref.BindingError != "" {
					warnings = append(warnings, ref.ID+": "+ref.BindingError)
					continue
				}
				if ref.Source == "" || ref.Kind == "" || ref.ID == "" {
					return nil, nil, fmt.Errorf("incomplete source identity %s", stored.ID)
				}
				result[id] = append(result[id], ref.Binding())
			}
		}
	}
	return result, warnings, nil
}
