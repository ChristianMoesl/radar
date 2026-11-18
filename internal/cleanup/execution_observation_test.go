package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type dispatchProvider struct {
	fakeProvider
	calls, failAt                    int
	preflightError                   bool
	cancelPreflight, cancelAfterCall context.CancelFunc
}

func (p *dispatchProvider) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	if p.cancelPreflight != nil {
		p.cancelPreflight()
	}
	if p.preflightError {
		return errors.New("private preflight error")
	}
	return nil
}
func (p *dispatchProvider) Cleanup(_ context.Context, request integration.CleanupRequest) (protocol.CleanupTarget, error) {
	p.calls++
	if p.cancelAfterCall != nil {
		p.cancelAfterCall()
	}
	if p.calls == p.failAt {
		return protocol.CleanupTarget{}, errors.New("private possibly partial failure")
	}
	return request.Target, nil
}

func TestCleanupExecutionObservationTracksActualDispatchWithoutChangingGuards(t *testing.T) {
	for _, mode := range []string{"success", "first-error", "partial-error", "cancel-between", "cancel-before", "cancel-preflight", "preflight-error", "unsupported-source", "invalid-mode", "automatic-block", "empty", "lease-contention", "ownership-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(dir, "agents"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &dispatchProvider{fakeProvider: fakeProvider{name: "git"}}
			preview := protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{{Source: "git", Kind: "worktree", ResourceID: "one"}, {Source: "git", Kind: "worktree", ResourceID: "two"}, {Source: "git", Kind: "worktree", ResourceID: "three"}}}
			options := ExecuteOptions{Mode: integration.CleanupConfirmed}
			stage, attempted, completed := "completed", 3, 3
			switch mode {
			case "first-error":
				p.failAt = 1
				stage, attempted, completed = "provider_call", 1, 0
			case "partial-error":
				p.failAt = 2
				stage, attempted, completed = "provider_call", 2, 1
			case "cancel-between":
				p.cancelAfterCall = cancel
				stage, attempted, completed = "target_dispatch", 1, 1
			case "cancel-before":
				cancel()
				stage, attempted, completed = "ownership_and_leases", 0, 0
			case "cancel-preflight":
				p.cancelPreflight = cancel
				stage, attempted, completed = "target_dispatch", 0, 0
			case "preflight-error":
				p.preflightError = true
				stage, attempted, completed = "bundle_preflight", 0, 0
			case "unsupported-source":
				preview.Targets[0].Source = "unsupported"
				stage, attempted, completed = "target_dispatch", 0, 0
			case "invalid-mode":
				options.Mode = integration.CleanupMode(255)
				stage, attempted, completed = "admission", 0, 0
			case "automatic-block":
				options.Mode = integration.CleanupSafe
				preview.Targets[0].Safety = []protocol.CleanupSafety{{BlocksAutomatic: true, Message: "private safety"}}
				stage, attempted, completed = "admission", 0, 0
			case "empty":
				preview.Targets = nil
				stage, attempted, completed = "admission", 0, 0
			case "lease-contention":
				preview.Targets[0].Path = filepath.Join(dir, "workspace")
				release, err := workspacelease.Acquire(preview.Targets[0].Path, false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				stage, attempted, completed = "ownership_and_leases", 0, 0
			case "ownership-unavailable":
				preview.Targets[0].Path = filepath.Join(dir, "workspace")
				if err := os.WriteFile(filepath.Join(dir, "agents"), []byte("not a registry directory"), 0600); err != nil {
					t.Fatal(err)
				}
				stage, attempted, completed = "ownership_and_leases", 0, 0
			}
			result, err := New([]integration.CleanupProvider{p}).Execute(ctx, preview, options)
			if (err == nil) != (mode == "success") || p.calls != attempted || len(result.Targets) != completed || result.ExecutionObservation == nil || result.ExecutionObservation.Stage != stage || result.ExecutionObservation.AttemptedTargets != attempted {
				t.Fatalf("%s: calls=%d result=%+v observation=%+v error=%v", mode, p.calls, result, result.ExecutionObservation, err)
			}
			status := "succeeded"
			if err != nil {
				status = "failed"
				if completed > 0 {
					status = "partial"
				}
			}
			if !protocol.ValidCleanupExecution(*result.ExecutionObservation, status, completed, len(preview.Targets)) {
				t.Fatal("native result invalid", result.ExecutionObservation)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "execution") || strings.Contains(string(raw), "attempted_targets") || strings.Contains(string(raw), "private") {
				t.Fatal("private internal observation serialized", string(raw))
			}
			var forged protocol.CleanupResult
			if json.Unmarshal([]byte(`{"execution_observation":{"stage":"completed","attempted_targets":3}}`), &forged) != nil || forged.ExecutionObservation != nil {
				t.Fatal("wire injected native observation")
			}
		})
	}
}
