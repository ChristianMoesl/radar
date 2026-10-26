package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
	"radar/internal/app"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type commandCloner struct {
	plan    integration.RepositoryClonePlan
	applied int
}

func (*commandCloner) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "fake"}
}
func (p *commandCloner) PreviewRepositoryClone(context.Context, integration.RepositoryCloneRequest) (integration.RepositoryClonePlan, error) {
	return p.plan, nil
}
func (p *commandCloner) ApplyRepositoryClone(_ context.Context, _ integration.RepositoryCloneRequest, expected string) (integration.RepositoryCloneResult, error) {
	p.applied++
	if expected != p.plan.PlanID {
		return integration.RepositoryCloneResult{}, fmt.Errorf("plan drift")
	}
	return integration.RepositoryCloneResult{OK: true, Status: "cloned", Repository: p.plan.Repository}, nil
}

func TestCloneCommandRequiresExplicitConsent(t *testing.T) {
	for _, tc := range []struct {
		name             string
		preview, machine bool
		expected, input  string
		applied          int
		wantErr          bool
	}{
		{"preview", true, true, "", "", 0, false},
		{"headless", false, true, "", "", 0, true},
		{"declined", false, false, "", "n\n", 0, false},
		{"no-input", false, false, "", "", 0, false},
		{"human-approved", false, false, "", "y\n", 1, false},
		{"adapter-approved", false, true, "accepted", "", 1, false},
		{"stale-plan", false, true, "stale", "", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &commandCloner{plan: integration.RepositoryClonePlan{PlanID: "accepted", RequiresConfirmation: true, Status: "clone", Remote: "https://github.com/acme/app.git", RepositoryDir: "/sources", Destination: "/sources/app", Repository: "/sources/app"}}
			var out bytes.Buffer
			_, err := cloneRepositoryCommand(context.Background(), p, integration.RepositoryCloneRequest{}, tc.preview, tc.expected, tc.machine, strings.NewReader(tc.input), &out)
			if (err != nil) != tc.wantErr || p.applied != tc.applied {
				t.Fatalf("applied=%d err=%v output=%q", p.applied, err, out.String())
			}
			if !tc.preview && !tc.machine {
				for _, value := range []string{p.plan.Remote, p.plan.RepositoryDir, p.plan.Destination} {
					if !strings.Contains(out.String(), value) {
						t.Fatalf("missing %s in %s", value, out.String())
					}
				}
			}
		})
	}
}

func TestOpenCommandConfirmationAndRederivation(t *testing.T) {
	plan := integration.ResourceOpenPlan{PlanID: "accepted", WorkspaceID: "one", TaskID: 1, RequiresConfirmation: true, Resources: []integration.OpenResource{{ID: "changes", Label: "Changes", URL: "https://github.com/acme/app/pull/123/files"}}}
	for _, tc := range []struct {
		name                    string
		preview, machine, drift bool
		expected, input         string
		launches                int
		wantErr                 bool
	}{
		{"preview", true, true, false, "", "", 0, false},
		{"headless", false, true, false, "", "", 0, true},
		{"declined", false, false, false, "", "n\n", 0, false},
		{"approved", false, false, false, "", "y\n", 1, false},
		{"adapter", false, true, false, "accepted", "", 1, false},
		{"stale-adapter", false, true, false, "stale", "", 0, true},
		{"human-plan-changed", false, false, true, "", "y\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepares, launches := 0, 0
			prepare := func() (integration.ResourceOpenPlan, error) {
				prepares++
				fresh := plan
				if tc.drift && prepares > 1 {
					fresh.PlanID = "changed"
				}
				return fresh, nil
			}
			var out bytes.Buffer
			_, err := openResourceCommand(context.Background(), prepare, tc.preview, tc.expected, tc.machine, strings.NewReader(tc.input), &out, func(context.Context, string) error { launches++; return nil })
			if (err != nil) != tc.wantErr || launches != tc.launches {
				t.Fatalf("launches=%d err=%v output=%s", launches, err, out.String())
			}
			if !tc.preview && !tc.machine && !strings.Contains(out.String(), plan.Resources[0].URL) {
				t.Fatal("confirmation omitted destination")
			}
		})
	}
}

func TestHostResourceRequestSchemasRejectUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"repository":"https://github.com/acme/app.git","destination":"/sources/app","approved":true}`, `{"repository":"x","Workspace":"/other"}`, `{"repository":"x"} {}`, `null {}`} {
		var request integration.RepositoryCloneRequest
		if err := decodeHostResourceRequest(raw, &request); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"resource_ids":["id"],"url":"https://evil.test"}`, `{"resource_ids":["id"],"auto_confirm":true}`, `{"resource_ids":["id"],"executable":"sh"}`} {
		var request integration.ResourceOpenRequest
		if err := decodeHostResourceRequest(raw, &request); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func resourceCacheFixture(t *testing.T, tasks []protocol.Task) (string, func() []string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "radar-resource-cache-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var methods []string
	done := make(chan struct{})
	t.Cleanup(func() { _ = listener.Close(); <-done })
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req protocol.Request
			if err := json.NewDecoder(conn).Decode(&req); err == nil {
				mu.Lock()
				methods = append(methods, req.Method)
				mu.Unlock()
				_ = json.NewEncoder(conn).Encode(protocol.Response{OK: true, Tasks: tasks})
			}
			_ = conn.Close()
		}
	}()
	return path, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), methods...) }
}

func TestOpenResourceCLIUsesAssociatedCacheAndFixedOSOpener(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "radar")
	managed := filepath.Join(root, "workspaces")
	anchor := filepath.Join(managed, "task")
	sources, bin := filepath.Join(root, "sources"), filepath.Join(root, "bin")
	for _, path := range []string{configDir, anchor, sources, bin} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := yaml.Marshal(map[string]any{"workspace": map[string]any{"root_dir": managed, "auto_confirm": true}, "repository_dirs": []string{sources}, "sbx": map[string]bool{"enabled": false}, "linking_mark_prefixes": []string{"ABC"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "task", Path: anchor, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Save(managed, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	task := protocol.Task{ID: 71, SourceRefs: []protocol.SourceRef{
		{ID: "workspace:" + group.ID, ProvidesWorkspace: true, WorkspaceID: group.ID},
		{ID: "github:pr:acme/app:123", Source: "github", Kind: "pull_request", Repo: "acme/app"},
	}}
	socket, methods := resourceCacheFixture(t, []protocol.Task{task})
	log := filepath.Join(root, "opens")
	// PATH contains neither Git nor gh; browser/HTTP commands are never real.
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$RADAR_OPEN_LOG\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "fd"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"XDG_CONFIG_HOME=" + filepath.Dir(configDir), "RADAR_SOCKET=" + socket, "RADAR_OPEN_LOG=" + log, "PATH=" + bin}
	request := `{"resource_ids":["github:open:pr:acme/app:123:changes"]}`
	base := []string{"open-resource", "--workspace", anchor, "--request", request, "--json"}
	stdout, stderr, code := outputCLI(t, append(append([]string{}, base...), "--preview"), "", env...)
	if code != 0 {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	var plan integration.ResourceOpenPlan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresConfirmation || len(plan.Resources) != 1 || plan.Resources[0].URL != "https://github.com/acme/app/pull/123/files" {
		t.Fatalf("%#v", plan)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("preview launched browser")
	}
	_, stderr, code = outputCLI(t, base, "", env...)
	if code == 0 || !strings.Contains(stderr, "explicit confirmation per batch") {
		t.Fatalf("%d %s", code, stderr)
	}
	stdout, stderr, code = outputCLI(t, append(append([]string{}, base...), "--plan", plan.PlanID), "", env...)
	if code != 0 || !strings.Contains(stdout, "launch_requested") {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	launches, err := os.ReadFile(log)
	if err != nil || string(launches) != "https://github.com/acme/app/pull/123/files\n" {
		t.Fatalf("%q %v", launches, err)
	}
	unknown := []string{"open-resource", "--workspace", anchor, "--request", `{"resource_ids":["https://evil.test/"]}`, "--preview", "--json"}
	if _, _, code := outputCLI(t, unknown, "", env...); code == 0 {
		t.Fatal("opened arbitrary URL")
	}
	stdout, stderr, code = outputCLI(t, []string{"workspace-context", "--workspace", anchor, "--json"}, "", env...)
	if code != 0 || !strings.Contains(stdout, `"openable_resources":[`) || !strings.Contains(stdout, "/pull/123/files") || !strings.Contains(stdout, `"repository_dirs":[`) {
		t.Fatalf("%d %s %s", code, stdout, stderr)
	}
	for _, method := range methods() {
		if method != "tasks" {
			t.Fatalf("unexpected daemon mutation/network refresh: %s", method)
		}
	}
}

func TestOpeningCacheUnavailablePreservesOtherWorkspaceContext(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "absent"))
	result := integration.WorkspaceContext{Registered: true, WorkspaceID: "one", Repositories: []integration.WorkspaceContextRepository{{Path: "/sources/app"}}}
	populateOpeningResources(context.Background(), app.DefaultIntegrations(), &result)
	if result.ResourceDiscoveryError == "" || result.OpenableResources == nil || len(result.OpenableResources) != 0 || result.Repositories[0].Path != "/sources/app" {
		t.Fatalf("%#v", result)
	}
}
