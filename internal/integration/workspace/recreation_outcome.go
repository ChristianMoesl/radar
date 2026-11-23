package workspace

import (
	"context"
	"errors"

	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	workspacegroup "radar/internal/integration/workspace/group"
)

// observeRecreatedSandboxRuntime shares the pre-port, pre-state and final runtime
// evaluator. The first observation grounds an ID, not creation provenance; later
// observations pin that ID, and final observations also require restored state.
func observeRecreatedSandboxRuntime(ctx context.Context, runner Runner, group workspacegroup.Workspace, target integration.SandboxRecreateTarget, expectedID string, restored bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	probeCtx, cancel := context.WithTimeout(ctx, sandboxObservationTimeout)
	actual, err := sbxclient.New(runner).FindRuntimeIdentity(probeCtx, target.SandboxName, target.SandboxID)
	probeErr := probeCtx.Err()
	cancel()
	if probeErr != nil {
		return "", probeErr
	}
	if err != nil {
		return "", errors.New("sandbox recreation outcome unavailable; runtime retained for inspection")
	}
	if actual == nil || actual.ID == target.SandboxID || (expectedID != "" && actual.ID != expectedID) || (actual.Status != "running" && actual.Status != "stopped") || (restored && actual.Status != target.PreviousStatus) {
		return "", errors.New("sandbox recreation replacement or restored state not observed; inspect before retrying")
	}
	mounts := sandboxWorkspaceMounts(listedSandbox{Workspaces: actual.Workspaces, Mounts: actual.Mounts})
	if !sameMountSet(group.Sandbox.Mounts, mounts) {
		return "", errors.New("sandbox recreation recorded mounts not observed; runtime retained for inspection")
	}
	return actual.ID, nil
}

// observeSandboxRecreation runs under recreateOneSandbox's retained admission.
// Successful commands are not replacement/state/mount/port outcome evidence.
// These bounded observations are not an atomic reservation against external SBX
// changes, health attestation or permission to retry any partially applied work.
func observeSandboxRecreation(ctx context.Context, runner Runner, root string, group workspacegroup.Workspace, target integration.SandboxRecreateTarget, replacement string) error {
	if _, err := activationRegistration(ctx, root, group.Path, group); err != nil {
		return err
	}
	if _, err := observeRecreatedSandboxRuntime(ctx, runner, group, target, replacement, true); err != nil {
		return err
	}
	bindings, err := listSandboxPortBindings(ctx, runner, target.SandboxName)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return errors.New("sandbox recreation port outcome unavailable; runtime retained for inspection")
	}
	ports, err := logicalSandboxPorts(bindings)
	if err != nil || len(incompatibleSandboxPorts(bindings)) > 0 || len(portDifference(ports, group.Sandbox.Ports)) > 0 || len(portDifference(group.Sandbox.Ports, ports)) > 0 {
		return errors.New("sandbox recreation recorded IPv4 ports not observed; runtime retained for inspection")
	}
	// Port reads address a reusable name. Reject a changed lifetime/state/mount
	// during that observation, and independently recheck registration/context.
	if _, err := observeRecreatedSandboxRuntime(ctx, runner, group, target, replacement, true); err != nil {
		return err
	}
	_, err = activationRegistration(ctx, root, group.Path, group)
	return err
}
