package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

// These snapshots were captured by the unoptimized 21e4af9 implementation.
// Normalize only fixture-dependent paths, IDs, and revisions. Revision behavior
// and command budgets are checked independently below.
func contextContractCases() []string {
	return []string{"healthy", "ports", "absent", "list-error", "ports-error", "unowned", "malformed"}
}
func contextContractRunner(r *inspectionProbeRunner, kind string) {
	switch kind {
	case "ports":
		r.ports = `[{"host_port":8080,"sandbox_port":80,"host_ip":"0.0.0.0","protocol":"tcp"}]`
	case "absent":
		r.listOutput = "[]"
	case "list-error":
		r.listErr = errors.New("provider unavailable")
	case "ports-error":
		r.portsErr = errors.New("ports unavailable")
	case "unowned":
		r.listOutput = `[{"name":"fixture","workspaces":["/unrelated"]}]`
	case "malformed":
		r.listOutput = "not json"
	}
}
func normalizedContext(result integration.WorkspaceContext, root string) string {
	data, _ := json.Marshal(result)
	// This order matters: strip path-dependent digests before normalizing paths.
	normalized := strings.ReplaceAll(string(data), result.Revision, "<revision>")
	normalized = strings.ReplaceAll(normalized, result.WorkspaceID, "<workspace-id>")
	fixtureRoot := filepath.Dir(root)
	if physical, err := filepath.EvalSymlinks(fixtureRoot); err == nil {
		normalized = strings.ReplaceAll(normalized, physical, "/fixture")
	}
	normalized = strings.ReplaceAll(normalized, fixtureRoot, "/fixture")
	var value any
	_ = json.Unmarshal([]byte(normalized), &value)
	pretty, _ := json.MarshalIndent(value, "", "  ")
	return string(pretty) + "\n"
}
func TestWorkspaceContextMatchesLegacyResults(t *testing.T) {
	for _, kind := range contextContractCases() {
		t.Run(kind, func(t *testing.T) {
			root, group, runner := inspectionFixture(t)
			contextContractRunner(runner, kind)
			result, err := InspectWorkspace(context.Background(), runner, group.Members[0].Path, root)
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{
				"absent":      "1c9fc4490fbc417ff112beaeae8098101c9d0b94dc57827701d1fb6b9c3dbc39",
				"healthy":     "82065084f3fa1521de49bd105acf48904da292b8054aeffa5d71fbb92c035793",
				"list-error":  "f076b3d34edcd59d2b2af2c7ab5b4aff27ce356fecae2baf13d401874c999f5a",
				"malformed":   "f9dc682983781b7bcd8e41795c29f545679ae252d3976981096d01f759314d1d",
				"ports":       "a81c03f497d7ebba9e6927708ffcd484080be4bf818f05b0f203d0e5bddb03ae",
				"ports-error": "246a958abd7cd3f1b375602789662ed7cbc698fab8df1a8ccfa10a06ec0df24e",
				"unowned":     "093eb7cd25971a87c12cf99ba912e8f82bc34434ed0aa9be47a6d9efa4973a2d",
			}[kind]
			if got := fmt.Sprintf("%x", sha256.Sum256([]byte(normalizedContext(result, root)))); got != expected {
				t.Fatalf("context changed from legacy result (%s):\n%s", got, normalizedContext(result, root))
			}
			if runner.calls["sbx ls"] != 1 {
				t.Fatalf("sandbox list calls: %v", runner.calls)
			}
			wantPorts := 0
			if kind == "healthy" || kind == "ports" || kind == "ports-error" {
				wantPorts = 1
			}
			if runner.calls["sbx ports"] != wantPorts {
				t.Fatalf("port reads: %v", runner.calls)
			}
		})
	}
}
func TestWorkspaceContextObservesPortsAgainAndPreservesOrder(t *testing.T) {
	root, group, runner := inspectionFixture(t)
	first, err := InspectWorkspace(context.Background(), runner, group.Path, root)
	if err != nil {
		t.Fatal(err)
	}
	contextContractRunner(runner, "ports")
	second, err := InspectWorkspace(context.Background(), runner, group.Path, root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision || len(second.Sandbox.Ports) != 1 {
		t.Fatal("live ports did not change revision/result")
	}
	for range 10 {
		result, err := InspectWorkspace(context.Background(), runner, group.Path, root)
		if err != nil || !reflect.DeepEqual(result, second) {
			t.Fatalf("nondeterministic result: %+v %v", result, err)
		}
	}
	for i, member := range second.Members {
		if member.Path != group.Members[i].Path || member.Dirty != (i == 1) {
			t.Fatalf("member order/status changed: %+v", second.Members)
		}
	}
	if runner.calls["sbx ls"] != 12 {
		t.Fatalf("observation reused across requests: %v", runner.calls)
	}
}
func TestInspectionReadsOverlapWithBoundedConcurrency(t *testing.T) {
	var active, peak atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		inspectReads(40, func(int) {
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			entered <- struct{}{}
			<-release
			active.Add(-1)
		})
		close(done)
	}()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("reads did not overlap")
		}
	}
	close(release)
	// Drain task notifications while the bounded workers finish.
	for {
		select {
		case <-entered:
		case <-done:
			if peak.Load() != 4 {
				t.Fatalf("worker bound: %d", peak.Load())
			}
			return
		case <-time.After(time.Second):
			t.Fatal("workers did not finish")
		}
	}
}

type unavailableProbeRunner struct{ Runner }

func (r unavailableProbeRunner) LookPath(name string) error {
	if name == "sbx" || name == "sbx.exe" {
		return os.ErrNotExist
	}
	return r.Runner.LookPath(name)
}
func TestWorkspaceContextMissingCLIReportsUnavailability(t *testing.T) {
	root, group, runner := inspectionFixture(t)
	result, err := InspectWorkspace(context.Background(), unavailableProbeRunner{runner}, group.Path, root)
	if err != nil || result.Sandbox == nil || !strings.Contains(result.Sandbox.RuntimeError, "unavailable") {
		t.Fatalf("missing CLI treated as absent: %+v %v", result, err)
	}
	if runner.calls["sbx ls"] != 0 || runner.calls["sbx ports"] != 0 {
		t.Fatal("ran unavailable provider")
	}
}

type blockingProbeRunner struct {
	Runner
	command   string
	deadlines []time.Time
}

func (r *blockingProbeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "sbx" && args[0] == r.command {
		deadline, ok := ctx.Deadline()
		if !ok {
			return "", errors.New("unbounded provider read")
		}
		r.deadlines = append(r.deadlines, deadline)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return r.Runner.Run(ctx, cwd, name, args...)
}
func TestWorkspaceContextBoundsProviderReadsAndCancellation(t *testing.T) {
	for _, command := range []string{"ls", "ports"} {
		t.Run(command, func(t *testing.T) {
			root, group, runner := inspectionFixture(t)
			blocking := &blockingProbeRunner{Runner: runner, command: command}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			result, err := InspectWorkspace(ctx, blocking, group.Path, root)
			// Other read-only tasks may also observe cancellation, which remains an error.
			if err == nil && (result.Sandbox == nil || !strings.Contains(result.Sandbox.RuntimeError, "deadline exceeded")) {
				t.Fatalf("cancellation hidden: %+v", result)
			}
			if len(blocking.deadlines) != 1 {
				t.Fatalf("unexpected retries: %v", blocking.deadlines)
			}
		})
	}
}

func TestPlanningUsesOneFreshSandboxObservation(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	root, group, runner := inspectionFixture(t)
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A note-only sandbox plan avoids Git branch planning while exercising the
	// same sandbox observation and mount/readiness ownership code.
	group.Members = nil
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	revision, err = workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := ReconcileWorkspaceRequest{Workspace: group.Path, WorkspaceRoot: root, Revision: revision, prepareOnly: true, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: &DesiredWorkspaceSandbox{Ports: []workspacegroup.SandboxPort{}}}}
	if _, err := PreviewReconcileWorkspace(context.Background(), runner, request); err != nil {
		t.Fatal(err)
	}
	if runner.calls["sbx version"] != 1 || runner.calls["sbx ls"] != 1 || runner.calls["sbx ports"] != 1 {
		t.Fatalf("redundant planning probes: %v", runner.calls)
	}
	runner.listOutput = `[{"name":"fixture","workspaces":["/changed-owner"]}]`
	if _, err := ApplyReconcileWorkspace(context.Background(), runner, nil, request); err == nil || !strings.Contains(err.Error(), "unrelated runtime") {
		t.Fatalf("apply reused preview ownership: %v", err)
	}
	if runner.calls["sbx ls"] != 2 {
		t.Fatalf("apply did not reobserve: %v", runner.calls)
	}
}

type memberErrorRunner struct {
	Runner
	first, second       string
	firstErr, secondErr error
	secondFinished      chan struct{}
}

func (r memberErrorRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "git" && len(args) > 1 && args[0] == "-C" {
		switch args[1] {
		case r.first:
			<-r.secondFinished
			return "", r.firstErr
		case r.second:
			close(r.secondFinished)
			return "", r.secondErr
		}
	}
	return r.Runner.Run(ctx, cwd, name, args...)
}
func TestInspectionMemberErrorsKeepRegistryOrder(t *testing.T) {
	root, group, base := inspectionFixture(t)
	firstErr, secondErr := errors.New("first member error"), errors.New("second member error")
	runner := memberErrorRunner{Runner: base, first: group.Members[0].Path, second: group.Members[1].Path, firstErr: firstErr, secondErr: secondErr, secondFinished: make(chan struct{})}
	_, err := InspectWorkspace(context.Background(), runner, group.Path, root)
	if !errors.Is(err, firstErr) {
		t.Fatalf("worker completion order changed error precedence: %v", err)
	}
}

func TestContextContractNormalizationHandlesPhysicalFixtureAlias(t *testing.T) {
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	result := integration.WorkspaceContext{Revision: "revision-value", WorkspaceID: "workspace-value", WorkspacePath: filepath.Join(alias, "workspaces"), RepositoryDirs: []string{filepath.Join(physical, "sources")}}
	normalized := normalizedContext(result, filepath.Join(alias, "workspaces"))
	if strings.Contains(normalized, physical) || strings.Contains(normalized, alias) || !strings.Contains(normalized, `"/fixture/sources"`) {
		t.Fatalf("fixture alias leaked into the contract digest: %s", normalized)
	}
}
