package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/obsidian/settings"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func muteTask(title string) protocol.Task {
	return protocol.Task{ID: 17, Title: title, Attention: "in_progress", SourceRefs: []protocol.SourceRef{
		{ID: "jira:issue:ABC-123", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, Signal: "in_progress"},
		{ID: "github:pr:acme/app:7", Source: "github", Kind: "pr", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, Signal: "done"},
		{ID: "tmux:session:review", BindingKey: "tmux:$7:2026-08-01T10:00:00Z", Source: "tmux", Kind: "session", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Authority: protocol.SourceRefAuthorityNone},
		{ID: "jira:issue:ABC-999", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing},
	}}
}

func TestMuteAdoptsSourceOnlyTaskAtomicallyAndRetriesWithoutWorkspace(t *testing.T) {
	vault := testVault(t)
	source := NewSourceAt(vault)
	ctx := context.Background()
	task := muteTask("Review change")
	identity, err := source.SetMuted(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	if ref.ID != identity.SourceRefID || !ref.Muted || ref.Status != "open" || ref.ProvidesWorkspace || ref.Path != "" {
		t.Fatalf("adopted ref = %+v", ref)
	}
	if len(ref.Bindings) != 3 {
		t.Fatalf("bindings = %+v", ref.Bindings)
	}
	for _, binding := range ref.Bindings {
		if !containsString(ref.LinkingKeys, binding.LinkingKey()) {
			t.Fatalf("missing linking key for %+v: %+v", binding, ref.LinkingKeys)
		}
		if binding.Source == "jira" && !binding.WorkItem {
			t.Fatal("work-item authority lost")
		}
		if binding.Source == "tmux" && (binding.WorkItem || binding.ID != task.SourceRefs[2].ID || binding.Key != task.SourceRefs[2].BindingKey) {
			t.Fatalf("runtime lifetime identity was coerced: %+v", binding)
		}
	}
	current, err := readNote(ref.Metadata["note_path"])
	if err != nil || !strings.HasSuffix(current.content, "---\n") || strings.Contains(current.content, "in_progress") || strings.Contains(current.content, "ABC-999") || strings.Contains(current.content, "task_id") {
		t.Fatalf("created note = %+v, err=%v", current, err)
	}
	before, err := os.Stat(current.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Emulate failed post-write collection: retry with the original source-only
	// task, including a new numeric cache ID, rather than its authoring ref.
	task.ID = 991
	again, err := source.SetMuted(ctx, task, true)
	if err != nil || again != identity {
		t.Fatalf("retry = %+v, err=%v", again, err)
	}
	after, err := os.Stat(current.Path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("idempotent retry rewrote note")
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".radar-notes.lock" {
		t.Fatalf("mute provisioned resources: %+v, %v", entries, err)
	}
	if _, err := os.Lstat(workspacegroup.Path(root)); !os.IsNotExist(err) {
		t.Fatalf("mute wrote registry: %v", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("mute created workspace: %+v, %v", registry, err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestMuteExistingNotePreservesAuthoredFieldsBytesAndPermissions(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "User title"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	path := ref.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), "radar-priority: normal", "radar-priority: urgent # do not normalize", 1)
	content = strings.Replace(content, "radar-completed-at:\n---", "radar-completed-at:\nradar-completion-baseline: pending\ncustom:\n  radar-state: unrelated\n  text: |-\n    untouched\n# user separator\n---", 1) + "\nUser notes.\r\nNo final newline"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	task := muteTask("Source title must not replace user title")
	task.SourceRefs = append(task.SourceRefs, ref)
	identity, err := source.SetMuted(ctx, task, true)
	if err != nil || identity.SourceRefID != ref.ID {
		t.Fatalf("mute = %+v, %v", identity, err)
	}
	updated, err := readNote(path)
	if err != nil {
		t.Fatal(err)
	}
	withoutManaged, _, err := stripMuteFields(updated.content)
	if err != nil || withoutManaged != content {
		t.Fatalf("unrelated bytes changed:\n%s\nerr=%v", updated.content, err)
	}
	if updated.Title != "User title" || updated.Priority != "urgent" || updated.State != "open" || updated.CompletionBaseline != "pending" {
		t.Fatalf("authored fields changed: %+v", updated)
	}
	if _, err := source.SetMuted(ctx, task, false); err != nil {
		t.Fatal(err)
	}
	unmuted, err := readNote(path)
	if err != nil || unmuted.Muted || !reflect.DeepEqual(unmuted.Bindings, updated.Bindings) {
		t.Fatalf("unmute detached task: %+v, %v", unmuted, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions = %+v, %v", info, err)
	}
}

func stripMuteFields(content string) (string, note, error) {
	current, err := parseNote(content)
	if err != nil {
		return "", current, err
	}
	// Remove in reverse byte order; all other bytes must equal the fixture.
	for {
		field := ""
		span := fieldRange{}
		for _, key := range []string{"radar-muted", "radar-source-refs"} {
			if candidate, exists := current.fields[key]; exists && (field == "" || candidate.start > span.start) {
				field, span = key, candidate
			}
		}
		if field == "" {
			break
		}
		content = content[:span.start] + content[span.end:]
		delete(current.fields, field)
	}
	return content, current, nil
}

func TestUnmuteNoopNeverCreatesNoteOrRewritesExisting(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	identity, err := source.SetMuted(ctx, muteTask("Never muted"), false)
	if err != nil || identity.SourceRefID != "" {
		t.Fatalf("unmute no-op = %+v, %v", identity, err)
	}
	result := source.Collect(ctx, integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 0 {
		t.Fatalf("unmute created note: %+v", result)
	}
	if _, err := source.Create(ctx, "Authored"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	before, err := os.Stat(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	identity, err = source.SetMuted(ctx, protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, false)
	if err != nil || identity.SourceRefID != ref.ID {
		t.Fatalf("authored unmute = %+v, %v", identity, err)
	}
	after, err := os.Stat(ref.Metadata["note_path"])
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("unmute no-op rewrote ordinary note")
	}
	current, err := readNote(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	if _, adopted := current.fields["radar-source-refs"]; adopted {
		t.Fatal("unmute no-op adopted ordinary note")
	}
}

func TestMuteRetriesReuseRenamedAndArchivedNotes(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "renamed", true: "archived"}[archived], func(t *testing.T) {
			source := NewSourceAt(testVault(t))
			ctx := context.Background()
			task := muteTask("Original")
			identity, err := source.SetMuted(ctx, task, true)
			if err != nil {
				t.Fatal(err)
			}
			ref := collectedArchiveRef(t, source)
			oldPath := ref.Metadata["note_path"]
			if archived {
				if _, err := source.SetLifecycle(ctx, ref, "done"); err != nil {
					t.Fatal(err)
				}
				ref = collectedArchiveRef(t, source)
			}
			path := filepath.Join(filepath.Dir(ref.Metadata["note_path"]), "Renamed.md")
			if err := os.Rename(ref.Metadata["note_path"], path); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(data), `radar-title: "Original"`, `radar-title: "User renamed title"`, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			// Both retry shapes are supported: stale authored path and no
			// authoring ref after failed projection/cache reset.
			stale := ref
			stale.Metadata = map[string]string{"note_path": oldPath}
			for _, selected := range []protocol.Task{task, {SourceRefs: append(append([]protocol.SourceRef(nil), task.SourceRefs...), stale)}} {
				again, err := source.SetMuted(ctx, selected, true)
				if err != nil || again != identity {
					t.Fatalf("retry = %+v, %v", again, err)
				}
			}
			ref = collectedArchiveRef(t, source)
			if ref.Metadata["note_path"] != path || ref.Title != "User renamed title" || ref.Status != map[bool]string{false: "open", true: "done"}[archived] {
				t.Fatalf("retry replaced or moved note: %+v", ref)
			}
			if _, err := source.SetMuted(ctx, task, false); err != nil {
				t.Fatal(err)
			}
			if after := collectedArchiveRef(t, source); after.Muted || after.Metadata["note_path"] != path || len(after.Bindings) != 3 {
				t.Fatalf("unmute changed association/path: %+v", after)
			}
		})
	}
}

func TestMuteCannotAdoptUnrelatedSameTitleOrFilename(t *testing.T) {
	for _, titles := range [][2]string{{"Same title", "Same title"}, {"CI/CD", "CI-CD"}} {
		t.Run(titles[0], func(t *testing.T) {
			source := NewSourceAt(testVault(t))
			ctx := context.Background()
			if _, err := source.Create(ctx, titles[0]); err != nil {
				t.Fatal(err)
			}
			ref := collectedArchiveRef(t, source)
			before, err := os.ReadFile(ref.Metadata["note_path"])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.SetMuted(ctx, muteTask(titles[1]), true); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("collision did not fail safely: %v", err)
			}
			after, err := os.ReadFile(ref.Metadata["note_path"])
			if err != nil || string(after) != string(before) || collectedArchiveRef(t, source).Muted {
				t.Fatal("collision modified unrelated note")
			}
		})
	}
}

func TestMuteDoneTaskPreservesLifecycleAndNeverArchives(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "source only", true: "authored private"}[existing], func(t *testing.T) {
			source := NewSourceAt(testVault(t))
			ctx := context.Background()
			task := muteTask("Already completed")
			task.Attention = "done"
			task.DoneAt = "2026-08-01T10:00:00Z"
			var original string
			if existing {
				if _, err := source.Create(ctx, task.Title); err != nil {
					t.Fatal(err)
				}
				ref := collectedArchiveRef(t, source)
				original = ref.Metadata["note_path"]
				data, err := os.ReadFile(original)
				if err != nil {
					t.Fatal(err)
				}
				content := strings.Replace(string(data), "radar-state: open", "radar-state: done", 1)
				content = strings.Replace(content, "radar-completed-at:", "radar-completed-at: "+task.DoneAt+"\nradar-completion-baseline: "+strings.Repeat("a", 64), 1)
				if err := os.WriteFile(original, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				task.SourceRefs = append(task.SourceRefs, ref)
			}
			if _, err := source.SetMuted(ctx, task, true); err != nil {
				t.Fatal(err)
			}
			ref := collectedArchiveRef(t, source)
			if settings.IsArchivedNote(ref.Metadata["note_path"]) || (existing && original != ref.Metadata["note_path"]) || ref.Status != "done" || !ref.Muted {
				t.Fatalf("muted done task moved/reopened: %+v", ref)
			}
			if _, err := source.SetMuted(ctx, task, false); err != nil {
				t.Fatal(err)
			}
			current, err := readNote(ref.Metadata["note_path"])
			if err != nil || current.Muted || current.State != "done" || current.CompletedAt != task.DoneAt || (existing && current.CompletionBaseline != strings.Repeat("a", 64)) {
				t.Fatalf("unmute changed completed lifecycle: %+v, %v", current, err)
			}
		})
	}
}

func TestMutedFlagAndBindingsRemainStickyAcrossLifecycle(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("Sticky preference")
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	originalBindings := append([]protocol.SourceBinding(nil), ref.Bindings...)
	items := []protocol.SourceRef{task.SourceRefs[0], task.SourceRefs[1]}
	// Arms completion while remote work is active, without unmuting it.
	armed, err := source.ReconcileLifecycle(ctx, ref, items)
	if err != nil || armed == nil || !armed.Ref.Muted {
		t.Fatalf("active reconcile = %+v, %v", armed, err)
	}
	items[0].Signal = "done"
	done, err := source.ReconcileLifecycle(ctx, armed.Ref, items)
	if err != nil || done == nil || done.Signal != integration.SignalDone || !done.Ref.Muted {
		t.Fatalf("completion = %+v, %v", done, err)
	}
	items[0].Signal = "in_progress"
	opened, err := source.ReconcileLifecycle(ctx, done.Ref, items)
	if err != nil || opened == nil || opened.Ref.Status != "open" || !opened.Ref.Muted || !reflect.DeepEqual(opened.Ref.Bindings, originalBindings) {
		t.Fatalf("reopening = %+v, %v", opened, err)
	}
}

func TestConcurrentMuteCreatesOneFullyBoundNote(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	task := muteTask("Concurrent")
	identities := make([]integration.AuthoredTaskIdentity, 8)
	errors := make([]error, len(identities))
	var wg sync.WaitGroup
	for i := range identities {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			identities[i], errors[i] = source.SetMuted(ctx, task, true)
		}(i)
	}
	wg.Wait()
	for i := range identities {
		if errors[i] != nil || identities[i] != identities[0] {
			t.Fatalf("concurrent mute %d = %+v, %v", i, identities[i], errors[i])
		}
	}
	ref := collectedArchiveRef(t, source)
	if !ref.Muted || len(ref.Bindings) != 3 {
		t.Fatalf("incomplete adoption: %+v", ref)
	}
}

func TestMuteRejectsCancelledAndMalformedTasksWithoutWriting(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.SetMuted(ctx, muteTask("Cancelled"), true); err == nil {
		t.Fatal("cancelled adoption succeeded")
	}
	for _, task := range []protocol.Task{
		{Title: "No identity"},
		{Title: "Informational", SourceRefs: []protocol.SourceRef{{ID: "jira:issue:ABC-123", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational}}},
		{Title: "Invalid", SourceRefs: []protocol.SourceRef{{ID: "", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative}}},
		{Title: "Invalid done", Attention: "done", DoneAt: "not a timestamp", SourceRefs: muteTask("").SourceRefs},
	} {
		if _, err := source.SetMuted(context.Background(), task, true); err == nil {
			t.Fatalf("unsafe task adopted: %+v", task)
		}
	}
	if result := source.Collect(context.Background(), integration.CollectRequest{}); !result.Complete || len(result.Observations) != 0 {
		t.Fatalf("failed adoption left note: %+v", result)
	}
}

func TestMuteRetryAfterCreationFailureLeavesNoIncompleteNote(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	// An invalid completed-at value fails the complete candidate's validation
	// before any private directory or partially authored note is created.
	task := muteTask("Retry creation")
	task.Attention, task.DoneAt = "done", "invalid"
	if _, err := source.SetMuted(ctx, task, true); err == nil {
		t.Fatal("invalid note creation succeeded")
	}
	entries, err := os.ReadDir(taskRoot(source.vaultPath))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed creation left partial directory: %+v, %v", entries, err)
	}
	task.DoneAt = time.Now().UTC().Format(time.RFC3339)
	if _, err := source.SetMuted(ctx, task, true); err != nil {
		t.Fatal(err)
	}
	if ref := collectedArchiveRef(t, source); !ref.Muted || len(ref.Bindings) != 3 || ref.Status != "done" {
		t.Fatalf("retry was not fully bound: %+v", ref)
	}
}

func TestInformationalAuthoredNoteDoesNotBecomeMuteOwner(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Unrelated authored note"); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	before, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	ref.Role = protocol.SourceRefRoleInformational
	task := muteTask("Source-only independent task")
	task.SourceRefs = append(task.SourceRefs, ref)
	identity, err := source.SetMuted(ctx, task, true)
	if err != nil || identity.SourceRefID == ref.ID {
		t.Fatalf("informational note became owner: %+v, %v", identity, err)
	}
	after, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil || string(after) != string(before) {
		t.Fatal("informational note was mutated")
	}
	result := source.Collect(ctx, integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 2 {
		t.Fatalf("independent task adoption = %+v", result)
	}
}
