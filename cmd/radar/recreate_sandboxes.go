package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"radar/internal/app"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/logging"
)

func runRecreateSandboxes(args []string) {
	flags := outputFlags("radar recreate-sandboxes")
	current := flags.String("workspace", "", "limit recreation to this registered workspace (default: all Radar-managed sandboxes)")
	preview := flags.Bool("preview", false, "inspect and print the plan without changes")
	yes := flags.Bool("yes", false, "confirm loss of sandbox-local files and interruption of processes")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || (*preview && *yes) {
		fmt.Fprintln(os.Stderr, "usage: radar recreate-sandboxes [--workspace <path>] [--preview | --yes] [--json]")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	manager, err := app.DefaultIntegrations().WorkspaceManager()
	if err != nil {
		fatal(err)
	}
	logger, file, _, err := logging.New()
	if err != nil {
		fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	value, err := recreateSandboxesCommand(ctx, manager, logger, integration.SandboxRecreateRequest{
		WorkspaceRoot: manager.ExpandPath(cfg.Workspace.RootDir), Workspace: *current,
	}, *preview, *yes, os.Stdin, os.Stderr)
	cancel()
	_ = file.Close()
	if err != nil {
		fatal(err)
	}
	printResult(value)
	if result, ok := value.(integration.SandboxRecreateResult); ok && !result.OK && !result.Cancelled {
		os.Exit(1)
	}
}

func recreateSandboxesCommand(ctx context.Context, manager integration.WorkspaceSandboxRecreator, logger *slog.Logger, req integration.SandboxRecreateRequest, previewOnly, yes bool, input io.Reader, diagnostics io.Writer) (any, error) {
	plan, err := manager.PreviewRecreateSandboxes(ctx, req)
	if err != nil {
		return nil, err
	}
	if previewOnly {
		return plan, nil
	}
	ready := 0
	for _, target := range plan.Targets {
		if target.Status == "recreate" {
			ready++
		}
	}
	if ready > 0 && !yes {
		if err := writeResult(diagnostics, diagnostics, plan, false); err != nil {
			return nil, err
		}
		fmt.Fprintf(diagnostics, "Recreate %d sandbox(es)? Running commands/services will stop and files outside host mounts will be lost. This cannot be undone. [y/N] ", ready)
		answer, _ := bufio.NewReader(input).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			return integration.SandboxRecreateResult{Cancelled: true, Targets: plan.Targets}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req.ExpectedPlanID = plan.PlanID
	return manager.RecreateSandboxes(ctx, logger, req)
}
