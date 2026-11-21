package cleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type capabilityAdmissionProvider struct {
	fakeProvider
	events *[]string
}

func (p capabilityAdmissionProvider) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	*p.events = append(*p.events, "admit:"+p.name)
	return p, func() { *p.events = append(*p.events, "release:"+p.name) }, nil
}

func (p capabilityAdmissionProvider) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	*p.events = append(*p.events, "preflight:"+p.name)
	return nil
}

func (p capabilityAdmissionProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.events = append(*p.events, "cleanup:"+p.name+":"+req.Target.ResourceID)
	return req.Target, os.Remove(req.Target.Path)
}

func capabilityAdmissionFixture(t *testing.T) (string, protocol.CleanupPreview) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	preview := protocol.CleanupPreview{TaskID: 7}
	for i, id := range []string{"one", "two", "three"} {
		path := filepath.Join(root, id)
		if err := os.WriteFile(path, []byte("preserve until the whole bundle is admitted"), 0600); err != nil {
			t.Fatal(err)
		}
		source := "git"
		if i == 1 {
			source = "workspace"
		}
		preview.Targets = append(preview.Targets, protocol.CleanupTarget{Source: source, Kind: "fixture", ResourceID: id, Path: path})
	}
	return root, preview
}

func TestCleanupRejectsUnsupportedOrAmbiguousBundleBeforeAnyProviderOrLease(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		for _, problem := range []string{"missing", "empty", "whitespace", "duplicate"} {
			for position := range 3 {
				t.Run(fmt.Sprintf("mode=%d/%s/position=%d", mode, problem, position), func(t *testing.T) {
					root, preview := capabilityAdmissionFixture(t)
					var events []string
					git := capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "git"}, events: &events}
					workspace := capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "workspace"}, events: &events}
					providers := []integration.CleanupProvider{git, workspace}
					source := "missing"
					switch problem {
					case "empty":
						source = ""
					case "whitespace":
						source = "git "
					case "duplicate":
						source = preview.Targets[position].Source
						providers = append(providers, capabilityAdmissionProvider{fakeProvider: fakeProvider{name: source}, events: &events})
					}
					preview.Targets[position].Source = source
					result, err := New(providers).Execute(context.Background(), preview, ExecuteOptions{Mode: mode})
					if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("source %q", source)) || !strings.Contains(err.Error(), "stopped after 0 of 3") || result.TaskID != preview.TaskID || len(result.Targets) != 0 || len(events) != 0 {
						t.Fatalf("bundle capability was not rejected before callbacks: result=%+v events=%v error=%v", result, events, err)
					}
					if result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "admission" || result.ExecutionObservation.AttemptedTargets != 0 || !protocol.ValidCleanupExecution(*result.ExecutionObservation, "failed", 0, len(preview.Targets)) {
						t.Fatalf("unsupported bundle reported later dispatch: %+v", result.ExecutionObservation)
					}
					for _, target := range preview.Targets {
						if data, readErr := os.ReadFile(target.Path); readErr != nil || string(data) != "preserve until the whole bundle is admitted" {
							t.Fatalf("invalid whole bundle changed resource %q: %q %v", target.ResourceID, data, readErr)
						}
					}
					if _, statErr := os.Stat(filepath.Join(root, "leases")); !os.IsNotExist(statErr) {
						t.Fatalf("invalid bundle created execution leases: %v", statErr)
					}
				})
			}
		}
	}
}

func TestCleanupCapabilityAdmissionPreservesValidProviderAndTargetOrder(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			_, preview := capabilityAdmissionFixture(t)
			var events []string
			git := capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "git"}, events: &events}
			workspace := capabilityAdmissionProvider{fakeProvider: fakeProvider{name: "workspace"}, events: &events}
			result, err := New([]integration.CleanupProvider{git, workspace}).Execute(context.Background(), preview, ExecuteOptions{Mode: mode})
			want := []string{"admit:git", "admit:workspace", "preflight:git", "preflight:workspace", "cleanup:git:one", "cleanup:workspace:two", "cleanup:git:three", "release:git", "release:workspace"}
			if err != nil || !reflect.DeepEqual(result.Targets, preview.Targets) || !reflect.DeepEqual(events, want) || result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "completed" || result.ExecutionObservation.AttemptedTargets != 3 {
				t.Fatalf("valid provider/target order changed: result=%+v events=%v error=%v", result, events, err)
			}
			for _, target := range preview.Targets {
				if _, statErr := os.Stat(target.Path); !os.IsNotExist(statErr) {
					t.Fatalf("valid fixture target was not dispatched: %s %v", target.ResourceID, statErr)
				}
			}
		})
	}
}
