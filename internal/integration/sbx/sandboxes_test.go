package sbx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

func TestParseSandboxes(t *testing.T) {
	sandboxes, err := parseSandboxes(`{
  "sandboxes": [
    {
      "name": "radar-rb3ca-experience-center-DPSCAP-600-Integrate-generated-links",
      "id": "c048feba-a578-492b-baf0-895b04b6e1b3",
      "agent": "shell",
      "status": "running",
      "workspaces": [
        "/Users/me/radar/rb3ca-experience-center/DPSCAP-600-Integrate-generated-links",
        "/Users/me/.pi/agent",
        "/Users/me/.pi/agent/sessions"
      ]
    }
  ]
}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(sandboxes) != 1 {
		t.Fatalf("sandboxes = %d, want 1", len(sandboxes))
	}
	if sandboxes[0].Name != "radar-rb3ca-experience-center-DPSCAP-600-Integrate-generated-links" {
		t.Fatalf("sandbox name = %q", sandboxes[0].Name)
	}
}

func TestSandboxSourceRef(t *testing.T) {
	s := sandbox{
		Name:   "radar-rb3ca-experience-center-DPSCAP-600-Integrate-generated-links",
		ID:     "c048feba-a578-492b-baf0-895b04b6e1b3",
		Agent:  "shell",
		Status: "running",
		Workspaces: []string{
			"/Users/me/radar/rb3ca-experience-center/DPSCAP-600-Integrate-generated-links",
			"/Users/me/.pi/agent",
			"/Users/me/.pi/agent/sessions",
		},
	}

	ref := s.SourceRef()
	contracttest.AssertValidSourceRefs(t, "sbx", []protocol.SourceRef{ref})
	if ref.ID != "sbx:sandbox:"+s.Name || ref.Source != "sbx" || ref.Kind != "sandbox" {
		t.Fatalf("unexpected source ref identity: %+v", ref)
	}
	if ref.SourceLabel != "Docker sbx" || ref.Title != s.Name || ref.Status != "running" {
		t.Fatalf("unexpected source ref display fields: %+v", ref)
	}
	wantPath := "/Users/me/radar/rb3ca-experience-center/DPSCAP-600-Integrate-generated-links"
	if ref.Path != wantPath {
		t.Fatalf("path = %q, want %q", ref.Path, wantPath)
	}
	if ref.CanonicalKey != "workspace:"+wantPath {
		t.Fatalf("canonical key = %q", ref.CanonicalKey)
	}
	wantKeys := []string{"ticket:DPSCAP-600", "workspace:" + wantPath}
	if !reflect.DeepEqual(ref.LinkingKeys, wantKeys) {
		t.Fatalf("linking keys = %+v, want %+v", ref.LinkingKeys, wantKeys)
	}
	if ref.Metadata["id"] != s.ID || ref.Metadata["agent"] != "shell" || ref.Metadata["workspace_count"] != "3" {
		t.Fatalf("metadata = %+v", ref.Metadata)
	}
}

func TestSourcePreviewCleanupReturnsEverySandboxTarget(t *testing.T) {
	one := sandbox{Name: "radar-repo-one", Workspaces: []string{"/work/repo/one"}}.SourceRef()
	two := sandbox{Name: "radar-repo-two", Workspaces: []string{"/work/repo/two"}}.SourceRef()
	targets, err := Source{}.PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{
		Task: protocol.Task{ID: 7, SourceRefs: []protocol.SourceRef{one, two}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].SandboxName != "radar-repo-one" || targets[1].SandboxName != "radar-repo-two" {
		t.Fatalf("cleanup targets = %+v", targets)
	}
}

func TestCleanupSandboxRemovesNamedSandbox(t *testing.T) {
	runner := &fakeRunner{}
	result, err := cleanupSandbox(context.Background(), runner, protocol.CleanupTarget{SourceRefID: "sbx:sandbox:radar-repo-small-fix", Source: "sbx", Kind: "sandbox", Title: "radar-repo-small-fix", SandboxName: "radar-repo-small-fix", Path: "/work/repo/small-fix"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "sbx" || result.Kind != "sandbox" || result.Title != "radar-repo-small-fix" {
		t.Fatalf("result = %+v", result)
	}
	assertCallContains(t, runner.calls, "sbx", "rm --force radar-repo-small-fix")
	if len(runner.calls) != 1 || runner.calls[0].cwd != "" {
		t.Fatalf("cleanupSandbox() cwd = %+v, want empty cwd", runner.calls)
	}
}

func TestPrimarySandboxWorkspaceSkipsPiAgentMount(t *testing.T) {
	workspace := primarySandboxWorkspace([]string{"/Users/me/.pi/agent", "/Users/me/.pi/agent/sessions", "/repo/worktree"})
	if workspace != "/repo/worktree" {
		t.Fatalf("workspace = %q, want /repo/worktree", workspace)
	}
}

func TestSandboxWithoutWorkspaceUsesSourceRefCanonicalKey(t *testing.T) {
	ref := sandbox{Name: "sandbox-conn-test", ID: "f263b19b"}.SourceRef()
	if ref.CanonicalKey != "sbx:sandbox:sandbox-conn-test" {
		t.Fatalf("canonical key = %q", ref.CanonicalKey)
	}
	if len(ref.LinkingKeys) != 0 {
		t.Fatalf("linking keys = %+v, want none", ref.LinkingKeys)
	}
}

func TestSBXErrorDetailSuggestsLoginForAuthFailure(t *testing.T) {
	err := sbxErrorDetail(errors.New("sbx ls --json failed: Sign-in required"))
	if err != "not signed in; run sbx login" {
		t.Fatalf("sbxErrorDetail() = %q, want login suggestion", err)
	}
}

func TestSourceListsSandboxesOncePerCollection(t *testing.T) {
	tmp := t.TempDir()
	countPath := filepath.Join(tmp, "calls")
	installFakeSBX(t, tmp, fmt.Sprintf("printf 'call\\n' >> %q\nprintf '{\"sandboxes\":[]}\\n'\n", countPath))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := Source{}

	preflight := source.Status(context.Background(), logger)
	if !preflight.CanRun {
		t.Fatalf("preflight status = %+v, want runnable", preflight)
	}
	if _, err := os.Stat(countPath); !os.IsNotExist(err) {
		t.Fatalf("sbx was invoked during preflight: %v", err)
	}

	result := source.Collect(context.Background(), integration.CollectRequest{Logger: logger})
	if result.SourceStatus == nil || result.SourceStatus.Status != "ok" {
		t.Fatalf("collection status = %+v, want ok", result.SourceStatus)
	}
	calls, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(calls), "call\n"); got != 1 {
		t.Fatalf("sbx calls = %d, want 1", got)
	}
}

func TestSBXOutputReportsEmptyStderrFailure(t *testing.T) {
	tmp := t.TempDir()
	installFakeSBX(t, tmp, "exit 1\n")

	_, err := sbxOutput(context.Background(), "ls", "--json")
	if err == nil || !strings.Contains(err.Error(), "sbx ls --json failed: exit status 1") {
		t.Fatalf("sbxOutput() error = %v, want exit status detail", err)
	}
}

func TestSBXOutputReportsTimeout(t *testing.T) {
	tmp := t.TempDir()
	installFakeSBX(t, tmp, "sleep 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := sbxOutput(ctx, "ls", "--json")
	if err == nil || !strings.Contains(err.Error(), "sbx ls --json failed: context deadline exceeded") {
		t.Fatalf("sbxOutput() error = %v, want timeout detail", err)
	}
}

func installFakeSBX(t *testing.T, dir string, body string) {
	t.Helper()
	path := filepath.Join(dir, "sbx")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
