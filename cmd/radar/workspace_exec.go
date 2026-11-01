package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"radar/internal/app"
	"radar/internal/protocol"
	"syscall"
)

func runWorkspaceExec(args []string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	flags := outputFlags("radar workspace-exec")
	workspace := flags.String("workspace", "", "registered workspace")
	cwd := flags.String("cwd", "", "working directory within workspace")
	binding := flags.String("binding", "", "expected execution binding JSON; reject a changed workspace or sandbox")
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
	var expected *protocol.AgentWorkspaceBinding
	if *binding != "" {
		expected = &protocol.AgentWorkspaceBinding{}
		if err := json.Unmarshal([]byte(*binding), expected); err != nil || expected.ID == "" || expected.Path == "" ||
			(expected.Mode != "local" && expected.Mode != "sbx") || (expected.Mode == "sbx" && expected.Sandbox == "") {
			fatal(errors.New("binding requires workspace id, path, mode and selected sandbox for SBX"))
		}
	}
	output, err := provider.ExecuteWorkspace(ctx, *workspace, *cwd, flags.Args(), expected)
	if err != nil {
		fatal(err)
	}
	if jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"output": output})
	} else {
		os.Stdout.WriteString(output)
	}
}
