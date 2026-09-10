package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
)

func TestTitleMetadataEditsPreservePathsIdentityAndUserContent(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	desired, err := source.PrepareWorkspaceNote(ctx, "Original")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(desired.Path)
	if err != nil {
		t.Fatal(err)
	}
	body := "\nUser body: leave it alone.\n"
	content := strings.Replace(string(data), `radar-title: "Original"`, `radar-title: 'Setup: screenshots #1'`, 1)
	content = strings.Replace(content, "radar-completed-at:\n", "radar-completed-at:\ncustom:\n  owner: Example\n  radar-state: unrelated\n", 1) + body
	if err := os.WriteFile(desired.Path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale creation plan must not overwrite an edited title on retry.
	if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
		t.Fatal(err)
	}
	result := source.Collect(ctx, integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	ref := result.Observations[0].Ref
	if ref.Title != "Setup: screenshots #1" || ref.Presentation.WorkspaceName != ref.Title || ref.ID != desired.LinkingKey || ref.WorkspaceAnchorPath != desired.Path {
		t.Fatalf("ref = %+v", ref)
	}
	if _, err := source.SetPriority(ctx, ref, "urgent"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(desired.Path)
	if err != nil || string(after) != strings.Replace(content, "radar-priority: normal", "radar-priority: urgent", 1) {
		t.Fatalf("mutation changed unrelated content: %v", err)
	}
	if _, err := source.Create(ctx, ref.Title); err == nil {
		t.Fatal("duplicate display title accepted after metadata edit")
	}
}

func TestTitleYAMLValidation(t *testing.T) {
	header := "---\nradar-id: 12345678-1234-4234-8234-123456789abc\nradar-state: open\nradar-priority: normal\nradar-created-at: 2026-08-01T10:00:00Z\nradar-completed-at:\n"
	for _, tt := range []struct{ field, want string }{
		{`radar-title: "Setup: screenshots #1"`, "Setup: screenshots #1"},
		{`radar-title: 'It''s a task: yes'`, "It's a task: yes"},
		{`radar-title: A plain title # comment`, "A plain title"},
		{`radar-title: "true"`, "true"},
		{"radar-title: >-\n  Setup: screenshots\n  next step", "Setup: screenshots next step"},
	} {
		current, err := parseNote(header + tt.field + "\n---\n")
		if err != nil || current.Title != tt.want {
			t.Errorf("%q: title=%q, error=%v", tt.field, current.Title, err)
		}
	}
	for _, field := range []string{"", "radar-title:", `radar-title: " "`, "radar-title: true", "radar-title: 123", "radar-title: []", "radar-title: {x: y}", `radar-title: "unfinished`, "radar-title: a\nradar-title: b", "radar-title: a: b"} {
		if _, err := parseNote(header + field + "\n---\n"); err == nil {
			t.Errorf("invalid title accepted: %q", field)
		}
	}
}

func TestMissingTitleDoesNotFallBackToFilenameOrMutate(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	desired, err := source.PrepareWorkspaceNote(ctx, "Plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
		t.Fatal(err)
	}
	initial := source.Collect(ctx, integration.CollectRequest{})
	data, err := os.ReadFile(desired.Path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), "radar-title: \"Plan\"\n", "", 1)
	if err := os.WriteFile(desired.Path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	result := source.Collect(ctx, integration.CollectRequest{Previous: observationsAsTasks(initial.Observations)})
	if result.Complete || result.SourceStatus.Status != "partial" || !strings.Contains(result.SourceStatus.Detail, "missing required field radar-title") || len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	if _, err := source.SetPriority(ctx, initial.Observations[0].Ref, "urgent"); err == nil {
		t.Fatal("unmigrated note mutated")
	}
	after, err := os.ReadFile(desired.Path)
	if err != nil || string(after) != content {
		t.Fatalf("unmigrated content changed: %v", err)
	}
}

func TestPlannedNoteRequiresTitleAndMatchingSafePath(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	desired, err := source.PrepareWorkspaceNote(ctx, "Setup: screenshots")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"", " ", "Other", " Setup: screenshots", string([]byte{0xff})} {
		changed := desired
		changed.Title = title
		if err := source.EnsureWorkspaceNote(ctx, changed); err == nil {
			t.Errorf("invalid plan title accepted: %q", title)
		}
	}
	if _, err := os.Lstat(filepath.Dir(desired.Path)); !os.IsNotExist(err) {
		t.Fatal("invalid plan created directory")
	}
}

func TestArchiveRestoreSanitizesDirectoryNotDisplayTitle(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	title := `Setup: ../screenshots "why?"`
	if _, err := source.Create(ctx, title); err != nil {
		t.Fatal(err)
	}
	ref := collectedArchiveRef(t, source)
	original := ref.Metadata["note_path"]
	if _, err := source.SetLifecycle(ctx, ref, "done"); err != nil {
		t.Fatal(err)
	}
	archived := collectedArchiveRef(t, source)
	if archived.Title != title {
		t.Fatalf("archived title = %q", archived.Title)
	}
	if _, err := source.SetLifecycle(ctx, archived, "open"); err != nil {
		t.Fatal(err)
	}
	reopened := collectedArchiveRef(t, source)
	if reopened.Title != title || reopened.Metadata["note_path"] != original {
		t.Fatalf("reopened = %+v", reopened)
	}
}
