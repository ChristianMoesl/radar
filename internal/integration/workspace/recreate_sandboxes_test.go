package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
)

const emptyRecreateSecrets = `{"secrets":[],"custom_secrets":[],"env_only_count":0}`

type recreationRunner struct {
	calls         []call
	live          map[string]listedSandbox
	ports         map[string][]workspacegroup.SandboxPort
	secrets       map[string]string
	policies      map[string]string
	failCreate    string
	failReady     string
	failPorts     string
	secretFailure bool
	beforeRun     func([]string)
}

func (r *recreationRunner) LookPath(string) error { return nil }
func (r *recreationRunner) Run(_ context.Context, cwd, executable string, args ...string) (string, error) {
	r.calls = append(r.calls, call{cwd: cwd, name: executable, args: append([]string(nil), args...)})
	if executable != "sbx" {
		return "", fmt.Errorf("unexpected host command %s", executable)
	}
	if r.beforeRun != nil {
		r.beforeRun(args)
	}
	encode := func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }
	switch args[0] {
	case "ls":
		rows := []listedSandbox{}
		for _, s := range r.live {
			rows = append(rows, s)
		}
		return encode(map[string]any{"sandboxes": rows})
	case "secret":
		if r.secretFailure {
			return "", errors.New("PRIVATE credential provider error")
		}
		if s, ok := r.secrets[args[3]]; ok {
			return s, nil
		}
		return emptyRecreateSecrets, nil
	case "policy":
		if s, ok := r.policies[args[2]]; ok {
			return s, nil
		}
		return `{"rules":[]}`, nil
	case "rm":
		delete(r.live, args[2])
		delete(r.ports, args[2])
		return "", nil
	case "create":
		name := args[2]
		if name == r.failCreate {
			return "", errors.New("invalid sandbox kit")
		}
		index := 3
		for index < len(args) && strings.HasPrefix(args[index], "--") {
			index += 2
		}
		r.live[name] = listedSandbox{ID: "new-" + name, Name: name, Status: "stopped", Agent: args[index], Workspaces: args[index+1:]}
		return "", nil
	case "ports":
		name := args[1]
		if args[2] == "--publish" {
			if name == r.failPorts {
				return "", errors.New("port unavailable")
			}
			var host, container int
			if _, err := fmt.Sscanf(args[3], "%d:%d/tcp4", &host, &container); err != nil {
				return "", err
			}
			r.ports[name] = append(r.ports[name], workspacegroup.SandboxPort{HostPort: host, SandboxPort: container})
			return "", nil
		}
		rows := []map[string]any{}
		for _, p := range r.ports[name] {
			rows = append(rows, map[string]any{"host_ip": "127.0.0.1", "host_port": p.HostPort, "sandbox_port": p.SandboxPort, "protocol": "tcp4"})
		}
		return encode(rows)
	case "exec":
		name := args[1]
		if name == "--workdir" {
			name = args[3]
		}
		s := r.live[name]
		s.Status = "running"
		r.live[name] = s
		if name == r.failReady {
			return "", errors.New("PRIVATE readiness output")
		}
		return "", nil
	case "stop":
		s := r.live[args[1]]
		s.Status = "stopped"
		r.live[args[1]] = s
		return "", nil
	}
	return "", fmt.Errorf("unexpected args %v", args)
}

func recreationFixture(t *testing.T, count int) (integration.SandboxRecreateRequest, []workspacegroup.Workspace, *recreationRunner) {
	t.Helper()
	root := t.TempDir()
	r := &recreationRunner{live: map[string]listedSandbox{}, ports: map[string][]workspacegroup.SandboxPort{}, secrets: map[string]string{}, policies: map[string]string{}}
	groups := []workspacegroup.Workspace{}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("sandbox-%d", i)
		anchor := filepath.Join(root, name)
		if err := os.MkdirAll(anchor, 0755); err != nil {
			t.Fatal(err)
		}
		g := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: name, Path: anchor, Sandbox: &workspacegroup.Sandbox{Name: name, Agent: "shell", Mounts: []string{anchor}}}
		groups = append(groups, g)
		r.live[name] = listedSandbox{ID: "original-" + name, Name: name, Agent: "shell", Status: "running", Workspaces: []string{anchor}}
	}
	saveRecreationGroups(t, root, groups)
	return integration.SandboxRecreateRequest{WorkspaceRoot: root}, groups, r
}

func saveRecreationGroups(t *testing.T, root string, groups []workspacegroup.Workspace) {
	t.Helper()
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: append([]workspacegroup.Workspace(nil), groups...)}); err != nil {
		t.Fatal(err)
	}
}
func recreateFixturePlan(t *testing.T, ctx context.Context, req integration.SandboxRecreateRequest, r *recreationRunner) integration.SandboxRecreateRequest {
	t.Helper()
	plan, err := PreviewRecreateSandboxes(ctx, r, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedPlanID = plan.PlanID
	return req
}
func assertNoRecreationMutation(t *testing.T, r *recreationRunner) {
	t.Helper()
	for _, c := range r.calls {
		if c.args[0] == "rm" || c.args[0] == "create" || c.args[0] == "exec" || c.args[0] == "stop" || (c.args[0] == "ports" && c.args[2] != "--json") {
			t.Fatalf("unexpected mutation: %+v", c)
		}
	}
}

func TestSandboxRecreationPreviewAndSelection(t *testing.T) {
	req, groups, r := recreationFixture(t, 2)
	r.live["unmanaged"] = listedSandbox{ID: "unmanaged", Name: "unmanaged", Status: "running"}
	delete(r.live, "sandbox-1")
	plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
	if err != nil || len(plan.Targets) != 2 || plan.Targets[0].Status != "recreate" || plan.Targets[1].Status != "skipped" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	req.Workspace = filepath.Join(groups[0].Path, "member", "nested")
	selected, err := PreviewRecreateSandboxes(context.Background(), r, req)
	if err != nil || len(selected.Targets) != 1 || selected.Targets[0].WorkspaceID != groups[0].ID {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	assertNoRecreationMutation(t, r)
	req.Workspace = t.TempDir()
	if _, err := PreviewRecreateSandboxes(context.Background(), r, req); err == nil {
		t.Fatal("unregistered path accepted")
	}
}

func TestSandboxRecreationRestoresRecipePortsAndStateWithoutChangingRegistry(t *testing.T) {
	req, groups, r := recreationFixture(t, 2)
	g := &groups[0]
	g.Sandbox.Agent = t.TempDir()
	g.Sandbox.KitPath = t.TempDir()
	g.Sandbox.EnvFile = filepath.Join(t.TempDir(), "env file")
	if err := os.WriteFile(g.Sandbox.EnvFile, []byte("PRIVATE=value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	g.Sandbox.ReadyCommand = []string{"sandbox-startup", "wait", "--timeout", "60"}
	g.Sandbox.Ports = []workspacegroup.SandboxPort{{HostPort: 4001, SandboxPort: 4000}}
	r.ports[g.Sandbox.Name] = append([]workspacegroup.SandboxPort(nil), g.Sandbox.Ports...)
	ro := t.TempDir() + ":ro"
	g.Sandbox.Mounts = append(g.Sandbox.Mounts, ro)
	s := r.live[g.Sandbox.Name]
	s.Workspaces = g.Sandbox.Mounts
	r.live[g.Sandbox.Name] = s
	s = r.live["sandbox-1"]
	s.Status = "stopped"
	r.live[s.Name] = s
	saveRecreationGroups(t, req.WorkspaceRoot, groups)
	before, _ := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
	req = recreateFixturePlan(t, context.Background(), req, r)
	result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
	if err != nil || !result.OK || result.Recreated != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if r.live["sandbox-0"].Status != "running" || r.live["sandbox-1"].Status != "stopped" {
		t.Fatal("runtime states not restored")
	}
	want, err := sandboxCreateArgs(g.Path, g.Sandbox.Name, SandboxKitConfig{Name: g.Sandbox.Agent, Path: g.Sandbox.KitPath}, g.Sandbox.EnvFile, g.Sandbox.Mounts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range r.calls {
		if reflect.DeepEqual(c.args, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("recorded recipe not passed exactly: %v", r.calls)
	}
	if !reflect.DeepEqual(r.ports[g.Sandbox.Name], g.Sandbox.Ports) {
		t.Fatal("ports not restored")
	}
	after, _ := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
	if string(before) != string(after) {
		t.Fatal("registry changed")
	}
}

func TestSandboxRecreationPreflightBlocksUnsafeTargets(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, []workspacegroup.Workspace, *recreationRunner)
		want  string
	}{
		{"scoped service secret", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			r.secrets["sandbox-0"] = `{"secrets":[{}],"custom_secrets":[],"env_only_count":0}`
		}, "sandbox-scoped secrets"},
		{"scoped custom secret", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			r.secrets["sandbox-0"] = `{"secrets":[],"custom_secrets":[{}],"env_only_count":0}`
		}, "sandbox-scoped secrets"},
		{"env-only secret", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			r.secrets["sandbox-0"] = `{"secrets":[],"custom_secrets":[],"env_only_count":1}`
		}, "sandbox-scoped secrets"},
		{"secret error", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) { r.secretFailure = true }, "could not inspect"},
		{"secret schema", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) { r.secrets["sandbox-0"] = `{}` }, "unrecognized"},
		{"local policy", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			r.policies["sandbox-0"] = `{"rules":[{"scope":"sandbox:sandbox-0","editable":true}]}`
		}, "sandbox-specific policies"},
		{"policy schema", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) { r.policies["sandbox-0"] = `{}` }, "unrecognized"},
		{"missing mount", func(t *testing.T, g []workspacegroup.Workspace, _ *recreationRunner) {
			if err := os.Remove(g[0].Path); err != nil {
				t.Fatal(err)
			}
		}, "mount directory"},
		{"missing env-file", func(_ *testing.T, g []workspacegroup.Workspace, _ *recreationRunner) {
			g[0].Sandbox.EnvFile = filepath.Join(g[0].Path, "missing.env")
		}, "env_file"},
		{"missing kit", func(_ *testing.T, g []workspacegroup.Workspace, _ *recreationRunner) {
			g[0].Sandbox.KitPath = filepath.Join(g[0].Path, "missing-kit")
		}, "kit is unavailable"},
		{"mount drift", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			s := r.live["sandbox-0"]
			s.Workspaces = append(s.Workspaces, "/extra")
			r.live[s.Name] = s
		}, "live mounts differ"},
		{"port drift", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			r.ports["sandbox-0"] = []workspacegroup.SandboxPort{{HostPort: 8080, SandboxPort: 8080}}
		}, "live ports differ"},
		{"missing runtime ID", func(_ *testing.T, _ []workspacegroup.Workspace, r *recreationRunner) {
			s := r.live["sandbox-0"]
			s.ID = ""
			r.live[s.Name] = s
		}, "stable runtime identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, g, r := recreationFixture(t, 1)
			test.setup(t, g, r)
			saveRecreationGroups(t, req.WorkspaceRoot, g)
			plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
			if err != nil || len(plan.Targets) != 1 || plan.Targets[0].Status != "blocked" || !strings.Contains(plan.Targets[0].Reason, test.want) {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			b, _ := json.Marshal(plan)
			if strings.Contains(string(b), "PRIVATE") {
				t.Fatal("credential diagnostics leaked")
			}
			req.ExpectedPlanID = plan.PlanID
			result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
			if err != nil || result.OK || result.Failed != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertNoRecreationMutation(t, r)
		})
	}
}

func TestSandboxRecreationAcceptsRegeneratedKitPolicy(t *testing.T) {
	req, _, r := recreationFixture(t, 1)
	r.policies["sandbox-0"] = `{"rules":[{"scope":"global"},{"name":"kit:sandbox-0","scope":"sandbox:sandbox-0","editable":false,"provenance":{"created_via":"provisioned","source":"sandboxes"}}]}`
	plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
	if err != nil || plan.Targets[0].Status != "recreate" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestSandboxRecreationRejectsChangedConfirmation(t *testing.T) {
	for _, change := range []string{"identity", "registry", "new credential"} {
		t.Run(change, func(t *testing.T) {
			req, g, r := recreationFixture(t, 1)
			req = recreateFixturePlan(t, context.Background(), req, r)
			switch change {
			case "identity":
				s := r.live["sandbox-0"]
				s.ID = "replacement"
				r.live[s.Name] = s
			case "registry":
				g[0].Sandbox.ReadyCommand = []string{"different"}
				saveRecreationGroups(t, req.WorkspaceRoot, g)
			case "new credential":
				r.secrets["sandbox-0"] = `{"secrets":[{}],"custom_secrets":[],"env_only_count":0}`
			}
			_, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
			if err == nil || !strings.Contains(err.Error(), "plan changed") {
				t.Fatalf("err=%v", err)
			}
			assertNoRecreationMutation(t, r)
		})
	}
}

func TestSandboxRecreationContinuesAfterFailureAndRetainsReadyFailure(t *testing.T) {
	for _, failure := range []string{"create", "ready", "ports"} {
		t.Run(failure, func(t *testing.T) {
			req, g, r := recreationFixture(t, 2)
			switch failure {
			case "create":
				r.failCreate = "sandbox-0"
			case "ready":
				g[0].Sandbox.ReadyCommand = []string{"wait-ready"}
				r.failReady = "sandbox-0"
			case "ports":
				g[0].Sandbox.Ports = []workspacegroup.SandboxPort{{HostPort: 4001, SandboxPort: 4000}}
				r.ports["sandbox-0"] = g[0].Sandbox.Ports
				r.failPorts = "sandbox-0"
			}
			saveRecreationGroups(t, req.WorkspaceRoot, g)
			req = recreateFixturePlan(t, context.Background(), req, r)
			result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
			if err != nil || result.OK || result.Failed != 1 || result.Recreated != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if strings.Contains(result.Targets[0].Reason, "PRIVATE") {
				t.Fatal("readiness diagnostic leaked")
			}
			if failure != "create" && r.live["sandbox-0"].ID != "new-sandbox-0" {
				t.Fatal("recreated runtime removed after post-create failure")
			}
		})
	}
}

func TestSandboxRecreationRechecksSecretsImmediatelyBeforeRemoval(t *testing.T) {
	req, _, r := recreationFixture(t, 1)
	req = recreateFixturePlan(t, context.Background(), req, r)
	reads := 0
	r.beforeRun = func(args []string) {
		if args[0] == "secret" {
			reads++
			if reads == 2 {
				r.secrets["sandbox-0"] = `{"secrets":[{}],"custom_secrets":[],"env_only_count":0}`
			}
		}
	}
	result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
	if err != nil || result.Failed != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertNoRecreationMutation(t, r)
}

func TestSandboxRecreationEmptyAndCancellation(t *testing.T) {
	req, _, r := recreationFixture(t, 0)
	plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
	if err != nil || len(plan.Targets) != 0 || len(r.calls) != 0 {
		t.Fatalf("plan=%+v calls=%v err=%v", plan, r.calls, err)
	}
	req, _, r = recreationFixture(t, 1)
	req = recreateFixturePlan(t, context.Background(), req, r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := recreateSandboxes(ctx, r, nil, req, sandboxReconcilePolicy{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	assertNoRecreationMutation(t, r)
	if _, err := RecreateSandboxes(context.Background(), r, nil, integration.SandboxRecreateRequest{}); err == nil {
		t.Fatal("unconfirmed operation accepted")
	}
}

func TestSandboxRecreationRejectsAmbiguousOwnerAndUnknownRecipe(t *testing.T) {
	for _, mode := range []string{"owner", "recipe"} {
		t.Run(mode, func(t *testing.T) {
			req, groups, r := recreationFixture(t, 2)
			if mode == "owner" {
				groups[1].Sandbox.Name = groups[0].Sandbox.Name
			} else {
				groups[0].Sandbox.Agent = "custom-resolved-agent"
			}
			saveRecreationGroups(t, req.WorkspaceRoot, groups)
			req.Workspace = groups[0].Path
			plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
			if err != nil || len(plan.Targets) != 1 || plan.Targets[0].Status != "blocked" {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			assertNoRecreationMutation(t, r)
		})
	}
}
