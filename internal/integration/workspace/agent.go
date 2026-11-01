package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// The intent lives beside the native store, not in the disposable workspace.
// Persist it before provisioning so an interrupted add reuses the same note,
// workspace and sandbox. Existing explicit bindings have no ownership intent.
type agentWorkspaceIntent struct {
	Version int                            `json:"version"`
	AgentID string                         `json:"agent_id"`
	Root    string                         `json:"root"`
	Binding protocol.AgentWorkspaceBinding `json:"binding"`
	Note    *DesiredWorkspaceNote          `json:"note"`
}

func (s Source) PrepareAgentWorkspace(ctx context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	options, err := s.createOptions(integration.ManagedWorkspaceRequest{PrepareOnly: true, Name: "agent-" + req.ID, TaskLinkingKey: "radar.agent:" + req.ID})
	if err != nil {
		return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, err
	}
	cfg, err := config.Load()
	if err != nil {
		return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, err
	}
	return prepareAgentWorkspace(ctx, ExecRunner{}, options, req, cfg.Workspace.AutoConfirm)
}

func prepareAgentWorkspace(ctx context.Context, runner Runner, options CreateOptions, req integration.AgentWorkspaceRequest, autoConfirm bool) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	fail := func(err error) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
		return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, err
	}
	intentPath := filepath.Join(req.DataDir, "workspace.json")
	var intent agentWorkspaceIntent
	data, err := os.ReadFile(intentPath)
	owned := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if owned {
		if err := json.Unmarshal(data, &intent); err != nil {
			return fail(fmt.Errorf("invalid agent workspace intent; preserve agent data: %w", err))
		}
		if intent.Version != 1 || intent.AgentID != req.ID || intent.Root == "" || intent.Binding.ID == "" || intent.Binding.Path == "" || intent.Note == nil || intent.Note.Path == "" || intent.Note.LinkingKey == "" {
			return fail(fmt.Errorf("invalid or unsupported agent workspace intent; preserve agent data"))
		}
		if req.Path != "" && !sameCleanPath(req.Path, intent.Binding.Path) {
			return fail(fmt.Errorf("agent already owns workspace %s; refusing to rebind retained work", intent.Binding.Path))
		}
		if req.Expected != nil && intent.Binding != *req.Expected {
			return fail(fmt.Errorf("agent workspace ownership binding changed; refusing recovery"))
		}
		options.WorkspaceRoot, options.Path, options.Note = intent.Root, intent.Binding.Path, intent.Note
	} else if req.Path != "" {
		binding, err := agentWorkspace(ctx, runner, req.Path, options.Sandbox)
		if err != nil {
			return fail(err)
		}
		if err := verifyExecutionBinding(binding, req.Expected); err != nil {
			return fail(err)
		}
		if req.Preview {
			return fail(fmt.Errorf("an explicitly bound workspace is not agent-managed; use workspace reconciliation"))
		}
		return binding, integration.WorkspaceReconcilePlan{}, nil
	} else if req.Expected != nil {
		return fail(fmt.Errorf("agent workspace ownership intent is missing; refusing to provision a replacement"))
	}
	options.PrepareOnly, options.Switch = true, false
	options.Name, options.TaskLinkingKey = "agent-"+req.ID, "radar.agent:"+req.ID
	if len(req.Note) != 0 {
		var note DesiredWorkspaceNote
		if err := json.Unmarshal(req.Note, &note); err != nil {
			return fail(err)
		}
		if owned && (note.Path != intent.Note.Path || note.LinkingKey != intent.Note.LinkingKey) {
			return fail(fmt.Errorf("prepared note differs from agent workspace ownership intent"))
		}
		options.Note = &note
	}
	if owned {
		_, registered, found, err := RegisteredWorkspace(intent.Binding.Path, intent.Root)
		if err != nil {
			return fail(err)
		}
		if !found && req.Expected != nil {
			return fail(fmt.Errorf("agent-owned workspace registration is missing; refusing replacement"))
		}
		if found {
			binding := workspaceBinding(registered)
			if binding != intent.Binding || registered.TaskLinkingKey != options.TaskLinkingKey || (registered.NotePath != "" && !sameCleanPath(registered.NotePath, intent.Note.Path)) {
				return fail(fmt.Errorf("agent-owned workspace identity changed; refusing recovery"))
			}
			if info, err := os.Stat(registered.Path); err != nil || !info.IsDir() {
				return fail(fmt.Errorf("agent-owned workspace anchor is missing: %s", registered.Path))
			}
		} else if intent.Binding.Mode == "sbx" && !options.Sandbox {
			return fail(fmt.Errorf("agent workspace requires SBX; refusing host fallback"))
		}
	}
	plan, _, err := planCreate(ctx, runner, options)
	if err != nil {
		return fail(err)
	}
	binding := workspaceBinding(plan.group)
	if owned && binding != intent.Binding {
		return fail(fmt.Errorf("agent workspace plan changes its ownership binding"))
	}
	if plan.group.TaskLinkingKey != options.TaskLinkingKey {
		return fail(fmt.Errorf("workspace is not owned by this agent"))
	}
	relative, err := filepath.Rel(binding.Path, req.DataDir)
	if err != nil {
		return fail(err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		return fail(fmt.Errorf("durable agent data must be outside its disposable workspace"))
	}
	public := integrationPlan(plan)
	public.AutoConfirm = autoConfirm
	if req.Preview {
		return binding, public, nil
	}
	if req.PlanID != "" && req.PlanID != plan.PlanID {
		return fail(fmt.Errorf("agent workspace plan changed; preview it again"))
	}
	if !autoConfirm && len(plan.Changes) > 0 && req.PlanID == "" {
		return fail(fmt.Errorf("agent workspace confirmation required; use agent add/start --preview and supply the accepted --plan-id and --prepared-note"))
	}
	if !owned {
		intent = agentWorkspaceIntent{Version: 1, AgentID: req.ID, Root: plan.root, Binding: binding, Note: plan.Note}
		if err := saveAgentWorkspaceIntent(intentPath, intent); err != nil {
			return fail(err)
		}
	}
	options.Note, options.ExpectedPlanID = plan.Note, plan.PlanID
	if _, err := Create(ctx, runner, options); err != nil {
		return fail(err)
	}
	// exec starts a stopped SBX in place; do not replace it just to resume it.
	if plan.group.Sandbox != nil {
		if _, err := managedClient(runner).Run(ctx, binding.Path, "exec", "--workdir", binding.Path, binding.Sandbox, "true"); err != nil {
			return fail(fmt.Errorf("start agent workspace sandbox: %w", err))
		}
	}
	verified, err := agentWorkspace(ctx, runner, binding.Path, options.Sandbox)
	if err != nil {
		return fail(err)
	}
	if err := verifyExecutionBinding(verified, &binding); err != nil {
		return fail(err)
	}
	return verified, public, nil
}

func workspaceBinding(registered workspacegroup.Workspace) protocol.AgentWorkspaceBinding {
	binding := protocol.AgentWorkspaceBinding{ID: registered.ID, Path: registered.Path, Mode: "local"}
	if registered.Sandbox != nil {
		binding.Mode, binding.Sandbox = "sbx", registered.Sandbox.Name
	}
	return binding
}

func saveAgentWorkspaceIntent(path string, intent agentWorkspaceIntent) error {
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".workspace-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Publish once: another daemon must not overwrite this agent's identity.
	if err := os.Link(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
