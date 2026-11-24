package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"radar/internal/agentservice"
	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

var _ integration.SandboxRecreationPreviewDiagnostics = Source{}

func (Source) DiagnosticRecreationPreview(ctx context.Context, id, scope string) (protocol.DiagnosticRecreationPreview, error) {
	root, err := DefaultRoot()
	if err != nil {
		return protocol.DiagnosticRecreationPreview{}, errors.New("native recreation preview unavailable")
	}
	return diagnosticRecreationPreview(ctx, ExecRunner{}, root, id, scope)
}

func diagnosticRecreationPreview(ctx context.Context, runner Runner, root, id, scope string) (protocol.DiagnosticRecreationPreview, error) {
	view, _, err := captureDiagnosticRecreationPreview(ctx, runner, root, id, scope)
	return view, err
}

// Capture the private native confirmation from the same planner invocation as
// the public proposal. Never recover it by replanning after those observations.
// The request stays in-process: it has no expiry, one-use or consent authority,
// and every failed capture must withhold it along with the public projection.
func captureDiagnosticRecreationPreview(ctx context.Context, runner Runner, root, id, scope string) (protocol.DiagnosticRecreationPreview, integration.SandboxRecreateRequest, error) {
	fail := func() (protocol.DiagnosticRecreationPreview, integration.SandboxRecreateRequest, error) {
		return protocol.DiagnosticRecreationPreview{}, integration.SandboxRecreateRequest{}, errors.New("native recreation preview unavailable; no recreation requested")
	}
	if ctx.Err() != nil || id == "" || len(id) > 512 || len(scope) > 4096 || (scope != "" && !filepath.IsAbs(scope)) {
		return fail()
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return fail()
	}
	before, err := json.Marshal(registry)
	if err != nil || len(before) > 1024*1024 {
		return fail()
	}
	var selected *workspacegroup.Workspace
	for _, group := range registry.Workspaces {
		if group.ID == id && (scope == "" || filepath.Clean(scope) == filepath.Clean(group.Path)) {
			if selected != nil || group.Sandbox == nil {
				return fail()
			}
			copy := group
			selected = &copy
		}
	}
	if selected == nil || ctx.Err() != nil {
		return fail()
	}
	// Observe bounded registered owners and only the existing deletion-lease
	// inode. These are not runtime ownership, holder identity or writer admission.
	paths := []string{selected.Path}
	owners, ownerSnapshot, ownerErr := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, paths)
	leaseEvidence, leaseSnapshot := workspacelease.ProbeExclusive(ctx, selected.Path)
	// Resolve this path from registration again in the production planner. Never
	// accept client native targets, commands or an expected apply plan.
	plan, err := PreviewRecreateSandboxes(ctx, runner, integration.SandboxRecreateRequest{WorkspaceRoot: root, Workspace: selected.Path})
	if err != nil || ctx.Err() != nil || len(plan.Targets) != 1 || len(plan.PlanID) != 64 {
		return fail()
	}
	target := plan.Targets[0]
	if target.WorkspaceID != selected.ID || target.WorkspacePath != selected.Path || target.SandboxName != selected.Sandbox.Name {
		return fail()
	}
	// Metadata reads can outlive the planner's initial listing. Observe the
	// expected lifetime independently, without replanning or adopting a newer
	// runtime at the reusable name. This is still point-in-time evidence only.
	probeCtx, cancel := context.WithTimeout(ctx, sandboxObservationTimeout)
	client := sbxclient.New(runner)
	var actual *sbxclient.Runtime
	if target.SandboxID == "" {
		actual, err = client.FindRuntime(probeCtx, target.SandboxName)
	} else {
		actual, err = client.FindRuntimeIdentity(probeCtx, target.SandboxName, target.SandboxID)
	}
	probeErr := probeCtx.Err()
	cancel()
	if err != nil || probeErr != nil || ctx.Err() != nil {
		return fail()
	}
	if actual == nil {
		if target.SandboxID != "" {
			return fail()
		}
	} else if actual.ID != target.SandboxID || actual.Status != target.PreviousStatus ||
		(target.Status == "recreate" && !sameMountSet(selected.Sandbox.Mounts, sandboxWorkspaceMounts(listedSandbox{Workspaces: actual.Workspaces, Mounts: actual.Mounts}))) {
		return fail()
	}
	current, err := workspacegroup.Load(root)
	if err != nil {
		return fail()
	}
	after, err := json.Marshal(current)
	if err != nil || string(before) != string(after) || ctx.Err() != nil {
		return fail()
	}
	_, freshOwners, freshOwnerErr := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, paths)
	freshLease, freshLeaseSnapshot := workspacelease.ProbeExclusive(ctx, selected.Path)
	if (ownerErr == nil) != (freshOwnerErr == nil) || (ownerErr == nil && ownerSnapshot != freshOwners) || leaseEvidence != freshLease || leaseSnapshot != freshLeaseSnapshot || ctx.Err() != nil {
		return fail()
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 1024*1024 {
		return fail()
	}
	// Full private registration/recipe and plan bind this display identity. It
	// deliberately cannot be supplied to the native recreation executor.
	identity := append(append(before, raw...), ownerSnapshot[:]...)
	identity = append(identity, leaseSnapshot[:]...)
	sum := sha256.Sum256(identity)
	view := protocol.DiagnosticRecreationPreview{ID: id, PreviewID: "recreation-preview:" + hex.EncodeToString(sum[:]), Status: target.Status, PreviousState: "unknown", RuntimeIdentityObserved: target.SandboxID != "", ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: leaseEvidence}
	if ownerErr == nil {
		count := owners[selected.Path]
		view.OwnershipEvidence, view.AgentOwnerCount = "fresh_bounded_registration", &count
	}
	if target.PreviousStatus == "running" || target.PreviousStatus == "stopped" {
		view.PreviousState = target.PreviousStatus
	}
	if !protocol.ValidDiagnosticRecreationPreview(view, id) {
		return fail()
	}
	return view, integration.SandboxRecreateRequest{WorkspaceRoot: root, Workspace: selected.Path, ExpectedPlanID: plan.PlanID}, nil
}
