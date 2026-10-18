package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"radar/internal/config"
	group "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
	"strings"
)

func agentWorkspace(ctx context.Context, runner Runner, path string, sandboxRequired bool) (protocol.AgentWorkspaceBinding, error) {
	if path == "" {
		return protocol.AgentWorkspaceBinding{}, fmt.Errorf("a registered Radar workspace is required")
	}
	_, registered, found, err := RegisteredWorkspace(path, "")
	if err != nil {
		return protocol.AgentWorkspaceBinding{}, err
	}
	if !found {
		return protocol.AgentWorkspaceBinding{}, fmt.Errorf("workspace is not registered: %s", path)
	}
	if info, err := os.Stat(registered.Path); err != nil || !info.IsDir() {
		return protocol.AgentWorkspaceBinding{}, fmt.Errorf("associated workspace is missing: %s", registered.Path)
	}
	binding := protocol.AgentWorkspaceBinding{ID: registered.ID, Path: registered.Path, Mode: "local"}
	if registered.Sandbox != nil {
		binding.Mode = "sbx"
		binding.Sandbox = registered.Sandbox.Name
		actual, found, err := findSandbox(ctx, runner, registered.Path, registered.Sandbox.Name)
		if err != nil {
			return binding, err
		}
		if !found || !strings.EqualFold(actual.Status, "running") || !sharedDirectoryReady(registered.Sandbox.SharedDirectory, sandboxWorkspaceMounts(actual)) {
			return binding, fmt.Errorf("workspace sandbox %s is unavailable or missing required mounts; repair it before starting the agent", binding.Sandbox)
		}
	} else if sandboxRequired {
		return binding, fmt.Errorf("SBX is enabled but the workspace has no managed sandbox; reconcile the workspace first")
	}
	return binding, nil
}

func (Source) AgentWorkspace(ctx context.Context, path string) (protocol.AgentWorkspaceBinding, error) {
	cfg, err := config.Load()
	if err != nil {
		return protocol.AgentWorkspaceBinding{}, err
	}
	return agentWorkspace(ctx, ExecRunner{}, path, cfg.SBX.WorkspaceEnabled(workspaceGOOS, ExecRunner{}.LookPath))
}

func (s Source) ExecuteWorkspace(ctx context.Context, path, cwd string, argv []string) (string, error) {
	binding, err := s.AgentWorkspace(ctx, path)
	if err != nil {
		return "", err
	}
	if len(argv) == 0 || argv[0] == "" {
		return "", fmt.Errorf("command argv is required")
	}
	if cwd == "" {
		cwd = binding.Path
	}
	_, registered, found, err := RegisteredWorkspace(binding.Path, "")
	if err != nil || !found {
		return "", fmt.Errorf("workspace registration unavailable")
	}
	return executeWorkspace(ctx, ExecRunner{}, binding, registered, cwd, argv)
}

func executeWorkspace(ctx context.Context, runner Runner, binding protocol.AgentWorkspaceBinding, registered group.Workspace, cwd string, argv []string) (string, error) {
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	if !workspaceContains(registered, canonical) {
		return "", fmt.Errorf("working directory is outside the associated workspace")
	}
	release, err := workspacelease.Acquire(binding.Path, false)
	if err != nil {
		return "", err
	}
	defer release()
	if binding.Mode == "sbx" {
		args := append([]string{"exec", "--workdir", canonical, binding.Sandbox}, argv...)
		return runner.Run(ctx, binding.Path, "sbx", args...)
	}
	return runner.Run(ctx, canonical, argv[0], argv[1:]...)
}

func workspaceContains(registered group.Workspace, path string) bool {
	roots := []string{registered.Path}
	for _, member := range registered.Members {
		roots = append(roots, member.Path)
	}
	for _, root := range roots {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(canonical, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
