package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type admissionRunner struct {
	sessions map[string]sessionlease.Identity
	killed   []string
	switched []string
	kill     func() error
	path     string
}

func (*admissionRunner) LookPath(string) error { return nil }
func (r *admissionRunner) Run(ctx context.Context, _, _ string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch args[0] {
	case "display-message":
		selector := strings.TrimPrefix(strings.TrimSuffix(args[3], ":"), "=")
		id := r.sessions[selector]
		if args[4] == "#{session_name}" {
			return id.Name + "\n", nil
		}
		if id.ID == "" {
			return "12::\n", nil
		}
		return fmt.Sprintf("%s:%s:%s\n", id.ServerPID, id.Created, id.ID), nil
	case "kill-session":
		r.killed = append(r.killed, args[2])
		if r.kill != nil {
			return "", r.kill()
		}
	case "list-sessions":
		var rows []string
		for selector, id := range r.sessions {
			if selector == id.ID {
				rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t0\t1\t%s", id.ServerPID, id.Created, id.ID, id.Name, r.path))
			}
		}
		return strings.Join(rows, "\n"), nil
	case "switch-client":
		r.switched = append(r.switched, args[2])
	}
	return "", nil
}
func admissionFixture(t *testing.T) *admissionRunner {
	t.Helper()
	setupHome(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	r := &admissionRunner{sessions: map[string]sessionlease.Identity{}}
	for i, name := range []string{"workspace-alpha", "workspace-beta"} {
		id := sessionlease.Identity{ServerPID: "12", Created: "34", ID: fmt.Sprintf("$%d", i+7), Name: name}
		for _, alias := range []string{name, id.ID, name[:len(name)-1], name + "*"} {
			r.sessions[alias] = id
		}
	}
	return r
}
func sessionTarget(r *admissionRunner, name string) protocol.CleanupTarget {
	id := r.sessions[name]
	return protocol.CleanupTarget{Source: "tmux", Kind: "session", ResourceID: id.ID, Title: name, SourceRefID: id.Key()}
}

type fixtureAdmissionProvider struct {
	Source
	runner *admissionRunner
}

func (p fixtureAdmissionProvider) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	if mode != integration.CleanupRetire {
		// Model observed disappearance independently of command exit, retaining
		// cached identities for the existing held/released admission probes.
		return admitCleanup(ctx, &retirementOutcomeRunner{base: p.runner}, targets, mode, true)
	}
	return admitCleanup(ctx, p.runner, targets, mode, true)
}

type effectProvider struct {
	calls *int
	run   func() error
}

func (effectProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "fixture"}
}
func (effectProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (effectProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}
func (p effectProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.calls++
	if p.run != nil {
		return req.Target, p.run()
	}
	return req.Target, nil
}
func TestCleanupAdmitsWholeNativeBundleBeforeAnyProviderEffect(t *testing.T) {
	for _, selector := range []string{"workspace-beta", "=workspace-beta", "workspace-bet", "workspace-beta*", "$8"} {
		t.Run(selector, func(t *testing.T) {
			r := admissionFixture(t)
			active, err := prepareAttach(context.Background(), r, integration.SessionTarget{Name: selector})
			if err != nil {
				t.Fatal(err)
			}
			defer active.Release()
			effects := 0
			service := cleanup.New([]integration.CleanupProvider{effectProvider{calls: &effects}, fixtureAdmissionProvider{runner: r}})
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
				{Source: "fixture", ResourceID: "first-effect"}, sessionTarget(r, "workspace-alpha"), sessionTarget(r, "workspace-beta"),
			}}
			result, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{})
			if err == nil || effects != 0 || len(r.killed) != 0 || len(result.Targets) != 0 {
				t.Fatal("partial effects before complete admission", result, err, effects, r.killed)
			}
			// The first runtime was admitted before the second failed: it must unwind.
			probe := sessionlease.New(true)
			defer probe.Close()
			if _, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "$7"}); err != nil {
				t.Fatal("partial admission leaked", err)
			}
		})
	}
}
func TestCleanupHoldsNativeBundleThroughLaterProvidersAndReleases(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			r := admissionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			effects := 0
			effect := effectProvider{calls: &effects, run: func() error {
				for _, selector := range []string{"workspace-alpha", "$7", "workspace-alph", "workspace-alpha*"} {
					attachment, err := prepareAttach(context.Background(), r, integration.SessionTarget{Name: selector})
					if err == nil {
						attachment.Release()
						t.Error("foreground entered cleanup's retained bundle", selector)
					}
				}
				// No path/name overlap: independent navigation remains usable.
				attachment, err := prepareAttach(context.Background(), r, integration.SessionTarget{Name: "workspace-beta"})
				if err != nil {
					t.Fatal("unrelated foreground blocked", err)
				}
				attachment.Release()
				if outcome == "failure" {
					return errors.New("fixture later provider failed")
				}
				if outcome == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			service := cleanup.New([]integration.CleanupProvider{fixtureAdmissionProvider{runner: r}, effect})
			target := sessionTarget(r, "workspace-alpha")
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target, target, {Source: "fixture"}}}
			result, err := service.Execute(ctx, preview, cleanup.ExecuteOptions{})
			// Both native targets return, but the duplicate is now an observed
			// absence no-op, not permission to repeat the first kill.
			if (err != nil) != (outcome != "success") || effects != 1 || len(r.killed) != 1 {
				t.Fatal(result, err, effects, r.killed)
			}
			if len(result.Targets) < 2 || result.Targets[0].ResourceID != target.ResourceID {
				t.Fatal("provider targets deduplicated or rewritten", result)
			}
			probe := sessionlease.New(true)
			defer probe.Close()
			if _, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "$7"}); err != nil {
				t.Fatal("bundle leaked", err)
			}
		})
	}
}
func TestCleanupRechecksIncarnationAndAbsenceBeforeNativeRemoval(t *testing.T) {
	for _, change := range []string{"restart", "name-reused", "appeared", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			r := admissionFixture(t)
			target := sessionTarget(r, "workspace-alpha")
			if change == "appeared" {
				delete(r.sessions, "$7")
				delete(r.sessions, "workspace-alpha")
			}
			bound, release, err := admitCleanup(context.Background(), r, []protocol.CleanupTarget{target}, integration.CleanupSafe, false)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch change {
			case "restart":
				id := r.sessions["$7"]
				id.ServerPID = "99"
				r.sessions["$7"] = id
			case "name-reused":
				delete(r.sessions, "$7")
				id := r.sessions["$8"]
				id.Name = "workspace-alpha"
				r.sessions[id.Name] = id
			case "appeared":
				id := sessionlease.Identity{ServerPID: "12", Created: "35", ID: "$9", Name: "workspace-alpha"}
				r.sessions["$7"] = id
				r.sessions["$9"] = id
				r.sessions[id.Name] = id
			case "cancelled":
				cancel()
			}
			_, err = bound.Cleanup(ctx, integration.CleanupRequest{Target: target})
			if err == nil || len(r.killed) != 0 {
				t.Fatal("stale runtime removed", err, r.killed)
			}
		})
	}
}

func TestRetirementPreservesRegistrationOwnershipAndAdmittedIncarnation(t *testing.T) {
	for _, outcome := range []string{"retired", "missing", "registration-changed", "foreign-path", "stale-preview"} {
		t.Run(outcome, func(t *testing.T) {
			r := admissionFixture(t)
			root, err := workspace.DefaultRoot()
			if err != nil {
				t.Fatal(err)
			}
			anchor := filepath.Join(root, "fixture")
			if err := os.MkdirAll(anchor, 0755); err != nil {
				t.Fatal(err)
			}
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "fixture", Path: anchor, SessionName: "workspace-alpha"}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			r.path = anchor
			task := protocol.Task{SourceRefs: []protocol.SourceRef{
				{Source: "workspace", Kind: "workspace", ProvidesWorkspace: true, WorkspaceID: group.ID, ID: "workspace:" + group.ID, Path: anchor},
				{Source: "tmux", Kind: "session", ID: r.sessions["$7"].Key(), Title: group.SessionName, Path: anchor},
			}}
			targets, err := workspace.RuntimeCleanupTargets(task, "tmux")
			if err != nil || len(targets) != 1 {
				t.Fatal(targets, err)
			}
			target := targets[0]
			target.Operation["runtime_retired"] = "original"
			if outcome == "missing" {
				for selector, id := range r.sessions {
					if id.ID == "$7" {
						delete(r.sessions, selector)
					}
				}
			}
			if outcome == "stale-preview" {
				target.Operation["runtime_id"] = "tmux:session:99:34:$7"
			}
			bound, release, err := admitCleanup(context.Background(), r, []protocol.CleanupTarget{target}, integration.CleanupRetire, false)
			if outcome == "stale-preview" {
				if err == nil {
					release()
					t.Fatal("stale collected identity admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			switch outcome {
			case "registration-changed":
				if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
					registry.Workspaces[0].SessionName = "replacement"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "foreign-path":
				r.path = t.TempDir()
			case "retired":
				// A successful native kill removes this lifetime, independently
				// of the fixture's recorded command exit/call count.
				r.kill = func() error {
					for selector, id := range r.sessions {
						if id.ID == "$7" {
							delete(r.sessions, selector)
						}
					}
					return nil
				}
			}
			result, err := bound.Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: integration.CleanupRetire})
			shouldFail := outcome == "registration-changed" || outcome == "foreign-path"
			if (err != nil) != shouldFail || len(r.killed) != map[bool]int{false: 0, true: 1}[outcome == "retired"] {
				t.Fatal(outcome, result, err, r.killed)
			}
			if target.Operation["runtime_retired"] != "original" {
				t.Fatal("retirement mutated original preview")
			}
			if !shouldFail && (result.Operation["runtime_retired"] == "true") != (outcome == "retired") {
				t.Fatal("retirement result was not stamped accurately", result)
			}
		})
	}
}

func TestPausedCleanupAllowsOnlyUnrelatedNativeNavigation(t *testing.T) {
	r := admissionFixture(t)
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	calls := 0
	service := cleanup.New([]integration.CleanupProvider{fixtureAdmissionProvider{runner: r}, effectProvider{calls: &calls, run: func() error { close(entered); <-resume; return nil }}})
	preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{sessionTarget(r, "workspace-alpha"), {Source: "fixture"}}}
	go func() {
		_, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{})
		done <- err
	}()
	defer func() {
		unblock()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("fixture cleanup did not exit")
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture cleanup did not reach paused provider")
	}
	for _, alias := range []string{"workspace-alpha", "=workspace-alpha", "workspace-alph", "workspace-alpha*", "$7"} {
		if err := switchSession(context.Background(), r, integration.SessionTarget{Name: alias}); err == nil {
			t.Error("same runtime entered paused cleanup", alias)
		}
		attachment, err := prepareAttach(context.Background(), r, integration.SessionTarget{Name: alias})
		if err == nil {
			attachment.Release()
			t.Error("same foreground runtime entered paused cleanup", alias)
		}
	}
	if err := switchSession(context.Background(), r, integration.SessionTarget{Name: "workspace-beta"}); err != nil {
		t.Fatal("unrelated switch blocked by paused cleanup", err)
	}
	attachment, err := prepareAttach(context.Background(), r, integration.SessionTarget{Name: "workspace-beta"})
	if err != nil {
		t.Fatal("unrelated foreground blocked by paused cleanup", err)
	}
	if err := attachment.Validate(); err != nil {
		attachment.Release()
		t.Fatal(err)
	}
	attachment.Release()
	if len(r.switched) != 1 || r.switched[0] != "$8" {
		t.Fatal("unrelated switch not pinned to admitted runtime", r.switched)
	}
}
