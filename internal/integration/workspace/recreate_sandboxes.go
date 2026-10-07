package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	sbxsettings "radar/internal/integration/sbx/settings"
	"radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
)

func (Source) PreviewRecreateSandboxes(ctx context.Context, req integration.SandboxRecreateRequest) (integration.SandboxRecreatePlan, error) {
	return PreviewRecreateSandboxes(ctx, ExecRunner{}, req)
}

func (Source) RecreateSandboxes(ctx context.Context, logger *slog.Logger, req integration.SandboxRecreateRequest) (integration.SandboxRecreateResult, error) {
	return RecreateSandboxes(ctx, ExecRunner{}, logger, req)
}

func PreviewRecreateSandboxes(ctx context.Context, runner Runner, req integration.SandboxRecreateRequest) (integration.SandboxRecreatePlan, error) {
	plan, _, err := planSandboxRecreation(ctx, runner, req)
	return plan, err
}

func planSandboxRecreation(ctx context.Context, runner Runner, req integration.SandboxRecreateRequest) (integration.SandboxRecreatePlan, []workspacegroup.Workspace, error) {
	plan := integration.SandboxRecreatePlan{Targets: []integration.SandboxRecreateTarget{}}
	root, err := workspaceRoot(req.WorkspaceRoot)
	if err != nil {
		return plan, nil, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return plan, nil, err
	}
	selectedID := ""
	if req.Workspace != "" {
		_, selected, found, err := RegisteredWorkspace(req.Workspace, root)
		if err != nil {
			return plan, nil, err
		}
		if !found || selected.Sandbox == nil {
			return plan, nil, errors.New("selected path is not a registered Radar workspace with a sandbox")
		}
		selectedID = selected.ID
	}
	owners := map[string]int{}
	groups := []workspacegroup.Workspace{}
	for _, group := range registry.Workspaces {
		if group.Sandbox == nil {
			continue
		}
		owners[group.Sandbox.Name]++
		if selectedID == "" || group.ID == selectedID {
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Sandbox.Name < groups[j].Sandbox.Name })
	if len(groups) > 0 {
		if err := sbxclient.New(runner).RequireManaged(); err != nil {
			return plan, nil, err
		}
		sandboxes, err := listSandboxes(ctx, runner)
		if err != nil {
			return plan, nil, err
		}
		observed := map[string]listedSandbox{}
		for _, sandbox := range sandboxes {
			if _, exists := observed[sandbox.Name]; exists {
				return plan, nil, fmt.Errorf("SBX listed duplicate sandbox name %q", sandbox.Name)
			}
			observed[sandbox.Name] = sandbox
		}
		for _, group := range groups {
			if err := ctx.Err(); err != nil {
				return plan, nil, err
			}
			actual, found := observed[group.Sandbox.Name]
			target := integration.SandboxRecreateTarget{
				WorkspaceID: group.ID, WorkspaceName: group.Name, WorkspacePath: group.Path,
				SandboxName: group.Sandbox.Name, SandboxID: actual.ID, PreviousStatus: actual.Status, Status: "recreate",
			}
			switch {
			case owners[group.Sandbox.Name] != 1:
				target.Status, target.Reason = "blocked", "sandbox name is shared by multiple registered workspaces"
			case !found:
				target.Status, target.Reason = "skipped", "sandbox is absent; open the workspace to recover it"
			default:
				if err := validateSandboxRecreation(ctx, runner, group, actual); err != nil {
					target.Status, target.Reason = "blocked", err.Error()
				}
			}
			plan.Targets = append(plan.Targets, target)
		}
	}
	// Include the complete recorded recipe and runtime identities in confirmation.
	// Neither environment-file contents nor secret/policy values enter this plan.
	data, err := json.Marshal(struct {
		Groups  []workspacegroup.Workspace
		Targets []integration.SandboxRecreateTarget
	}{groups, plan.Targets})
	if err != nil {
		return plan, nil, err
	}
	sum := sha256.Sum256(data)
	plan.PlanID = hex.EncodeToString(sum[:])
	return plan, groups, nil
}

func validateSandboxRecreation(ctx context.Context, runner Runner, group workspacegroup.Workspace, actual listedSandbox) error {
	sandbox := group.Sandbox
	if actual.ID == "" || (actual.Status != "running" && actual.Status != "stopped") {
		return errors.New("sandbox has no stable runtime identity or is not running/stopped")
	}
	if !sameMountSet(sandbox.Mounts, sandboxWorkspaceMounts(actual)) {
		return errors.New("live mounts differ from Radar's recorded mounts; reconcile the workspace first")
	}
	if err := sbxsettings.ValidateReadyCommand(sandbox.ReadyCommand); err != nil {
		return err
	}
	if err := validateSandboxEnvFile(sandbox.EnvFile); err != nil {
		return err
	}
	if _, err := sandboxCreateArgs(group.Path, sandbox.Name, SandboxKitConfig{Name: sandbox.Agent, Path: sandbox.KitPath}, sandbox.EnvFile, sandbox.Mounts); err != nil {
		return err
	}
	for _, mount := range append([]string{group.Path}, sandbox.Mounts...) {
		path := strings.TrimSuffix(mount, ":ro")
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("required mount directory is unavailable: %s", path)
		}
	}
	if !strings.ContainsAny(sandbox.Agent, "/:") {
		switch sandbox.Agent {
		case "claude", "codex", "copilot", "cursor", "devin", "docker-agent", "droid", "gemini", "kiro", "opencode", "shell":
		default:
			return errors.New("recorded agent is not a built-in agent or a kit reference; original creation recipe is unavailable")
		}
	}
	for _, ref := range []string{sandbox.Agent, sandbox.KitPath} {
		if ref == "" {
			continue
		}
		if filepath.IsAbs(ref) || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "~/") {
			path := ExpandPath(ref)
			if !filepath.IsAbs(path) {
				path = filepath.Join(group.Path, path)
			}
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("recorded kit is unavailable: %s", path)
			}
		}
	}
	bindings, err := listSandboxPortBindings(ctx, runner, sandbox.Name)
	if err != nil {
		return errors.New("could not inspect published sandbox ports")
	}
	ports, err := logicalSandboxPorts(bindings)
	if err != nil {
		return errors.New("invalid published sandbox ports")
	}
	if len(incompatibleSandboxPorts(bindings)) > 0 || len(portDifference(ports, sandbox.Ports)) > 0 || len(portDifference(sandbox.Ports, ports)) > 0 {
		return errors.New("live ports differ from Radar's recorded IPv4 ports; reconcile the workspace first")
	}
	return validateRecreationCredentialsAndPolicies(ctx, runner, sandbox.Name)
}

// SBX removal deletes sandbox-scoped secrets. Do not export credentials or
// silently discard them. Unknown/unreadable metadata fails closed.
func validateRecreationCredentialsAndPolicies(ctx context.Context, runner Runner, name string) error {
	client := sbxclient.New(runner)
	output, err := client.Run(ctx, "", "secret", "ls", "--sandbox", name, "--json")
	if err != nil {
		return errors.New("could not inspect sandbox-scoped secrets; recreation refused")
	}
	var secrets struct {
		Secrets       json.RawMessage `json:"secrets"`
		CustomSecrets json.RawMessage `json:"custom_secrets"`
		EnvOnlyCount  *int            `json:"env_only_count"`
	}
	if json.Unmarshal([]byte(output), &secrets) != nil || secrets.Secrets == nil || secrets.CustomSecrets == nil || secrets.EnvOnlyCount == nil {
		return errors.New("unrecognized SBX secret metadata; recreation refused")
	}
	count := *secrets.EnvOnlyCount
	for _, raw := range []json.RawMessage{secrets.Secrets, secrets.CustomSecrets} {
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) != nil {
			return errors.New("unrecognized SBX secret metadata; recreation refused")
		}
		count += len(rows)
	}
	if count != 0 {
		return errors.New("sandbox-scoped secrets would be deleted by SBX; preserve/reconfigure them before recreation")
	}
	output, err = client.Run(ctx, "", "policy", "ls", name, "--include-inactive", "--json")
	if err != nil {
		return errors.New("could not inspect sandbox policies; recreation refused")
	}
	var policies struct {
		Rules *[]struct {
			Name       string `json:"name"`
			Scope      string `json:"scope"`
			Editable   bool   `json:"editable"`
			Provenance struct {
				CreatedVia string `json:"created_via"`
				Source     string `json:"source"`
			} `json:"provenance"`
		} `json:"rules"`
	}
	if json.Unmarshal([]byte(output), &policies) != nil || policies.Rules == nil {
		return errors.New("unrecognized SBX policy metadata; recreation refused")
	}
	for _, rule := range *policies.Rules {
		if rule.Scope == "global" {
			continue
		}
		// SBX regenerates this noneditable kit rule from the recorded kit.
		if rule.Scope == "sandbox:"+name && rule.Name == "kit:"+name && !rule.Editable &&
			rule.Provenance.CreatedVia == "provisioned" && rule.Provenance.Source == "sandboxes" {
			continue
		}
		return errors.New("sandbox-specific policies are not recorded by Radar; preserve/reconfigure them before recreation")
	}
	return nil
}

func RecreateSandboxes(ctx context.Context, runner Runner, logger *slog.Logger, req integration.SandboxRecreateRequest) (integration.SandboxRecreateResult, error) {
	result := integration.SandboxRecreateResult{Targets: []integration.SandboxRecreateTarget{}}
	if req.ExpectedPlanID == "" {
		return result, errors.New("sandbox recreation requires a confirmed preview plan")
	}
	release, err := operationlock.Acquire(false)
	if err != nil {
		return result, err
	}
	defer release()
	root, err := workspaceRoot(req.WorkspaceRoot)
	if err != nil {
		return result, err
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		var err error
		result, err = recreateSandboxes(ctx, runner, logger, req, defaultSandboxReconcilePolicy)
		return err
	})
	return result, err
}

func recreateSandboxes(ctx context.Context, runner Runner, logger *slog.Logger, req integration.SandboxRecreateRequest, policy sandboxReconcilePolicy) (integration.SandboxRecreateResult, error) {
	result := integration.SandboxRecreateResult{Targets: []integration.SandboxRecreateTarget{}}
	plan, groups, err := planSandboxRecreation(ctx, runner, req)
	if err != nil {
		return result, err
	}
	if plan.PlanID != req.ExpectedPlanID {
		return result, errors.New("sandbox recreation plan changed; preview and confirm again")
	}
	policy.forceRecreate = true
	for i, target := range plan.Targets {
		switch target.Status {
		case "blocked":
			result.Failed++
		case "skipped":
			result.Skipped++
		default:
			err := ctx.Err()
			if err == nil {
				targetCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				err = recreateOneSandbox(targetCtx, runner, logger, groups[i], target, policy)
				cancel()
			}
			if err != nil {
				target.Status, target.Reason = "failed", err.Error()
				result.Failed++
			} else {
				target.Status = "recreated"
				result.Recreated++
			}
		}
		result.Targets = append(result.Targets, target)
		if logger != nil {
			logger.Info("sandbox recreation result", "workspace_id", target.WorkspaceID, "sandbox", target.SandboxName, "status", target.Status, "reason", target.Reason)
		}
	}
	result.OK = result.Failed == 0
	return result, nil
}

func recreateOneSandbox(ctx context.Context, runner Runner, logger *slog.Logger, group workspacegroup.Workspace, target integration.SandboxRecreateTarget, policy sandboxReconcilePolicy) error {
	// Recheck immediately before deleting: external SBX changes are not protected
	// by Radar's workspace lock.
	actual, found, err := findSandbox(ctx, runner, group.Path, target.SandboxName)
	if err != nil {
		return err
	}
	if !found || actual.ID != target.SandboxID || actual.Status != target.PreviousStatus {
		return errors.New("sandbox changed after confirmation; left untouched")
	}
	if err := validateSandboxRecreation(ctx, runner, group, actual); err != nil {
		return err
	}
	if err := reconcileSandboxWithPolicy(ctx, runner, group, logger, policy); err != nil {
		return err
	}
	if _, _, err := reconcileSandboxPorts(ctx, runner, target.SandboxName, group.Sandbox.Ports); err != nil {
		return fmt.Errorf("sandbox recreated but port restoration failed: %w", err)
	}
	client := sbxclient.New(runner)
	if target.PreviousStatus == "stopped" {
		if _, err := client.Run(ctx, "", "stop", target.SandboxName); err != nil {
			return errors.New("sandbox recreated but could not restore stopped state")
		}
	} else if _, err := client.Run(ctx, group.Path, "exec", target.SandboxName, "true"); err != nil {
		return errors.New("sandbox recreated but could not restore running state")
	}
	return nil
}
