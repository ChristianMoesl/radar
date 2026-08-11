package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/workspacegroup"
)

type introspectionRunner struct {
	current     string
	fdOutput    string
	refsOutput  string
	worktrees   string
	fetchCalled bool
}

func (r *introspectionRunner) LookPath(name string) error {
	if name == "fd" {
		return nil
	}
	return os.ErrNotExist
}

func (r *introspectionRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	if name == "fd" {
		return r.fdOutput, nil
	}
	switch command {
	case "git rev-parse --show-toplevel":
		if r.current != "" {
			return r.current, nil
		}
		return cwd, nil
	case "git fetch --prune origin":
		r.fetchCalled = true
		return "", nil
	case "git for-each-ref --format=%(refname)\t%(refname:short)\t%(symref) refs/heads refs/remotes/origin":
		return r.refsOutput, nil
	case "git worktree list --porcelain":
		return r.worktrees, nil
	default:
		return "", os.ErrNotExist
	}
}

func TestInspectWorkspaceReturnsMembersAndDiscoveredRepositories(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	root := filepath.Join(home, "workspaces")
	sources := filepath.Join(home, "sources")
	primaryRepo := filepath.Join(sources, "primary")
	memberRepo := filepath.Join(sources, "member")
	candidateRepo := filepath.Join(sources, "candidate")
	primaryPath := filepath.Join(root, "primary", "RAD-10-work")
	memberPath := filepath.Join(root, "member", "RAD-10-work")
	for _, path := range []string{root, sources} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config.Config{
		RepositoryDirs:      []string{sources},
		WorkspaceRoot:       root,
		LinkingMarkPrefixes: []string{"RAD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primaryPath), Name: "RAD-10-work", PrimaryPath: primaryPath,
		SessionName: "primary-RAD-10-work",
		Members: []workspacegroup.Member{
			{Repository: primaryRepo, Path: primaryPath, Branch: "RAD-10-work", Primary: true, SetupScheduled: true},
			{Repository: memberRepo, Path: memberPath, Branch: "feature/api", SetupScheduled: true},
		},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	runner := &introspectionRunner{
		current: primaryPath,
		fdOutput: strings.Join([]string{
			filepath.Join(primaryRepo, ".git"),
			filepath.Join(memberRepo, ".git"),
			filepath.Join(candidateRepo, ".git"),
		}, "\n"),
	}

	result, err := InspectWorkspace(context.Background(), runner, filepath.Join(primaryPath, "src"), root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Registered || result.EnrollmentRequired || result.WorkspaceID != group.ID || result.CurrentPath != primaryPath {
		t.Fatalf("workspace context = %+v", result)
	}
	if result.Revision == "" || !result.Capabilities.Worktrees || result.Capabilities.Sandbox || result.Capabilities.AdditionalMounts || result.Capabilities.PortForwarding {
		t.Fatalf("workspace capabilities = %+v, revision = %q", result.Capabilities, result.Revision)
	}
	if result.Desired.Sandbox != nil || len(result.Desired.Worktrees) != 2 {
		t.Fatalf("desired workspace = %+v", result.Desired)
	}
	if len(result.Members) != 2 || len(result.Repositories) != 3 {
		t.Fatalf("workspace context members/repositories = %+v", result)
	}
	membership := map[string]bool{}
	for _, repository := range result.Repositories {
		membership[repository.Path] = repository.AlreadyMember
	}
	wantMembership := map[string]bool{primaryRepo: true, memberRepo: true, candidateRepo: false}
	if !reflect.DeepEqual(membership, wantMembership) {
		t.Fatalf("repository membership = %#v, want %#v", membership, wantMembership)
	}
}

func TestWorkspaceContextEmptySandboxPortsMarshalAsArray(t *testing.T) {
	context := WorkspaceContext{
		Capabilities: WorkspaceContextCapabilities{Worktrees: true, Sandbox: true, AdditionalMounts: true, PortForwarding: true},
		Desired:      DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: &DesiredWorkspaceSandbox{AdditionalMounts: []DesiredSandboxMount{}, Ports: []workspacegroup.SandboxPort{}}},
	}
	data, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"sandbox":{"additional_mounts":[],"ports":[]}`) {
		t.Fatalf("context JSON = %s", data)
	}
}

func TestInspectRepositoryRefsReturnsCanonicalBranchesAndCheckouts(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	featurePath := filepath.Join(t.TempDir(), "feature")
	runner := &introspectionRunner{
		current: repo,
		refsOutput: strings.Join([]string{
			"refs/remotes/origin/HEAD\torigin/HEAD\trefs/remotes/origin/main",
			"refs/remotes/origin/main\torigin/main\t",
			"refs/heads/main\tmain\t",
			"refs/remotes/origin/feature/api\torigin/feature/api\t",
			"refs/heads/feature/api\tfeature/api\t",
		}, "\n"),
		worktrees: strings.Join([]string{
			"worktree " + repo,
			"HEAD aaaaaaa",
			"branch refs/heads/main",
			"",
			"worktree " + featurePath,
			"HEAD bbbbbbb",
			"branch refs/heads/feature/api",
			"",
		}, "\n"),
	}

	result, err := InspectRepositoryRefs(context.Background(), runner, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.fetchCalled {
		t.Fatal("origin was not fetched")
	}
	if result.Repository != repo || result.DefaultBranch != "main" {
		t.Fatalf("repository refs = %+v", result)
	}
	wantBases := []string{"origin/main", "origin/feature/api", "main", "feature/api"}
	if !reflect.DeepEqual(result.BaseRefs, wantBases) {
		t.Fatalf("base refs = %#v, want %#v", result.BaseRefs, wantBases)
	}
	wantBranches := []RepositoryBranch{
		{Name: "main", Local: true, Origin: true, CheckedOutPaths: []string{repo}},
		{Name: "feature/api", Local: true, Origin: true, CheckedOutPaths: []string{featurePath}},
	}
	if !reflect.DeepEqual(result.Branches, wantBranches) {
		t.Fatalf("branches = %#v, want %#v", result.Branches, wantBranches)
	}
}
