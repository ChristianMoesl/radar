package sourceactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"radar/internal/integration"
	"radar/internal/openurl"
	"radar/internal/protocol"
)

const MaxOpenResources = 16

// WorkspaceTask uses stable workspace identity, never titles, branch text or
// caller-provided refs. Ambiguity is a failure, not a first-match selection.
func WorkspaceTask(tasks []protocol.Task, workspaceID string) (protocol.Task, error) {
	var matches []protocol.Task
	if workspaceID == "" {
		return protocol.Task{}, fmt.Errorf("a registered workspace identity is required")
	}
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.ProvidesWorkspace && ref.WorkspaceID == workspaceID {
				matches = append(matches, task)
				break
			}
		}
	}
	if len(matches) != 1 {
		return protocol.Task{}, fmt.Errorf("expected one cached task for this workspace, found %d; refresh Radar task associations", len(matches))
	}
	return matches[0], nil
}

func OpenableResources(ctx context.Context, providers []integration.ResourceOpenProvider, task protocol.Task) ([]integration.OpenResource, error) {
	resources := make([]integration.OpenResource, 0)
	seen := map[string]integration.OpenResource{}
	for _, ref := range task.SourceRefs {
		for _, provider := range providers {
			if provider.Descriptor().Name != ref.Source {
				continue
			}
			targets, err := provider.OpenResources(ctx, ref)
			if err != nil {
				return nil, fmt.Errorf("invalid %s resource: %w", provider.Descriptor().Name, err)
			}
			for _, target := range targets {
				if target.ID == "" || target.Source != provider.Descriptor().Name || target.SourceRefID != ref.ID || target.Label == "" {
					return nil, fmt.Errorf("invalid advertised opening target")
				}
				if err := openurl.ValidateHTTPS(target.URL); err != nil {
					return nil, err
				}
				if previous, ok := seen[target.ID]; ok {
					if previous.URL != target.URL || previous.Source != target.Source {
						return nil, fmt.Errorf("ambiguous advertised opening target")
					}
					continue
				}
				seen[target.ID] = target
				resources = append(resources, target)
			}
		}
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	return resources, nil
}

func PreviewResourceOpen(ctx context.Context, providers []integration.ResourceOpenProvider, workspaceID string, task protocol.Task, request integration.ResourceOpenRequest) (integration.ResourceOpenPlan, error) {
	if workspaceID == "" || task.ID <= 0 {
		return integration.ResourceOpenPlan{}, fmt.Errorf("a registered workspace and identified cached task are required")
	}
	if len(request.ResourceIDs) == 0 || len(request.ResourceIDs) > MaxOpenResources {
		return integration.ResourceOpenPlan{}, fmt.Errorf("select 1 to %d advertised resource IDs", MaxOpenResources)
	}
	manifest, err := OpenableResources(ctx, providers, task)
	if err != nil {
		return integration.ResourceOpenPlan{}, err
	}
	byID := map[string]integration.OpenResource{}
	for _, target := range manifest {
		byID[target.ID] = target
	}
	plan := integration.ResourceOpenPlan{WorkspaceID: workspaceID, TaskID: task.ID, Resources: make([]integration.OpenResource, 0), RequiresConfirmation: true}
	seen := map[string]bool{}
	for _, id := range request.ResourceIDs {
		target, ok := byID[id]
		if !ok {
			return integration.ResourceOpenPlan{}, fmt.Errorf("unknown opening target; select IDs from the current workspace context")
		}
		if !seen[id] {
			plan.Resources = append(plan.Resources, target)
			seen[id] = true
		}
	}
	data, _ := json.Marshal(plan)
	sum := sha256.Sum256(data)
	plan.PlanID = hex.EncodeToString(sum[:])
	return plan, nil
}

// ApplyResourceOpen receives a freshly rederived plan, not a caller-authored
// manifest. Consent is obtained by the trusted adapter; this comparison detects
// drift only. All validation precedes the first irreversible browser launch.
func ApplyResourceOpen(ctx context.Context, plan integration.ResourceOpenPlan, expectedPlanID string, launch func(context.Context, string) error) (integration.ResourceOpenResult, error) {
	if expectedPlanID == "" || expectedPlanID != plan.PlanID {
		return integration.ResourceOpenResult{}, fmt.Errorf("opening plan changed or confirmation missing; preview and confirm again")
	}
	result := integration.ResourceOpenResult{OK: true, Results: make([]integration.ResourceOpenOutcome, 0, len(plan.Resources))}
	for _, target := range plan.Resources {
		outcome := integration.ResourceOpenOutcome{ID: target.ID, URL: target.URL, Status: "launch_requested"}
		err := ctx.Err()
		if err == nil {
			err = launch(ctx, target.URL)
		}
		if err != nil {
			result.OK = false
			outcome.Status = "failed"
			outcome.Error = "host browser launcher failed; check desktop availability (no automatic retry)"
			if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				outcome.Error = "host browser launcher is unavailable; install the platform opener before retrying"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				outcome.Error = "host browser launcher timed out; check whether it opened before retrying"
			}
			if ctx.Err() != nil {
				outcome.Status = "cancelled"
				outcome.Error = "opening cancelled before this launch completed"
			}
		}
		result.Results = append(result.Results, outcome)
	}
	return result, nil
}
