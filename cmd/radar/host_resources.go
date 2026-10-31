package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"radar/internal/app"
	"radar/internal/client"
	"radar/internal/integration"
	"radar/internal/openurl"
	"radar/internal/protocol"
	"radar/internal/socket"
	"radar/internal/sourceactions"
)

type hostResourceOptions struct {
	workspace, request, plan string
	preview                  bool
}

func hostResourceFlags(name string, args []string) hostResourceOptions {
	flags := outputFlags("radar " + name)
	current := flags.String("workspace", "", "path inside the current registered Radar workspace")
	request := flags.String("request", "", "strict JSON request")
	plan := flags.String("plan", "", "accepted preview plan ID (drift check, not consent)")
	preview := flags.Bool("preview", false, "validate and preview without side effects")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || *request == "" || (*preview && *plan != "") {
		fmt.Fprintf(os.Stderr, "usage: radar %s --request <json> [--workspace <path>] [--preview | --plan <id>] [--json]\n", name)
		os.Exit(2)
	}
	return hostResourceOptions{*current, *request, *plan, *preview}
}

func decodeHostResourceRequest(raw string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid host resource request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON request object")
	}
	return nil
}

func runCloneRepository(args []string) {
	options := hostResourceFlags("clone-repository", args)
	var req integration.RepositoryCloneRequest
	if err := decodeHostResourceRequest(options.request, &req); err != nil {
		fatal(err)
	}
	req.Workspace = options.workspace
	provider, err := app.DefaultIntegrations().RepositoryCloner()
	if err != nil {
		fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := cloneRepositoryCommand(ctx, provider, req, options.preview, options.plan, jsonOutput, os.Stdin, os.Stderr)
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func cloneRepositoryCommand(ctx context.Context, provider integration.RepositoryCloneProvider, req integration.RepositoryCloneRequest, preview bool, expected string, machine bool, input io.Reader, out io.Writer) (any, error) {
	plan, err := provider.PreviewRepositoryClone(ctx, req)
	if err != nil {
		return nil, err
	}
	if preview {
		return plan, nil
	}
	if plan.RequiresConfirmation {
		if machine {
			if expected == "" {
				return nil, fmt.Errorf("initial cloning requires explicit confirmation; preview and supply the accepted --plan from the trusted approval adapter")
			}
		} else {
			if err := writeResult(out, out, plan, false); err != nil {
				return nil, err
			}
			if !confirmUpdate(bufio.NewScanner(input), out, "Clone this repository on the host?") {
				return integration.RepositoryCloneResult{Status: "cancelled", Remote: plan.Remote, Repository: plan.Repository}, nil
			}
			if expected != "" && expected != plan.PlanID {
				return nil, fmt.Errorf("clone plan changed; preview and confirm again")
			}
			expected = plan.PlanID
		}
	} else if expected == "" {
		expected = plan.PlanID
	}
	return provider.ApplyRepositoryClone(ctx, req, expected)
}

func runOpenResource(args []string) {
	options := hostResourceFlags("open-resource", args)
	var req integration.ResourceOpenRequest
	if err := decodeHostResourceRequest(options.request, &req); err != nil {
		fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	registry := app.DefaultIntegrations()
	prepare := func() (integration.ResourceOpenPlan, error) {
		manager, err := registry.WorkspaceManager()
		if err != nil {
			return integration.ResourceOpenPlan{}, err
		}
		registration, found, err := manager.RegisteredWorkspace(options.workspace)
		if err != nil {
			return integration.ResourceOpenPlan{}, err
		}
		if !found {
			return integration.ResourceOpenPlan{}, fmt.Errorf("opening requires a registered Radar workspace")
		}
		task, err := cachedWorkspaceTask(ctx, registration.ID)
		if err != nil {
			return integration.ResourceOpenPlan{}, err
		}
		return sourceactions.PreviewResourceOpen(ctx, registry.ResourceOpenProviders(), registration.ID, task, req)
	}
	result, err := openResourceCommand(ctx, prepare, options.preview, options.plan, jsonOutput, os.Stdin, os.Stderr, openurl.Launch)
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func openResourceCommand(ctx context.Context, prepare func() (integration.ResourceOpenPlan, error), preview bool, expected string, machine bool, input io.Reader, out io.Writer, launch func(context.Context, string) error) (any, error) {
	plan, err := prepare()
	if err != nil {
		return nil, err
	}
	if preview {
		return plan, nil
	}
	if machine {
		if expected == "" {
			return nil, fmt.Errorf("opening requires explicit confirmation per batch; preview and supply the accepted --plan from the trusted approval adapter")
		}
	} else {
		if err := writeResult(out, out, plan, false); err != nil {
			return nil, err
		}
		if !confirmUpdate(bufio.NewScanner(input), out, "Open this batch in the host browser?") {
			return integration.ResourceOpenResult{OK: false, Cancelled: true, Results: []integration.ResourceOpenOutcome{}}, nil
		}
		if expected != "" && expected != plan.PlanID {
			return nil, fmt.Errorf("opening plan changed; preview and confirm again")
		}
		expected = plan.PlanID
		plan, err = prepare()
		if err != nil {
			return nil, err
		}
	}
	return sourceactions.ApplyResourceOpen(ctx, plan, expected, launch)
}

func cachedWorkspaceTask(ctx context.Context, workspaceID string) (protocol.Task, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Task{}, err
	}
	path, err := socket.Path()
	if err != nil {
		return protocol.Task{}, err
	}
	// A cache read only: never ensure/start the daemon or trigger collection.
	response, err := client.CallWithTimeout(path, "tasks", 2*time.Second)
	if err != nil {
		return protocol.Task{}, fmt.Errorf("cached task resources unavailable; start/refresh Radar normally before opening resources")
	}
	if !response.OK {
		return protocol.Task{}, fmt.Errorf("cached task resources unavailable")
	}
	if response.ResourceRefreshPending {
		return protocol.Task{}, fmt.Errorf("resource list refresh pending; refresh Radar before opening resources")
	}
	if err := ctx.Err(); err != nil {
		return protocol.Task{}, err
	}
	return sourceactions.WorkspaceTask(response.Tasks, workspaceID)
}

func populateOpeningResources(ctx context.Context, registry integration.Registry, result *integration.WorkspaceContext) {
	result.OpenableResources = make([]integration.OpenResource, 0)
	if !result.Registered {
		result.ResourceDiscoveryError = "resource opening requires a registered workspace"
		return
	}
	task, err := cachedWorkspaceTask(ctx, result.WorkspaceID)
	if err == nil {
		result.OpenableResources, err = sourceactions.OpenableResources(ctx, registry.ResourceOpenProviders(), task)
	}
	if err != nil {
		result.OpenableResources = make([]integration.OpenResource, 0)
		result.ResourceDiscoveryError = err.Error()
	}
}
