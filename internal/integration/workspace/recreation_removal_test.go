package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

// Command success and the observed lifetime are independent. No real SBX runs.
type recreationRemovalRunner struct {
	rows    []string
	calls   []string
	reads   int
	hook    func(string, int) error
	missing bool
}

func (r *recreationRemovalRunner) LookPath(string) error {
	if r.missing {
		return errors.New("fixture CLI unavailable")
	}
	return nil
}
func (r *recreationRemovalRunner) Run(_ context.Context, cwd, name string, args ...string) (string, error) {
	if cwd != "" {
		return "", errors.New("unexpected workspace cwd")
	}
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if args[0] == "ls" {
		r.reads++
	}
	if r.hook != nil {
		if err := r.hook(args[0], r.reads); err != nil {
			return "", err
		}
	}
	switch args[0] {
	case "ls":
		if r.reads > len(r.rows) {
			return "", errors.New("unexpected extra observation")
		}
		return r.rows[r.reads-1], nil
	case "rm":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected command %s", args[0])
	}
}

func TestRecreationRemovalRequiresObservedLifetimeAbsence(t *testing.T) {
	for _, scenario := range []string{"removed", "unrelated-after", "initial-absent", "gone-before", "lingering-then-absent", "remaining", "replacement-before", "replacement-after", "renamed-before", "renamed-after", "ambiguous-before", "ambiguous-after", "malformed-initial", "malformed-before", "malformed-after", "missing-id-initial", "missing-id-after", "duplicate-id-before", "duplicate-id-after", "unavailable-initial", "unavailable-before", "unavailable-after", "cancelled-initial", "cancelled-before", "cancelled-after", "cancelled-rm", "rm-error", "rm-not-found-error", "missing-cli", "pre-cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			native := `[{"name":"registered-name","id":"observed-lifetime"}]`
			renamed := `[{"name":"renamed-original","id":"observed-lifetime"}]`
			replacement := `[{"name":"registered-name","id":"replacement-lifetime"}]`
			runner := &recreationRemovalRunner{rows: []string{native, native, "[]"}}
			policy := sandboxReconcilePolicy{removalChecks: 1}
			success := scenario == "removed" || scenario == "unrelated-after" || scenario == "initial-absent" || scenario == "gone-before" || scenario == "lingering-then-absent"
			switch scenario {
			case "unrelated-after":
				runner.rows[2] = `[{"name":"unrelated","id":"unrelated-lifetime"}]`
			case "initial-absent":
				runner.rows[0] = "[]"
			case "gone-before":
				runner.rows[1] = "[]"
			case "lingering-then-absent":
				runner.rows = append([]string{native, native, native}, "[]")
				policy.removalChecks = 2
			case "remaining":
				runner.rows[2] = native
			case "replacement-before":
				runner.rows[1] = replacement
			case "replacement-after":
				runner.rows[2] = replacement
			case "renamed-before":
				runner.rows[1] = renamed
			case "renamed-after":
				runner.rows[2] = renamed
			case "ambiguous-before":
				runner.rows[1] = strings.TrimSuffix(native, "]") + "," + strings.TrimPrefix(replacement, "[")
			case "ambiguous-after":
				runner.rows[2] = strings.TrimSuffix(native, "]") + "," + strings.TrimPrefix(replacement, "[")
			case "duplicate-id-before":
				runner.rows[1] = strings.TrimSuffix(native, "]") + "," + strings.TrimPrefix(renamed, "[")
			case "duplicate-id-after":
				runner.rows[2] = strings.TrimSuffix(native, "]") + "," + strings.TrimPrefix(renamed, "[")
			case "malformed-initial":
				runner.rows[0] = "invalid"
			case "malformed-before":
				runner.rows[1] = "invalid"
			case "malformed-after":
				runner.rows[2] = "invalid"
			case "missing-id-initial":
				runner.rows[0] = `[{"name":"registered-name"}]`
			case "missing-id-after":
				runner.rows[2] = `[{"name":"registered-name"}]`
			case "missing-cli":
				runner.missing = true
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("fixture provider unavailable")
			if scenario == "pre-cancelled" {
				cancel()
			}
			runner.hook = func(command string, reads int) error {
				phase := "initial"
				if reads == 2 {
					phase = "before"
				}
				if reads >= 3 {
					phase = "after"
				}
				if command == "ls" && scenario == "unavailable-"+phase {
					return failure
				}
				if command == "ls" && scenario == "cancelled-"+phase {
					cancel()
				}
				if command == "rm" {
					if scenario == "rm-error" {
						return failure
					}
					if scenario == "rm-not-found-error" {
						return errors.New("fixture backend endpoint not found")
					}
					if scenario == "cancelled-rm" {
						cancel()
					}
				}
				return nil
			}
			err := removeSandboxForRecreation(ctx, runner, "registered-name", "registered-workspace", "fixture", nil, policy)
			if (err == nil) != success {
				t.Errorf("err=%v want success=%t calls=%v", err, success, runner.calls)
			}
			if strings.HasPrefix(scenario, "unavailable-") || scenario == "rm-error" {
				if !errors.Is(err, failure) {
					t.Error("native error identity lost", err)
				}
			}
			if strings.HasPrefix(scenario, "cancelled-") || scenario == "pre-cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Error("cancellation lost", err)
				}
			}
			want := []string{"sbx ls --json", "sbx ls --json", "sbx rm --force observed-lifetime", "sbx ls --json"}
			switch {
			case scenario == "missing-cli" || scenario == "pre-cancelled":
				want = nil
			case scenario == "initial-absent" || strings.HasSuffix(scenario, "-initial"):
				want = want[:1]
			case strings.HasSuffix(scenario, "-before"):
				want = want[:2]
			case strings.HasPrefix(scenario, "rm-") || scenario == "cancelled-rm":
				want = want[:3]
			case scenario == "lingering-then-absent":
				want = append(want, "sbx ls --json")
			}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Errorf("replayed, name-addressed or unobserved mutation: got=%v want=%v", runner.calls, want)
			}
		})
	}
}

type recreationRemovalBoundaryRunner struct {
	mount            string
	scenario         string
	removed          bool
	creates, removes int
	calls            []string
}

func (*recreationRemovalBoundaryRunner) LookPath(string) error { return nil }
func (r *recreationRemovalBoundaryRunner) Run(_ context.Context, _, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name != "sbx" {
		return "", fmt.Errorf("unexpected executable %s", name)
	}
	switch args[0] {
	case "version":
		return `{"client":{"version":"v0.48.0"}}`, nil
	case "ls":
		if r.removed {
			switch r.scenario {
			case "renamed":
				return fmt.Sprintf(`[{"name":"renamed-original","id":"original-lifetime","workspaces":[%q]}]`, r.mount), nil
			case "replacement":
				return fmt.Sprintf(`[{"name":"registered-name","id":"replacement-lifetime","workspaces":[%q]}]`, r.mount), nil
			case "malformed":
				return "invalid", nil
			}
		}
		return fmt.Sprintf(`[{"name":"registered-name","id":"original-lifetime","status":"running","workspaces":[%q,"/old-mount"]}]`, r.mount), nil
	case "rm":
		r.removed = true
		r.removes++
		if r.scenario == "not-found-error" {
			return "", errors.New("fixture backend endpoint not found")
		}
		return "", nil
	case "create":
		r.creates++
		return "", errors.New("invalid fixture kit")
	default:
		return "", fmt.Errorf("unexpected command %s", args[0])
	}
}

func TestRecreationRemovalUnverifiedOutcomeNeverStartsCreate(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, scenario := range []string{"renamed", "replacement", "malformed", "not-found-error"} {
		t.Run(scenario, func(t *testing.T) {
			mount := t.TempDir()
			sentinel := filepath.Join(mount, "retained")
			if err := os.WriteFile(sentinel, []byte("retained\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r := &recreationRemovalBoundaryRunner{mount: mount, scenario: scenario}
			group := workspacegroup.Workspace{ID: workspacegroup.ID(mount), Path: mount, Sandbox: &workspacegroup.Sandbox{Name: "registered-name", Agent: "shell", Mounts: []string{mount}}}
			err := reconcileSandboxWithPolicy(context.Background(), r, group, nil, sandboxReconcilePolicy{createAttempts: 3})
			if err == nil || r.creates != 0 || r.removes != 1 {
				t.Fatalf("uncertain removal crossed create/retry boundary err=%v creates=%d removes=%d calls=%v", err, r.creates, r.removes, r.calls)
			}
			for _, command := range r.calls {
				if strings.HasPrefix(command, "sbx rm ") && command != "sbx rm --force original-lifetime" {
					t.Error("removal addressed reusable name", r.calls)
				}
			}
			got, readErr := os.ReadFile(sentinel)
			if readErr != nil || string(got) != "retained\n" {
				t.Fatal("workspace contents changed", readErr)
			}
		})
	}
}
