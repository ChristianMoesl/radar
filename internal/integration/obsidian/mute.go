package obsidian

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// SetMuted adopts a source-only task on demand. Discovery and the complete
// bound-note write share the existing note lock, including retries after a
// successful write whose subsequent collection/publication failed.
func (s Source) SetMuted(ctx context.Context, task protocol.Task, muted bool) (integration.AuthoredTaskIdentity, error) {
	// An explicit authored identity is enough to clear an existing preference
	// when a supporting resource's lifetime is temporarily unreadable. Never
	// use that resource's unsafe raw identity to discover or adopt a note.
	requireIdentity := true
	if !muted {
		for _, ref := range task.SourceRefs {
			if ref.Role == protocol.SourceRefRoleAuthoritative && ref.Source == "obsidian" && ref.Kind == "task" && ref.Authority == protocol.SourceRefAuthorityPrimary {
				requireIdentity = false
			}
		}
	}
	bindings, err := taskBindings(task, requireIdentity)
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	var identity integration.AuthoredTaskIdentity
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		vault, err := s.configuredVault()
		if err != nil {
			return err
		}
		current, err := associatedNote(vault, task, bindings)
		if err != nil {
			return err
		}
		if current == nil {
			// Clearing a preference that has never been authored is a true no-op.
			if !muted {
				return nil
			}
			if len(bindings) == 0 {
				return fmt.Errorf("cannot adopt a task without an authoritative source identity")
			}
			desired, err := s.PrepareWorkspaceNote(ctx, task.Title)
			if err != nil {
				return err
			}
			content, err := workspaceNoteContent(desired)
			if err != nil {
				return err
			}
			prepared, err := parseNote(content)
			if err != nil {
				return err
			}
			updates := map[string]string{"radar-muted": "true", "radar-source-refs": encodeBindings(bindings)}
			if task.Attention == "done" {
				completed := task.DoneAt
				if completed == "" {
					completed = time.Now().UTC().Format(time.RFC3339)
				}
				updates["radar-state"] = "done"
				updates["radar-completed-at"] = completed
				updates["radar-completion-baseline"] = "pending"
			}
			content, _, err = updateNoteContent(prepared, updates)
			if err != nil {
				return err
			}
			// This creates only the private note directory, not a workspace or
			// registry entry. No unbound intermediate note is ever published.
			if err := createWorkspaceNote(desired, content); err != nil {
				return err
			}
			identity.SourceRefID = desired.LinkingKey
			return nil
		}
		identity.SourceRefID = "obsidian:task:" + current.ID
		updates := map[string]string{}
		if current.Muted != muted {
			updates["radar-muted"] = strconv.FormatBool(muted)
		}
		if muted {
			merged, changed := mergeBindings(current.Bindings, bindings)
			if _, adopted := current.fields["radar-source-refs"]; changed || !adopted {
				updates["radar-source-refs"] = encodeBindings(merged)
			}
		}
		_, err = persistPreference(*current, updates)
		return err
	})
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	return identity, nil
}

// ReconcileBindings records only new authoritative tracking intent on an
// already-adopted note. It never manufactures a note or changes its lifecycle.
func (s Source) ReconcileBindings(ctx context.Context, ref protocol.SourceRef, task protocol.Task) (*integration.Observation, error) {
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return nil, err
	}
	var observation *integration.Observation
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.noteForRef(ref)
		if err != nil {
			return err
		}
		if _, adopted := current.fields["radar-source-refs"]; !adopted {
			return nil
		}
		if ref.Metadata["content_hash"] != fmt.Sprintf("%x", sha256.Sum256([]byte(current.content))) {
			return fmt.Errorf("task note changed since collection")
		}
		bindings, err := taskBindings(task, true)
		if err != nil {
			return err
		}
		vault, err := s.configuredVault()
		if err != nil {
			return err
		}
		// Include the explicit target even if a caller supplied a task without
		// its authoring ref. Other authored refs/binding owners must agree.
		task.SourceRefs = append(append([]protocol.SourceRef(nil), task.SourceRefs...), ref)
		associated, err := associatedNote(vault, task, bindings)
		if err != nil {
			return err
		}
		if associated == nil || associated.ID != current.ID || associated.content != current.content {
			return fmt.Errorf("task note association changed since collection")
		}
		merged, changed := mergeBindings(current.Bindings, bindings)
		if !changed {
			return nil
		}
		updated, err := persistPreference(current, map[string]string{"radar-source-refs": encodeBindings(merged)})
		if err != nil {
			return err
		}
		result := observationsFor(vault, updated)[0]
		observation = &result
		return nil
	})
	return observation, err
}

// Preference/binding writes must not trigger archive/restore: muting is not
// completion, and unmuting a completed task must leave it completed in place.
func persistPreference(current note, updates map[string]string) (note, error) {
	if len(updates) == 0 {
		return current, nil
	}
	content, updated, err := updateNoteContent(current, updates)
	if err != nil || content == current.content {
		return current, err
	}
	info, err := os.Lstat(current.Path)
	if err != nil {
		return note{}, err
	}
	if !info.Mode().IsRegular() {
		return note{}, fmt.Errorf("task note must be a regular file")
	}
	if err := atomicWrite(current.Path, []byte(content), info.Mode().Perm()); err != nil {
		return note{}, err
	}
	return updated, nil
}

func taskBindings(task protocol.Task, requireIdentity bool) ([]protocol.SourceBinding, error) {
	bindings := make([]protocol.SourceBinding, 0)
	byKey := map[string]protocol.SourceBinding{}
	for _, ref := range task.SourceRefs {
		if ref.Role != protocol.SourceRefRoleAuthoritative || ref.Authored || (ref.Source == "obsidian" && ref.Kind == "task" && ref.Authority == protocol.SourceRefAuthorityPrimary) {
			continue
		}
		if ref.BindingError != "" {
			if requireIdentity {
				return nil, fmt.Errorf("cannot bind authoritative %s %s %q: %s", ref.Source, ref.Kind, ref.ID, ref.BindingError)
			}
			continue
		}
		binding := ref.Binding()
		if previous, exists := byKey[binding.LinkingKey()]; exists {
			if previous != binding {
				return nil, fmt.Errorf("conflicting authoritative identity %s", binding.LinkingKey())
			}
			continue
		}
		byKey[binding.LinkingKey()] = binding
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].LinkingKey() < bindings[j].LinkingKey() })
	// Use the same validation for provider-supplied and on-disk identities.
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(encodeBindings(bindings), "\n")), &node); err != nil {
		return nil, err
	}
	if _, err := parseBindings(node.Content[0]); err != nil {
		return nil, err
	}
	return bindings, nil
}

func mergeBindings(existing, incoming []protocol.SourceBinding) ([]protocol.SourceBinding, bool) {
	merged := append([]protocol.SourceBinding(nil), existing...)
	byKey := map[string]int{}
	for i, binding := range merged {
		byKey[binding.LinkingKey()] = i
	}
	changed := false
	for _, binding := range incoming {
		if index, exists := byKey[binding.LinkingKey()]; exists {
			// A lifetime key is the identity; retain its original locator even if
			// the provider's display-derived ID changes. Never coerce ID into Key.
			if binding.WorkItem && !merged[index].WorkItem {
				merged[index].WorkItem = true
				changed = true
			}
			continue
		}
		byKey[binding.LinkingKey()] = len(merged)
		merged = append(merged, binding)
		changed = true
	}
	return merged, changed
}

func encodeBindings(bindings []protocol.SourceBinding) string {
	if len(bindings) == 0 {
		return "[]"
	}
	data, _ := yaml.Marshal(bindings) // SourceBinding contains only scalar fields.
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return "\n" + strings.Join(lines, "\n")
}

func associatedNote(vault string, task protocol.Task, bindings []protocol.SourceBinding) (*note, error) {
	items, err := discover(vault)
	if err != nil {
		return nil, err
	}
	validateNoteOwnership(items)
	wantedIDs := map[string]bool{}
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative && (ref.Authored || (ref.Source == "obsidian" && ref.Kind == "task" && ref.Authority == protocol.SourceRefAuthorityPrimary)) {
			id := strings.TrimPrefix(ref.ID, "obsidian:task:")
			if ref.Source != "obsidian" || ref.Kind != "task" || ref.Authority != protocol.SourceRefAuthorityPrimary || id == ref.ID || !validID.MatchString(id) || (ref.Metadata["radar_id"] != "" && ref.Metadata["radar_id"] != id) {
				return nil, fmt.Errorf("task has an invalid or unsupported authored identity %q", ref.ID)
			}
			wantedIDs[id] = true
		}
	}
	if len(wantedIDs) > 1 {
		return nil, fmt.Errorf("task is associated with multiple authored notes")
	}
	keys := map[string]bool{}
	for _, binding := range bindings {
		keys[binding.LinkingKey()] = true
	}
	var associated *note
	for _, item := range items {
		// An unreadable/malformed note may own a requested binding. Do not
		// create or choose another note when ownership cannot be established.
		if item.err != nil {
			return nil, fmt.Errorf("cannot safely determine task note ownership: %s: %w", item.path, item.err)
		}
		matches := wantedIDs[item.note.ID]
		for _, binding := range item.note.Bindings {
			matches = matches || keys[binding.LinkingKey()]
		}
		if !matches {
			continue
		}
		if associated != nil {
			return nil, fmt.Errorf("task is associated with multiple authored notes: %s and %s", associated.Path, item.path)
		}
		current := item.note
		associated = &current
	}
	if len(wantedIDs) > 0 && (associated == nil || !wantedIDs[associated.ID]) {
		return nil, fmt.Errorf("associated Obsidian task note is missing or conflicts with binding ownership")
	}
	return associated, nil
}

func validateNoteOwnership(items []discoveredNote) {
	byID := map[string][]int{}
	byTitle := map[string][]int{}
	byBinding := map[string][]int{}
	for i, item := range items {
		if item.err != nil {
			continue
		}
		byID[item.note.ID] = append(byID[item.note.ID], i)
		byTitle[item.note.Title] = append(byTitle[item.note.Title], i)
		for _, binding := range item.note.Bindings {
			byBinding[binding.LinkingKey()] = append(byBinding[binding.LinkingKey()], i)
		}
	}
	for id, indexes := range byID {
		markDuplicates(items, indexes, "duplicate radar-id "+id)
	}
	for title, indexes := range byTitle {
		markDuplicates(items, indexes, fmt.Sprintf("duplicate task title %q", title))
	}
	for key, indexes := range byBinding {
		markDuplicates(items, indexes, "conflicting source binding ownership "+key)
	}
}

var _ integration.TaskMuteProvider = Source{}
var _ integration.TaskBindingProvider = Source{}
