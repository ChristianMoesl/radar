package workspace

import (
	"context"
	"fmt"

	obsidiansettings "radar/internal/integration/obsidian/settings"
	sbxclient "radar/internal/integration/sbx/client"
	sbxsettings "radar/internal/integration/sbx/settings"
	"radar/internal/integration/workspace/group"
	"radar/internal/pi"
)

func startWorkspaceRuntime(ctx context.Context, runner Runner, group workspacegroup.Workspace, forkSession string) (bool, bool, error) {
	return startWorkspaceRuntimeWithReadiness(ctx, runner, group, forkSession, false)
}

// Ordinary open/recovery remains sandbox-first. A newly recovered runtime must
// be checked even if reconciliation already checked the previous instance.
func startWorkspaceRuntimeWithReadiness(ctx context.Context, runner Runner, group workspacegroup.Workspace, forkSession string, readinessChecked bool) (bool, bool, error) {
	sessionExists, createSandbox, err := prepareWorkspaceRuntime(ctx, runner, group)
	if err != nil {
		return false, false, err
	}
	createdSandbox, err := prepareWorkspaceSandbox(ctx, runner, group, createSandbox, readinessChecked)
	if err != nil {
		return false, createdSandbox, err
	}
	if !sessionExists {
		if err := startWorkspaceSession(ctx, runner, group, forkSession, false); err != nil {
			return false, createdSandbox, err
		}
	}
	if err := releaseWorkspacePanes(ctx, runner, group.Path, group.SessionName); err != nil {
		return !sessionExists, createdSandbox, err
	}
	return !sessionExists, createdSandbox, nil
}

// Creation stays owned by its caller after switch-client. The first error is
// UI-only: finish provisioning even when switching fails, as the old late
// switch did not prevent resource creation either. Provisioning errors win.
func startNewWorkspaceRuntime(ctx context.Context, runner Runner, group workspacegroup.Workspace, forkSession string, switchClient bool) (started bool, switchErr, err error) {
	sessionExists, createSandbox, err := prepareWorkspaceRuntime(ctx, runner, group)
	if err != nil {
		return false, nil, err
	}
	if sessionExists {
		return false, nil, fmt.Errorf("tmux session %s already exists; cannot verify its early-start sandbox guard; open its registered workspace or choose a different session name", group.SessionName)
	}
	if err := startWorkspaceSession(ctx, runner, group, forkSession, true); err != nil {
		return false, nil, err
	}
	if switchClient {
		_, switchErr = runner.Run(ctx, group.Path, "tmux", "switch-client", "-t", group.SessionName)
		if switchErr == nil {
			// switch-client alone leaves a dashboard popup capturing input.
			// Create keeps this process alive across the resulting PTY hangup.
			if _, closeErr := runner.Run(ctx, group.Path, "tmux", "display-popup", "-C"); closeErr != nil {
				switchErr = fmt.Errorf("close Radar popup after workspace switch: %w", closeErr)
			}
		}
	}
	if _, err := prepareWorkspaceSandbox(ctx, runner, group, createSandbox, false); err != nil {
		return true, switchErr, err
	}
	return true, switchErr, releaseWorkspacePanes(ctx, runner, group.Path, group.SessionName)
}

// Returns existing-session and missing-sandbox state. Keep env-file validation
// before note/shared-directory changes during recovery.
func prepareWorkspaceRuntime(ctx context.Context, runner Runner, group workspacegroup.Workspace) (bool, bool, error) {
	if group.Sandbox != nil {
		if err := sbxsettings.ValidateReadyCommand(group.Sandbox.ReadyCommand); err != nil {
			return false, false, err
		}
		if err := sbxclient.New(runner).RequireManaged(); err != nil {
			return false, false, err
		}
	}
	_, sessionErr := runner.Run(ctx, group.Path, "tmux", "has-session", "-t", group.SessionName)
	if sessionErr != nil {
		if err := validateSessionDependencies(runner, group.Tmux); err != nil {
			return false, false, err
		}
	}
	if group.NotePath == "" {
		return false, false, fmt.Errorf("workspace %s has no canonical note; associate its Obsidian note before opening it", group.Path)
	}
	if err := obsidiansettings.ValidateWorkspaceNote(group.NotePath); err != nil {
		return false, false, err
	}
	createSandbox := false
	if group.Sandbox != nil && group.Sandbox.EnvFile != "" {
		exists, err := sandboxExists(ctx, runner, group.Sandbox.Name)
		if err != nil {
			return false, false, err
		}
		createSandbox = !exists
		if createSandbox {
			if err := validateSandboxEnvFile(group.Sandbox.EnvFile); err != nil {
				return false, false, err
			}
		}
	}
	if err := ensureNoteLink(group.Path, group.NotePath); err != nil {
		return false, false, err
	}
	if err := ensureSharedDirectory(group); err != nil {
		return false, false, err
	}
	// Preserve the original setup/lookup ordering for workspaces without an env-file.
	if group.Sandbox != nil && group.Sandbox.EnvFile == "" {
		exists, err := sandboxExists(ctx, runner, group.Sandbox.Name)
		if err != nil {
			return false, false, err
		}
		createSandbox = !exists
	}

	return sessionErr == nil, createSandbox, nil
}

func prepareWorkspaceSandbox(ctx context.Context, runner Runner, group workspacegroup.Workspace, createSandbox, readinessChecked bool) (bool, error) {
	if createSandbox {
		if _, err := startSandboxWithMounts(ctx, runner, group.Path, group.Sandbox.Name, SandboxKitConfig{Name: group.Sandbox.Agent, Path: group.Sandbox.KitPath}, group.Sandbox.EnvFile, group.Sandbox.Mounts); err != nil {
			return false, err
		}
	}
	if !readinessChecked || createSandbox {
		if err := waitForSandboxReady(ctx, runner, group.Path, group.Sandbox); err != nil {
			return createSandbox, err
		}
	}
	return createSandbox, nil
}

func startWorkspaceSession(ctx context.Context, runner Runner, group workspacegroup.Workspace, forkSession string, early bool) error {
	args := piArgsWithPrompt(taskPiSessionID(group.SessionName, group.TaskLinkingKey), group.SessionName, group.Model, group.Thinking, forkSession, "")
	if early {
		guard, err := pi.RequireSandboxPath()
		if err != nil {
			return fmt.Errorf("prepare required pi-sbx launch guard: %w", err)
		}
		args += " --extension " + shellQuote(guard)
		return createEarlyTmuxWorkspace(ctx, runner, group.Path, group.Path, group.SessionName, group.Tmux, args)
	}
	return createTmuxWorkspace(ctx, runner, group.Path, group.Path, group.SessionName, group.Tmux, args, nil)
}
