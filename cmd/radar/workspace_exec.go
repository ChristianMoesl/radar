package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"radar/internal/app"
	"syscall"
)

func runWorkspaceExec(args []string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	flags := outputFlags("radar workspace-exec")
	workspace := flags.String("workspace", "", "registered workspace")
	cwd := flags.String("cwd", "", "working directory within workspace")
	inspect := flags.Bool("inspect", false, "inspect the execution binding without running a command")
	_ = parseFlags(flags, args)
	provider, err := app.DefaultIntegrations().WorkspaceExecutor()
	if err != nil {
		fatal(err)
	}
	if *inspect {
		binding, err := provider.AgentWorkspace(ctx, *workspace)
		if err != nil {
			fatal(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(binding)
		return
	}
	if flags.NArg() == 0 {
		fatal(errors.New("workspace-exec requires a command after --"))
	}
	output, err := provider.ExecuteWorkspace(ctx, *workspace, *cwd, flags.Args())
	if err != nil {
		fatal(err)
	}
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"output": output})
	} else {
		os.Stdout.WriteString(output)
	}
}
