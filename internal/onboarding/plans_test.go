package onboarding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/setup"
)

type testSetupProvider struct {
	name    string
	stage   integration.SetupStage
	prepare func(integration.SetupRequest) (setup.Plan, error)
}

func (p testSetupProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name}
}
func (p testSetupProvider) SetupStage() integration.SetupStage { return p.stage }
func (p testSetupProvider) Setup(_ context.Context, req integration.SetupRequest) (setup.Plan, error) {
	return p.prepare(req)
}

// An unfamiliar provider must participate without any source-name switches, and
// one conflicting plan must prevent writes by every previously prepared plan.
func TestProviderPlansAreReviewedAndCheckedBeforeAnyApply(t *testing.T) {
	for _, outcome := range []string{"save", "cancel", "conflict", "prepare error", "apply error"} {
		t.Run(outcome, func(t *testing.T) {
			w, ui, _, home := fixture(t)
			ui.decisions = []bool{outcome != "cancel"}
			var events []string
			planned := filepath.Join(home, "provider-settings")
			conflict := errors.New("provider file changed during review")
			first := testSetupProvider{name: "unfamiliar", stage: integration.SetupStage{Phase: integration.SetupDirectories}, prepare: func(req integration.SetupRequest) (setup.Plan, error) {
				events = append(events, "prepare first")
				req.Config.RepositoryDirs = []string{filepath.Join(home, "repos")}
				req.Config.Workspace.RootDir = filepath.Join(home, "workspaces")
				req.Config.Obsidian.VaultPath = filepath.Join(home, "notes")
				req.Completed("explicit foreground action")
				return setup.Plan{Files: []setup.File{{Path: planned, Purpose: "Provider settings", Action: "CREATE", Details: "safe preview"}}, Summary: "Provider summary", CheckUnchanged: func() error { events = append(events, "check first"); return nil }, Apply: func() error {
					events = append(events, "apply first")
					return os.WriteFile(planned, []byte("saved"), 0600)
				}, AfterSave: func(context.Context) {
					events = append(events, "after first")
					if needed, err := Needed(); err != nil || needed {
						t.Fatal("post-save callback ran before config publication")
					}
				}}, nil
			}}
			second := testSetupProvider{name: "another", stage: integration.SetupStage{Phase: integration.SetupConnections}, prepare: func(integration.SetupRequest) (setup.Plan, error) {
				events = append(events, "prepare second")
				if outcome == "prepare error" {
					return setup.Plan{}, conflict
				}
				return setup.Plan{CheckUnchanged: func() error {
					events = append(events, "check second")
					if outcome == "conflict" {
						return conflict
					}
					return nil
				}, Apply: func() error {
					events = append(events, "apply second")
					if outcome == "apply error" {
						return conflict
					}
					return nil
				}}, nil
			}}
			// Registration deliberately differs from phase order.
			w.registry = integration.NewRegistry(second, first)
			ui.beforeConfirm = func(title string) {
				if title != "Save this configuration?" {
					return
				}
				if ui.reviewed == nil || !strings.Contains(ui.reviewed.summaryText, "Provider summary") || len(ui.reviewed.files) != 3 {
					t.Fatal("provider plan absent from combined review")
				}
				if _, err := os.Stat(planned); !os.IsNotExist(err) {
					t.Fatal("provider wrote before review")
				}
				events = append(events, "review")
			}
			err := w.run()
			if outcome == "cancel" {
				if !errors.Is(err, setup.ErrAborted) {
					t.Fatal(err)
				}
			} else if outcome != "save" {
				if !errors.Is(err, conflict) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := []string{"prepare first", "prepare second"}
			if outcome != "prepare error" {
				want = append(want, "review")
			}
			if outcome != "cancel" && outcome != "prepare error" {
				want = append(want, "check first", "check second")
				if outcome != "conflict" {
					want = append(want, "apply first", "apply second")
					if outcome == "save" {
						want = append(want, "after first")
					}
				}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events=%v want=%v", events, want)
			}
			needed, _ := Needed()
			if needed != (outcome != "save") {
				t.Fatal("config completion marker disagrees with plan outcome")
			}
			if outcome == "cancel" || outcome == "conflict" || outcome == "prepare error" {
				if _, err := os.Stat(planned); !os.IsNotExist(err) {
					t.Fatal("rejected plan was applied")
				}
			}
		})
	}
}
