package workspace

import (
	"context"
	"fmt"
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
	PrimaryPath        string                       `json:"primary_path"`
	Registered         bool                         `json:"registered"`
	EnrollmentRequired bool                         `json:"enrollment_required"`
	SessionName        string                       `json:"session_name,omitempty"`
	Sandbox            *WorkspaceContextSandbox     `json:"sandbox,omitempty"`
	Members            []WorkspaceContextMember     `json:"members"`
	Repositories       []WorkspaceContextRepository `json:"repositories"`
}

type WorkspaceContextMember struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	Primary    bool   `json:"primary"`
}

type WorkspaceContextSandbox struct {
	Name    string   `json:"name"`
	Agent   string   `json:"agent"`
	KitPath string   `json:"kit_path,omitempty"`
	Mounts  []string `json:"mounts"`
}

type WorkspaceContextRepository struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	AlreadyMember bool   `json:"already_member"`
	MemberPath    string `json:"member_path,omitempty"`
}

type RepositoryRefs struct {
	Repository    string             `json:"repository"`
	DefaultBranch string             `json:"default_branch,omitempty"`
	BaseRefs      []string           `json:"base_refs"`
	Branches      []RepositoryBranch `json:"branches"`
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

	current, err := currentGitTopLevel(ctx, runner, currentDirectory)
	if err != nil {
		return WorkspaceContext{}, fmt.Errorf("current workspace: %w", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return WorkspaceContext{}, err
	}
	group, registered := workspacegroup.FindByMemberPath(registry, current)
	if !registered {
		group, err = enrollmentPlan(ctx, runner, root, current)
		if err != nil {
			return WorkspaceContext{}, err
		}
	}

	repositories, err := DiscoverRepos(ctx, runner, currentDirectory)
	if err != nil {
		return WorkspaceContext{}, err
	}
	memberByRepository := make(map[string]workspacegroup.Member, len(group.Members))
	for _, member := range group.Members {
		memberByRepository[pathKey(member.Repository)] = member
		if !containsRepositoryPath(repositories, member.Repository) {
			repositories = append(repositories, member.Repository)
		}
	}

	result := WorkspaceContext{
		CurrentPath: current, WorkspaceRoot: root, WorkspaceID: group.ID,
		WorkspaceName: group.Name, PrimaryPath: group.PrimaryPath, Registered: registered,
		EnrollmentRequired: !registered, SessionName: group.SessionName,
		Members:      make([]WorkspaceContextMember, 0, len(group.Members)),
		Repositories: make([]WorkspaceContextRepository, 0, len(repositories)),
	}
	if group.Sandbox != nil {
		result.Sandbox = &WorkspaceContextSandbox{
			Name: group.Sandbox.Name, Agent: group.Sandbox.Agent, KitPath: group.Sandbox.KitPath,
			Mounts: append([]string(nil), group.Sandbox.Mounts...),
		}
	}
	for _, member := range group.Members {
		result.Members = append(result.Members, WorkspaceContextMember{
			Repository: member.Repository, Path: member.Path, Branch: member.Branch, Primary: member.Primary,
		})
	}
	for _, repository := range repositories {
		summary := WorkspaceContextRepository{Name: filepath.Base(repository), Path: repository}
		if member, ok := memberByRepository[pathKey(repository)]; ok {
			summary.AlreadyMember = true
			summary.MemberPath = member.Path
		}
		result.Repositories = append(result.Repositories, summary)
	}
	return result, nil
}

// InspectRepositoryRefs refreshes origin using the same behavior as the create
// flow and returns canonical branch capabilities for one repository.
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
	if err := FetchBranches(ctx, runner, root); err != nil {
		return RepositoryRefs{}, err
	}

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
