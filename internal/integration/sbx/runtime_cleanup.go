package sbx

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"radar/internal/agentservice"
	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func retireSandbox(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget) (protocol.CleanupTarget, error) {
	return cleanupRegisteredSandbox(ctx, runner, target, false)
}

func cleanupRegisteredSandbox(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget, remove bool) (protocol.CleanupTarget, error) {
	return cleanupRegisteredSandboxAdmitted(ctx, runner, target, remove, false)
}

// Service retains its full deletion bundle. Direct callers acquire the same
// actual owner path and reusable name before releasing the note transaction.
// Neither a runtime observation nor a short metadata callback grants admission.
func cleanupRegisteredSandboxAdmitted(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget, remove, admitted bool) (protocol.CleanupTarget, error) {
	target.Operation = maps.Clone(target.Operation)
	delete(target.Operation, "runtime_retired")
	if target.Source != "sbx" || target.Kind != "sandbox" {
		return target, fmt.Errorf("invalid registered sandbox cleanup target")
	}
	if err := ctx.Err(); err != nil {
		return target, err
	}
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return target, err
	}
	defer releaseOperation()
	root, err := workspace.DefaultRoot()
	if err != nil {
		return target, err
	}
	var owner workspacegroup.Workspace
	var rootInfo, anchorInfo os.FileInfo
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		owner, err = workspace.ValidateRuntimeCleanupTarget(root, target)
		if err != nil {
			return err
		}
		rootInfo, err = os.Stat(root)
		if err != nil {
			return err
		}
		anchorInfo, err = os.Stat(owner.Path)
		return err
	})
	if err != nil {
		return target, err
	}
	if !admitted {
		if err := agentservice.CheckWorkspaceRemoval(owner.Path); err != nil {
			return target, err
		}
		release, err := workspacelease.Acquire(owner.Path, true)
		if err != nil {
			return target, err
		}
		defer release()
		if err := ctx.Err(); err != nil {
			return target, err
		}
		releaseName, err := workspacelease.AcquireSandbox(owner.Sandbox.Name, true)
		if err != nil {
			return target, err
		}
		defer releaseName()
	}
	// Keep note identity/relocation and registration decisions atomic, but never
	// hold their global callback across SBX commands. Writers share the retained
	// canonical path/name admission; external/bypassing drift is refused afresh.
	refresh := func() error {
		return workspacegroup.WithNoteLock(root, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			configured, err := workspace.DefaultRoot()
			if err != nil {
				return err
			}
			if filepath.Clean(configured) != filepath.Clean(root) {
				return fmt.Errorf("configured workspace root changed; preview cleanup again")
			}
			current, err := workspace.ValidateRuntimeCleanupTarget(root, target)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(owner, current) {
				return fmt.Errorf("workspace runtime registration changed; preview cleanup again")
			}
			currentRoot, err := os.Stat(root)
			if err != nil {
				return err
			}
			currentAnchor, err := os.Stat(owner.Path)
			if err != nil {
				return err
			}
			if !os.SameFile(rootInfo, currentRoot) || !os.SameFile(anchorInfo, currentAnchor) {
				return fmt.Errorf("workspace runtime filesystem identity changed; preview cleanup again")
			}
			return ctx.Err()
		})
	}
	if err := refresh(); err != nil {
		return target, err
	}
	client := sbxclient.New(runner)
	var runtime *sbxclient.Runtime
	if remove && target.Operation["runtime_id"] != "" {
		runtime, err = client.FindRuntimeIdentity(ctx, target.ResourceID, target.Operation["runtime_id"])
	} else {
		runtime, err = client.FindRuntime(ctx, target.ResourceID)
	}
	if err != nil {
		return target, err
	}
	if err := refresh(); err != nil {
		return target, err
	}
	if runtime == nil {
		return target, nil
	}
	if target.Operation["runtime_absent"] == "true" {
		return target, fmt.Errorf("sandbox appeared after preview; preview cleanup again")
	}
	if expected := target.Operation["runtime_id"]; expected != "" && runtime.ID != expected {
		return target, fmt.Errorf("sandbox runtime identity changed; preview cleanup again")
	}
	mountsWorkspace := func(runtime *sbxclient.Runtime) bool {
		for _, mounts := range [][]string{runtime.Workspaces, runtime.Mounts} {
			for _, mount := range mounts {
				if !strings.HasSuffix(mount, ":ro") && filepath.Clean(mount) == filepath.Clean(target.Path) {
					return true
				}
			}
		}
		return false
	}
	if !mountsWorkspace(runtime) {
		return target, fmt.Errorf("sandbox runtime no longer mounts its registered workspace read/write")
	}
	switch strings.ToLower(runtime.Status) {
	case "stopped", "running":
	default:
		return target, fmt.Errorf("sandbox runtime state %q cannot be safely cleaned up", runtime.Status)
	}
	if remove {
		if target.Operation["runtime_id"] == "" {
			return target, fmt.Errorf("sandbox removal requires a previewed runtime identity")
		}
		// Address only the inspected lifetime, never a reusable sandbox name.
		if _, err := client.Run(ctx, "", "rm", "--force", runtime.ID); err != nil {
			return target, err
		}
		if err := refresh(); err != nil {
			return target, err
		}
		// Require absence of both the reusable name and the inspected lifetime,
		// including a renamed original. Never replay rm to resolve ambiguity.
		remaining, err := client.FindRuntimeIdentity(ctx, target.ResourceID, runtime.ID)
		if err != nil {
			return target, err
		}
		if err := refresh(); err != nil {
			return target, err
		}
		if remaining != nil {
			return target, fmt.Errorf("registered sandbox still exists after removal")
		}
		return target, nil
	}
	if strings.ToLower(runtime.Status) == "stopped" {
		return target, nil
	}
	// A reusable name can move after inspection despite Radar's admission.
	// Stop only the inspected lifetime; never fall back to a name or replay.
	if _, err := client.Run(ctx, "", "stop", runtime.ID); err != nil {
		return target, err
	}
	if err := refresh(); err != nil {
		return target, err
	}
	// Command acceptance is not evidence that the registered runtime stopped.
	// Observe the same lifetime under retained path/name admission and repeat
	// metadata, filesystem and cancellation guards after the provider read.
	remaining, err := client.FindRuntime(ctx, target.ResourceID)
	if err != nil {
		return target, err
	}
	if err := refresh(); err != nil {
		return target, err
	}
	if remaining == nil || remaining.ID != runtime.ID {
		return target, fmt.Errorf("sandbox runtime identity changed after stop; preview cleanup again")
	}
	if !mountsWorkspace(remaining) {
		return target, fmt.Errorf("sandbox runtime no longer mounts its registered workspace read/write")
	}
	if strings.ToLower(remaining.Status) != "stopped" {
		return target, fmt.Errorf("registered sandbox is not stopped after retirement")
	}
	target.Operation["runtime_retired"] = "true"
	return target, nil
}
