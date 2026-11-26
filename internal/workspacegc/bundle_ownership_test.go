package workspacegc

import (
	"context"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

type gcBundleOwnershipProvider struct {
	retirementOwnedProvider
	boundary string
	borrowed *[]protocol.CleanupTarget
}

func rewriteGCBorrowedTargets(targets []protocol.CleanupTarget) {
	for i := range targets {
		targets[i].Path += "-replacement"
		if targets[i].Operation != nil {
			targets[i].Operation["lifetime"] = "replacement"
			targets[i].Operation["runtime_retired"] = "false"
		}
		if len(targets[i].Safety) != 0 {
			targets[i].Safety[0].Summary = "replacement"
		}
	}
}

func (p gcBundleOwnershipProvider) AdmitCleanup(_ context.Context, targets []protocol.CleanupTarget, _ integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	if p.boundary == "admission" {
		rewriteGCBorrowedTargets(targets)
	}
	return p, func() { rewriteGCBorrowedTargets(*p.borrowed) }, nil
}

func (p gcBundleOwnershipProvider) PreflightCleanup(_ context.Context, targets []protocol.CleanupTarget, _ integration.CleanupMode) error {
	if p.boundary == "preflight" {
		rewriteGCBorrowedTargets(targets)
	}
	return nil
}

func (p gcBundleOwnershipProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	observed := req.Target
	observed.Operation, observed.Safety = maps.Clone(observed.Operation), slices.Clone(observed.Safety)
	*p.observed = append(*p.observed, observed)
	result, err := p.candidateBoundaryProvider.Cleanup(ctx, req)
	if p.boundary == "dispatch" {
		req.Target.Operation["lifetime"] = "provider-result"
		req.Target.Safety[0].Summary = "provider-result"
	}
	if p.name == "tmux" {
		result.Operation = map[string]string{"runtime_retired": "true", "lifetime": "native-result"}
		result.Safety = []protocol.CleanupSafety{{Kind: "notice", Summary: "native-result"}}
	}
	*p.borrowed = append(*p.borrowed, result)
	return result, err
}

func TestGCPlansKeepBundleAndRuntimeEvidenceOwned(t *testing.T) {
	for _, stage := range []string{"retire", "expire"} {
		for _, boundary := range []string{"admission", "preflight", "dispatch"} {
			t.Run(stage+"/"+boundary, func(t *testing.T) {
				store, root, done := candidateBoundaryFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
				now := done.Add(25 * time.Hour)
				if stage == "expire" {
					now = done.Add(ExpiryRetention)
				}
				guards, previews := 0, 0
				var calls []cleanupCall
				var modes []integration.CleanupMode
				var observed, borrowed []protocol.CleanupTarget
				options := Options{WorkspaceRoot: root, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
					guards++
					return execute()
				}}
				candidates, err := BuildPlan(store, now, options)
				if err != nil {
					t.Fatal(err)
				}
				var candidate Candidate
				for _, c := range candidates.Candidates {
					if c.WorkspaceID == "registered" {
						candidate = c
					}
				}
				var providers []integration.CleanupProvider
				for _, name := range []string{"tmux", "sbx", "git", "workspace"} {
					providers = append(providers, gcBundleOwnershipProvider{retirementOwnedProvider: retirementOwnedProvider{candidateBoundaryProvider: candidateBoundaryProvider{gcProvider: gcProvider{name: name, calls: &calls}, previews: &previews, modes: &modes}, observed: &observed}, boundary: boundary, borrowed: &borrowed})
				}
				service := cleanup.New(providers)
				var plan interface {
					Preview() protocol.CleanupPreview
					Execute(context.Context, *state.Store, cleanup.Service) (protocol.CleanupResult, bool, error)
				}
				if stage == "retire" {
					plan, err = PrepareRetirement(context.Background(), store, service, candidate, now, options)
				} else {
					plan, err = PrepareExpiry(context.Background(), store, service, candidate, now, options)
				}
				if err != nil {
					t.Fatal(err)
				}
				before := plan.Preview()
				result, changed, err := plan.Execute(context.Background(), store, service)
				if err != nil || !changed || guards != 1 || previews != 4 || len(result.Targets) != len(before.Targets) || len(calls) != len(before.Targets) {
					t.Fatalf("native plan/evidence changed: %+v %v changed=%t calls=%v guards=%d previews=%d", result, err, changed, calls, guards, previews)
				}
				if !reflect.DeepEqual(before, plan.Preview()) {
					t.Fatal("execution callback rewrote retained native plan")
				}
				for i, target := range before.Targets {
					if calls[i].source != target.Source || calls[i].path != target.Path || observed[i].Operation["lifetime"] != "original" || observed[i].Safety[0].Summary != "original" {
						t.Fatalf("callback redirected exact native bundle at %d: %v %+v", i, calls, observed)
					}
				}
				if result.Targets[0].Operation["runtime_retired"] != "true" || result.Targets[0].Safety[0].Summary != "native-result" || borrowed[0].Operation["runtime_retired"] != "false" {
					t.Fatalf("release rewrote completed runtime observation: %+v borrowed=%+v", result, borrowed)
				}
			})
		}
	}
}
