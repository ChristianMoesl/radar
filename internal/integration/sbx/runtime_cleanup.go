package sbx

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func retireSandbox(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget) (protocol.CleanupTarget, error) {
	return cleanupRegisteredSandbox(ctx, runner, target, false)
}

func cleanupRegisteredSandbox(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget, remove bool) (protocol.CleanupTarget, error) {
	delete(target.Operation, "runtime_retired")
	root, err := workspace.DefaultRoot()
	if err != nil {
		return target, err
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		if _, err := workspace.ValidateRuntimeCleanupTarget(root, target); err != nil {
			return err
		}
		client := sbxclient.New(runner)
		runtime, err := client.FindRuntime(ctx, target.ResourceID)
		if err != nil {
			return err
		}
		if runtime == nil {
			return nil
		}
		if target.Operation["runtime_absent"] == "true" {
			return fmt.Errorf("sandbox appeared after preview; preview cleanup again")
		}
		if expected := target.Operation["runtime_id"]; expected != "" && runtime.ID != expected {
			return fmt.Errorf("sandbox runtime identity changed; preview cleanup again")
		}
		owned := false
		for _, mount := range append(runtime.Workspaces, runtime.Mounts...) {
			if !strings.HasSuffix(mount, ":ro") && filepath.Clean(mount) == filepath.Clean(target.Path) {
				owned = true
			}
		}
		if !owned {
			return fmt.Errorf("sandbox runtime no longer mounts its registered workspace read/write")
		}
		switch strings.ToLower(runtime.Status) {
		case "stopped", "running":
		default:
			return fmt.Errorf("sandbox runtime state %q cannot be safely cleaned up", runtime.Status)
		}
		if remove {
			if target.Operation["runtime_id"] == "" {
				return fmt.Errorf("sandbox removal requires a previewed runtime identity")
			}
			// Address the inspected lifetime, never a reusable sandbox name.
			if _, err := client.Run(ctx, "", "rm", "--force", runtime.ID); err != nil {
				return err
			}
			remaining, err := client.FindRuntime(ctx, target.ResourceID)
			if err != nil {
				return err
			}
			if remaining != nil {
				return fmt.Errorf("registered sandbox still exists after removal")
			}
			return nil
		}
		if strings.ToLower(runtime.Status) == "stopped" {
			return nil
		}
		_, err = client.Run(ctx, "", "stop", target.ResourceID)
		if err == nil {
			target.Operation["runtime_retired"] = "true"
		}
		return err
	})
	return target, err
}
