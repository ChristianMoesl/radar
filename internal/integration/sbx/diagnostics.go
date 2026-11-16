package sbx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"radar/internal/command"
	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/protocol"
)

var _ integration.RuntimeProbeProvider = Source{}

func (Source) DiagnosticRuntime(ctx context.Context, name string) protocol.DiagnosticRuntimeObservation {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return diagnosticRuntime(ctx, name, diagnosticRuntimeRunner{})
}

func diagnosticRuntime(ctx context.Context, name string, runner sbxclient.Runner) protocol.DiagnosticRuntimeObservation {
	view := protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "probe_failed"}
	if ctx.Err() != nil {
		view.Problem = "cancelled_or_timed_out"
		return view
	}
	runtime, err := sbxclient.New(runner).FindRuntime(ctx, name)
	if ctx.Err() != nil {
		view.Problem = "cancelled_or_timed_out"
		return view
	}
	if err != nil {
		return view
	}
	view.Problem = ""
	if runtime == nil {
		view.Presence = "absent"
		return view
	}
	view.Presence, view.ID = "present", runtime.ID
	switch strings.ToLower(runtime.Status) {
	case "running", "stopped":
		view.State = strings.ToLower(runtime.Status)
	}
	return view
}

// Native executable selection/path translation is preserved, but diagnostic
// stdout is bounded by command.Output and stderr never leaves the process.
type diagnosticRuntimeRunner struct{}

func (diagnosticRuntimeRunner) LookPath(name string) error { _, err := exec.LookPath(name); return err }
func (diagnosticRuntimeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	// Windows listing normalization supplies / as its non-workspace cwd.
	// These read-only commands never remove or lock a registered workspace.
	if cwd != "" && cwd != "/" {
		return "", fmt.Errorf("runtime diagnostic cannot set a working directory")
	}
	output, err := command.OutputInDir(ctx, cwd, name, args, sbxclient.Environment(name, os.Environ()))
	return string(output), err
}
