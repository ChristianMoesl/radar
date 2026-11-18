package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sessionlayout "radar/internal/integration/tmux/layout"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/workspacelease"
)

type layoutNativeRunner struct {
	*paneGateRunner
	sessions    map[string]sessionlease.Identity
	effect      func(context.Context, []string) error
	afterCreate func()
}

func testLayoutAdmission(t *testing.T) *sessionlease.Set {
	t.Helper()
	admission := sessionlease.New(false)
	t.Cleanup(admission.Close)
	return admission
}

func layoutNativeFixture(t *testing.T) *layoutNativeRunner {
	t.Helper()
	sessionLeasePaths(t, false)
	return &layoutNativeRunner{paneGateRunner: &paneGateRunner{}, sessions: map[string]sessionlease.Identity{}}
}
func (r *layoutNativeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if args[0] == "display-message" {
		selector := strings.TrimSuffix(strings.TrimPrefix(args[3], "="), ":")
		id := r.sessions[selector]
		if args[4] == "#{session_name}" {
			return id.Name + "\n", nil
		}
		if id.ID == "" {
			return "12::\n", nil
		}
		return fmt.Sprintf("%s:%s:%s\n", id.ServerPID, id.Created, id.ID), nil
	}
	if r.effect != nil {
		if err := r.effect(ctx, args); err != nil {
			return "", err
		}
	}
	output, err := r.paneGateRunner.Run(ctx, cwd, name, args...)
	if err != nil {
		return output, err
	}
	if args[0] == "new-session" {
		id := sessionlease.Identity{ServerPID: "12", Created: "34", ID: "$9", Name: args[3]}
		r.sessions[id.Name], r.sessions[id.ID] = id, id
		if r.afterCreate != nil {
			r.afterCreate()
		}
	}
	return output, nil
}
func layoutAdmissionConfig() sessionlayout.Config {
	return sessionlayout.Config{Windows: []sessionlayout.Window{{Name: "pi", Panes: []sessionlayout.Pane{{Command: "pi " + sessionlayout.PiArgsPlaceholder}, {Command: "editor ."}}}, {Name: "other", Panes: []sessionlayout.Pane{{Command: "sleep 120"}}}}}
}
func assertLayoutAdmissionReleased(t *testing.T) {
	t.Helper()
	for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:tmux:session:12:34:$9"} {
		release, err := workspacelease.AcquireTmux(key, true)
		if err != nil {
			t.Fatal("constructor leaked admission", key, err)
		}
		release()
	}
}
func TestLayoutConstructorRejectsNativeCleanupBeforeCreation(t *testing.T) {
	r := layoutNativeFixture(t)
	admission := testLayoutAdmission(t)
	release, err := workspacelease.AcquireTmux("name:workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = createTmuxWorkspace(context.Background(), r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, admission)
	release()
	if err == nil || len(r.calls) != 0 {
		t.Fatal("constructor bypassed native cleanup", err, r.calls)
	}
	admission.Close()
	assertLayoutAdmissionReleased(t)
}
func TestLayoutConstructorRejectsAnAlreadyLiveExactName(t *testing.T) {
	r := layoutNativeFixture(t)
	admission := testLayoutAdmission(t)
	id := sessionlease.Identity{ServerPID: "12", Created: "34", ID: "$9", Name: "workspace"}
	r.sessions[id.Name], r.sessions[id.ID] = id, id
	if _, err := createTmuxWorkspace(context.Background(), r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, admission); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal("constructor accepted an existing runtime", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("constructor modified an existing runtime", r.calls)
	}
	admission.Close()
	assertLayoutAdmissionReleased(t)
}

func TestLayoutConstructorRetainsAdmissionAndPinsEveryOutcome(t *testing.T) {
	for _, early := range []bool{false, true} {
		for _, outcome := range []string{"success", "create-error", "pane-error", "cancelled"} {
			t.Run(fmt.Sprint("early=", early, "/", outcome), func(t *testing.T) {
				r := layoutNativeFixture(t)
				admission := testLayoutAdmission(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				checked := 0
				cleanup := false
				r.effect = func(ctx context.Context, args []string) error {
					checked++
					if release, err := workspacelease.AcquireTmux("name:workspace", true); err == nil {
						release()
						t.Error("constructor omitted name admission", args)
					}
					if args[0] != "new-session" {
						if release, err := workspacelease.AcquireTmux("runtime:tmux:session:12:34:$9", true); err == nil {
							release()
							t.Error("constructor omitted runtime admission", args)
						}
						if (args[0] == "new-window" && args[2] != "$9:") || (args[0] == "kill-session" && args[2] != "$9") {
							t.Error("native target not pinned", args)
						}
					}
					if args[0] == "kill-session" {
						cleanup = true
						return nil
					}
					if outcome == "create-error" && args[0] == "new-session" {
						return errors.New("fixture creation failed")
					}
					if args[0] == "split-window" {
						if outcome == "pane-error" {
							return errors.New("fixture pane failed")
						}
						if outcome == "cancelled" {
							cancel()
							return ctx.Err()
						}
					}
					return nil
				}
				created, err := createTmuxWorkspaceWithGates(ctx, r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, early, admission)
				if checked == 0 || (err != nil) != (outcome != "success") {
					t.Fatal("unexpected constructor outcome", checked, err)
				}
				if cleanup != (outcome == "pane-error" || outcome == "cancelled") {
					t.Fatal("incorrect verified rollback", cleanup, err)
				}
				if err == nil {
					if created.Key() != "tmux:session:12:34:$9" {
						t.Fatal("constructor lost creation identity", created)
					}
					for _, key := range []string{"name:workspace", "runtime:" + created.Key()} {
						if release, err := workspacelease.AcquireTmux(key, true); err == nil {
							release()
							t.Error("constructor released caller-owned admission", key)
						}
					}
				}
				admission.Close()
				assertLayoutAdmissionReleased(t)
			})
		}
	}
}
func TestLayoutConstructorCapturesCreationAndRejectsReplacement(t *testing.T) {
	for _, change := range []string{"restart", "replacement", "missing", "rename-and-reuse"} {
		t.Run(change, func(t *testing.T) {
			r := layoutNativeFixture(t)
			admission := testLayoutAdmission(t)
			r.afterCreate = func() {
				id := r.sessions["$9"]
				switch change {
				case "restart":
					id.ServerPID = "99"
				case "replacement":
					id.Created = "99"
				case "missing":
					delete(r.sessions, "$9")
					delete(r.sessions, "workspace")
					return
				case "rename-and-reuse":
					id.Name = "renamed-runtime"
					replacement := sessionlease.Identity{ServerPID: "12", Created: "99", ID: "$11", Name: "workspace"}
					r.sessions[replacement.Name], r.sessions[replacement.ID] = replacement, replacement
					r.sessions[id.Name], r.sessions[id.ID] = id, id
					return
				}
				r.sessions[id.Name], r.sessions[id.ID] = id, id
			}
			_, err := createTmuxWorkspace(context.Background(), r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, admission)
			if change == "rename-and-reuse" {
				if err != nil {
					t.Fatal("original renamed runtime lost", err)
				}
				assertCalledContains(t, r.calls, "tmux", "new-window -t $9:")
			} else {
				if err == nil {
					t.Fatal("changed creation accepted")
				}
				assertNotCalledContains(t, r.calls, "tmux", "new-window")
				assertNotCalledContains(t, r.calls, "tmux", "kill-session")
			}
			admission.Close()
			assertLayoutAdmissionReleased(t)
		})
	}
}
func TestLayoutConstructorRevalidatesBetweenPaneEffects(t *testing.T) {
	r := layoutNativeFixture(t)
	admission := testLayoutAdmission(t)
	r.effect = func(_ context.Context, args []string) error {
		if args[0] == "split-window" {
			id := r.sessions["$9"]
			id.Created = "99"
			r.sessions[id.ID], r.sessions[id.Name] = id, id
		}
		return nil
	}
	if _, err := createTmuxWorkspace(context.Background(), r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, admission); err == nil {
		t.Fatal("replacement between pane effects accepted")
	}
	assertNotCalledContains(t, r.calls, "tmux", "new-window")
	assertNotCalledContains(t, r.calls, "tmux", "kill-session")
	admission.Close()
	assertLayoutAdmissionReleased(t)
}

func TestLayoutConstructorNeverRollsBackAnUnidentifiedCreation(t *testing.T) {
	for _, output := range []string{"", "bad IDs", "12:: @1 %1", "12:34:workspace @1 %1"} {
		t.Run(output, func(t *testing.T) {
			r := layoutNativeFixture(t)
			admission := testLayoutAdmission(t)
			r.output = func(args []string) (string, bool) { return output, args[0] == "new-session" }
			if _, err := createTmuxWorkspace(context.Background(), r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, admission); err == nil {
				t.Fatal("unidentified creation accepted")
			}
			assertNotCalledContains(t, r.calls, "tmux", "kill-session")
			assertNotCalledContains(t, r.calls, "tmux", "split-window")
			admission.Close()
			assertLayoutAdmissionReleased(t)
		})
	}
}

func TestLayoutConstructorRollbackAfterPostCreationCancellation(t *testing.T) {
	r := layoutNativeFixture(t)
	admission := testLayoutAdmission(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.afterCreate = cancel
	if _, err := createTmuxWorkspace(ctx, r, "", "/workspace", "workspace", layoutAdmissionConfig(), "", nil, admission); !errors.Is(err, context.Canceled) {
		t.Fatal("creation cancellation lost", err)
	}
	assertCalledContains(t, r.calls, "tmux", "kill-session -t $9")
	assertNotCalledContains(t, r.calls, "tmux", "split-window")
	admission.Close()
	assertLayoutAdmissionReleased(t)
}

type layoutNativeHook struct {
	isolatedPaneTmux
	afterCreate func(string) error
	before      func([]string) error
}

func (r layoutNativeHook) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if r.before != nil {
		if err := r.before(args); err != nil {
			return "", err
		}
	}
	output, err := r.isolatedPaneTmux.Run(ctx, cwd, name, args...)
	if err == nil && args[0] == "new-session" && r.afterCreate != nil {
		err = r.afterCreate(output)
	}
	return output, err
}

func TestLayoutConstructorNativeRenameReuseAndVerifiedRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("rollback=", fail), func(t *testing.T) {
			sessionLeasePaths(t, false)
			path, ctx := t.TempDir(), context.Background()
			native := newIsolatedPaneTmux(t, path)
			var expected, originalID string
			checked := false
			sentinel := errors.New("fixture pane failure")
			r := layoutNativeHook{isolatedPaneTmux: native}
			r.afterCreate = func(output string) error {
				fields := strings.Fields(output)
				if len(fields) != 3 {
					return fmt.Errorf("unexpected native creation output: %q", output)
				}
				expected = "tmux:session:" + fields[0]
				originalID = strings.Split(fields[0], ":")[2]
				if _, err := native.Run(ctx, path, "tmux", "rename-session", "-t", originalID, "renamed-runtime"); err != nil {
					return err
				}
				_, err := native.Run(ctx, path, "tmux", "new-session", "-d", "-s", "workspace", "sleep 120")
				return err
			}
			r.before = func(args []string) error {
				if args[0] != "new-window" && args[0] != "kill-session" {
					return nil
				}
				checked = true
				for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:" + expected} {
					if release, err := workspacelease.AcquireTmux(key, true); err == nil {
						release()
						t.Error("native constructor omitted admission", key)
					}
				}
				if strings.TrimSuffix(args[2], ":") != originalID {
					t.Error("native effect followed reused name", args)
				}
				if fail && args[0] == "new-window" {
					return sentinel
				}
				return nil
			}
			cfg := sessionlayout.Config{Windows: []sessionlayout.Window{
				{Name: "pi", Panes: []sessionlayout.Pane{{Command: "sleep 120 # " + sessionlayout.PiArgsPlaceholder}}},
				{Name: "other", Panes: []sessionlayout.Pane{{Command: "sleep 120"}}},
			}}
			admission := testLayoutAdmission(t)
			_, err := createTmuxWorkspace(ctx, r, path, path, "workspace", cfg, "", nil, admission)
			if !checked || (err != nil) != fail || (fail && !errors.Is(err, sentinel)) {
				t.Fatal("native constructor outcome", checked, err)
			}
			if output, err := native.Run(ctx, path, "tmux", "display-message", "-p", "-t", "=workspace:", "#{session_windows}"); err != nil || output != "1" {
				t.Fatal("constructor modified reused name", output, err)
			}
			if output, err := native.Run(ctx, path, "tmux", "has-session", "-t", originalID); (err != nil) != fail {
				t.Fatal("verified rollback affected wrong runtime", output, err)
			}
			admission.Close()
			for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:" + expected} {
				release, err := workspacelease.AcquireTmux(key, true)
				if err != nil {
					t.Fatal("native constructor leaked admission", key, err)
				}
				release()
			}
		})
	}
}
