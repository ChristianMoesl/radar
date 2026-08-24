package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"radar/internal/workspacegroup"
)

type WorkspaceContext struct {
	CurrentPath        string                       `json:"current_path"`
	WorkspaceRoot      string                       `json:"workspace_root"`
	WorkspaceID        string                       `json:"workspace_id"`
	WorkspaceName      string                       `json:"workspace_name"`
	WorkspacePath      string                       `json:"workspace_path"`
	Registered         bool                         `json:"registered"`
	EnrollmentRequired bool                         `json:"enrollment_required"`
	SessionName        string                       `json:"session_name,omitempty"`
	Revision           string                       `json:"revision"`
	Capabilities       WorkspaceContextCapabilities `json:"capabilities"`
	Desired            DesiredWorkspaceDescription  `json:"desired"`
	Note               *WorkspaceContextNote        `json:"note,omitempty"`
	Sandbox            *WorkspaceContextSandbox     `json:"sandbox,omitempty"`
	Members            []WorkspaceContextMember     `json:"members"`
	Repositories       []WorkspaceContextRepository `json:"repositories"`
}

type WorkspaceContextCapabilities struct {
	Worktrees        bool `json:"worktrees"`
	Sandbox          bool `json:"sandbox"`
	AdditionalMounts bool `json:"additional_mounts"`
	PortForwarding   bool `json:"port_forwarding"`
}

type WorkspaceContextMember struct {
	Repository       string   `json:"repository"`
	Path             string   `json:"path"`
	Branch           string   `json:"branch"`
	Dirty            bool     `json:"dirty"`
	InstructionFiles []string `json:"instruction_files"`
	SkillPaths       []string `json:"skill_paths"`
}

type WorkspaceContextNote struct {
	Path          string `json:"path"`
	WorkspacePath string `json:"workspace_path"`
}

type WorkspaceContextSandbox struct {
	Name    string                       `json:"name"`
	Agent   string                       `json:"agent"`
	KitPath string                       `json:"kit_path,omitempty"`
	Mounts  []string                     `json:"mounts"`
	Ports   []workspacegroup.SandboxPort `json:"ports"`
}

type WorkspaceContextRepository struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	AlreadyMember bool   `json:"already_member"`
}

type RepositoryRefs struct {
	Repository    string             `json:"repository"`
	DefaultBranch string             `json:"default_branch,omitempty"`
	BaseRefs      []string           `json:"base_refs"`
	Branches      []RepositoryBranch `json:"branches"`
	Warning       string             `json:"warning,omitempty"`
}

type RepositoryBranch struct {
	Name            string   `json:"name"`
	Local           bool     `json:"local"`
	Origin          bool     `json:"origin"`
	CheckedOutPaths []string `json:"checked_out_paths,omitempty"`
}

// InspectWorkspace returns the current logical workspace and repositories Radar
// can discover without changing workspace, Git, tmux, or SBX state.
func InspectWorkspace(ctx context.Context, runner Runner, currentDirectory, workspaceRoot string) (WorkspaceContext, error) {
	root := strings.TrimSpace(workspaceRoot)
	var err error
	if root == "" {
		root, err = DefaultRoot()
		if err != nil {
			return WorkspaceContext{}, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return WorkspaceContext{}, err
	}
	root = filepath.Clean(root)

	registry, err := workspacegroup.Load(root)
	if err != nil {
		return WorkspaceContext{}, err
	}
	current, err := filepath.Abs(strings.TrimSpace(currentDirectory))
	if err != nil {
		return WorkspaceContext{}, err
	}
	current = filepath.Clean(current)
	group, registered := workspacegroup.FindByContainingPath(registry, current)
	if registered {
		current = group.Path
	}
	if !registered {
		current, err = currentGitTopLevel(ctx, runner, current)
		if err != nil {
			return WorkspaceContext{}, fmt.Errorf("current workspace: %w", err)
		}
		group, err = enrollmentPlan(ctx, runner, root, current)
		if err != nil {
			return WorkspaceContext{}, err
		}
	}

	repositories, err := DiscoverRepos(ctx, runner, currentDirectory)
	if err != nil {
		return WorkspaceContext{}, err
	}
	memberRepositories := make(map[string]bool, len(group.Members))
	for _, member := range group.Members {
		memberRepositories[pathKey(member.Repository)] = true
		if !containsRepositoryPath(repositories, member.Repository) {
			repositories = append(repositories, member.Repository)
		}
	}

	ports, _, err := observedSandboxPorts(ctx, runner, group)
	if err != nil {
		return WorkspaceContext{}, err
	}
	revision, err := workspaceRevision(group, ports)
	if err != nil {
		return WorkspaceContext{}, err
	}
	result := WorkspaceContext{
		CurrentPath: current, WorkspaceRoot: root, WorkspaceID: group.ID,
		WorkspaceName: group.Name, WorkspacePath: group.Path, Registered: registered,
		EnrollmentRequired: !registered, SessionName: group.SessionName, Revision: revision,
		Capabilities: WorkspaceContextCapabilities{Worktrees: true, Sandbox: group.Sandbox != nil, AdditionalMounts: group.Sandbox != nil, PortForwarding: group.Sandbox != nil},
		Desired:      DesiredWorkspaceDescription{Worktrees: make([]DesiredWorkspaceWorktree, 0, len(group.Members))},
		Members:      make([]WorkspaceContextMember, 0, len(group.Members)),
		Repositories: make([]WorkspaceContextRepository, 0, len(repositories)),
	}
	if group.NotePath != "" {
		result.Note = &WorkspaceContextNote{Path: group.NotePath, WorkspacePath: filepath.Join(group.Path, "note.md")}
	}
	if group.Sandbox != nil {
		desiredMounts := make([]DesiredSandboxMount, 0, len(group.Sandbox.AdditionalMounts))
		for _, mount := range group.Sandbox.AdditionalMounts {
			readOnly := mount.ReadOnly
			desiredMounts = append(desiredMounts, DesiredSandboxMount{Path: mount.Path, ReadOnly: &readOnly})
		}
		result.Desired.Sandbox = &DesiredWorkspaceSandbox{AdditionalMounts: desiredMounts, Ports: append([]workspacegroup.SandboxPort{}, ports...)}
		result.Sandbox = &WorkspaceContextSandbox{
			Name: group.Sandbox.Name, Agent: group.Sandbox.Agent, KitPath: group.Sandbox.KitPath,
			Mounts: append([]string(nil), group.Sandbox.Mounts...), Ports: append([]workspacegroup.SandboxPort{}, ports...),
		}
	}
	for _, member := range group.Members {
		changeCount, err := worktreeChangeCount(ctx, runner, member.Path)
		if err != nil {
			return WorkspaceContext{}, err
		}
		result.Members = append(result.Members, WorkspaceContextMember{
			Repository: member.Repository, Path: member.Path, Branch: member.Branch, Dirty: changeCount > 0,
			InstructionFiles: memberInstructionFiles(member.Path), SkillPaths: memberSkillPaths(member.Path),
		})
		result.Desired.Worktrees = append(result.Desired.Worktrees, DesiredWorkspaceWorktree{
			Repository: member.Repository, BranchMode: "existing", Branch: member.Branch,
		})
	}
	for _, repository := range repositories {
		result.Repositories = append(result.Repositories, WorkspaceContextRepository{
			Name: filepath.Base(repository), Path: repository, AlreadyMember: memberRepositories[pathKey(repository)],
		})
	}
	return result, nil
}

func memberInstructionFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return []string{}
	}
	for _, wanted := range []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"} {
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(entry.Name(), wanted) {
				return []string{filepath.Join(root, entry.Name())}
			}
		}
	}
	return []string{}
}

func memberSkillPaths(root string) []string {
	paths := make([]string, 0, 2)
	for _, relative := range []string{filepath.Join(".pi", "skills"), filepath.Join(".agents", "skills")} {
		path := filepath.Join(root, relative)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths = append(paths, path)
		}
	}
	return paths
}

// InspectRepositoryRefs attempts to refresh origin, then returns canonical
// branch capabilities from the refs available in the local repository.
func InspectRepositoryRefs(ctx context.Context, runner Runner, repository string) (RepositoryRefs, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return RepositoryRefs{}, fmt.Errorf("repository is required")
	}
	root, err := runner.Run(ctx, repository, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return RepositoryRefs{}, err
	}
	root, err = filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return RepositoryRefs{}, err
	}
	root = filepath.Clean(root)
	fetchErr := FetchBranches(ctx, runner, root)

	output, err := runner.Run(ctx, root, "git", "for-each-ref", "--format=%(refname)\t%(refname:short)\t%(symref)", "refs/heads", "refs/remotes/origin")
	if err != nil {
		return RepositoryRefs{}, err
	}
	branches := map[string]*RepositoryBranch{}
	remoteBases := []string{}
	localBases := []string{}
	defaultBranch := ""
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		refName := fields[0]
		if refName == "refs/remotes/origin/HEAD" {
			if len(fields) > 2 {
				defaultBranch = strings.TrimPrefix(fields[2], "refs/remotes/origin/")
			}
			continue
		}
		name := ""
		isLocal := strings.HasPrefix(refName, "refs/heads/")
		isOrigin := strings.HasPrefix(refName, "refs/remotes/origin/")
		switch {
		case isLocal:
			name = strings.TrimPrefix(refName, "refs/heads/")
			localBases = append(localBases, name)
		case isOrigin:
			name = strings.TrimPrefix(refName, "refs/remotes/origin/")
			remoteBases = append(remoteBases, "origin/"+name)
		default:
			continue
		}
		branch := branches[name]
		if branch == nil {
			branch = &RepositoryBranch{Name: name}
			branches[name] = branch
		}
		branch.Local = branch.Local || isLocal
		branch.Origin = branch.Origin || isOrigin
	}

	worktrees, err := runner.Run(ctx, root, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return RepositoryRefs{}, err
	}
	var worktreePath string
	for _, line := range append(strings.Split(worktrees, "\n"), "") {
		if line == "" {
			worktreePath = ""
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "worktree":
			worktreePath = filepath.Clean(value)
		case "branch":
			name := strings.TrimPrefix(value, "refs/heads/")
			if branch := branches[name]; branch != nil && worktreePath != "" {
				branch.CheckedOutPaths = append(branch.CheckedOutPaths, worktreePath)
			}
		}
	}

	sortBranches(remoteBases)
	sortBranches(localBases)
	result := RepositoryRefs{
		Repository: root, DefaultBranch: defaultBranch,
		BaseRefs: append(remoteBases, localBases...),
		Branches: make([]RepositoryBranch, 0, len(branches)),
	}
	if fetchErr != nil {
		result.Warning = fmt.Sprintf("origin refresh failed; using cached refs: %v", fetchErr)
	}
	for _, branch := range branches {
		sort.Strings(branch.CheckedOutPaths)
		result.Branches = append(result.Branches, *branch)
	}
	sort.Slice(result.Branches, func(i, j int) bool {
		return branchSortKey(result.Branches[i].Name) < branchSortKey(result.Branches[j].Name)
	})
	return result, nil
}

func containsRepositoryPath(paths []string, candidate string) bool {
	for _, path := range paths {
		if pathKey(path) == pathKey(candidate) {
			return true
		}
	}
	return false
}
