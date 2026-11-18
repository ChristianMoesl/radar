package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestCleanupDeduplicatesAliasLeasesWithoutDroppingProviderTargets(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "provider-error"}[fail], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			physical := filepath.Join(root, "physical")
			if err := os.Mkdir(physical, 0700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(root, "alias")
			if err := os.Symlink(physical, alias); err != nil {
				t.Fatal(err)
			}
			var calls []string
			provider := fakeProvider{name: "fixture", calls: &calls}
			if fail {
				provider.failPath = alias
			}
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
				{Source: "fixture", Kind: "sandbox", Path: physical},
				{Source: "fixture", Kind: "workspace", Path: alias},
			}}
			result, err := New([]integration.CleanupProvider{provider}).Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupConfirmed})
			if (err != nil) != fail || !reflect.DeepEqual(calls, []string{"fixture:" + physical, "fixture:" + alias}) {
				t.Fatalf("alias lease admission lost provider targets: err=%v calls=%v", err, calls)
			}
			wantCompleted := 2
			if fail {
				wantCompleted = 1
			}
			if len(result.Targets) != wantCompleted {
				t.Fatal("lease deduplication changed completion accounting")
			}
			for _, path := range []string{physical, alias} {
				release, err := workspacelease.Acquire(path, true)
				if err != nil {
					t.Fatal("cleanup leaked an alias lease", err)
				}
				release()
			}
		})
	}
}

func TestCleanupAliasContentionAndInvalidIdentityFailBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"contended", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			physical, alias := filepath.Join(root, "physical"), filepath.Join(root, "alias")
			if kind == "contended" {
				if err := os.Mkdir(physical, 0700); err != nil {
					t.Fatal(err)
				}
				release, err := workspacelease.Acquire(physical, false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			if err := os.Symlink(physical, alias); err != nil {
				t.Fatal(err)
			}
			var calls []string
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Path: alias}}}
			_, err := New([]integration.CleanupProvider{fakeProvider{name: "fixture", calls: &calls}}).Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupConfirmed})
			if err == nil || len(calls) != 0 {
				t.Fatalf("unsafe cleanup dispatched: %v %v", err, calls)
			}
			if kind == "contended" && !strings.Contains(err.Error(), "in use by another operation") {
				t.Fatal("conflict lost its actionable explanation", err)
			}
			if kind == "dangling" && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("identity failure lost its cause", err)
			}
		})
	}
}

func TestCleanupChecksEachAliasOwnerBeforeDeduplicatingItsLease(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	physical, alias := filepath.Join(root, "physical"), filepath.Join(root, "alias")
	if err := os.Mkdir(physical, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(root, "agents", "fixture-owner")
	if err := os.MkdirAll(owner, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "fixture-owner", Workspace: protocol.AgentWorkspaceBinding{ID: "fixture", Path: alias, Mode: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "registration.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Path: physical}, {Source: "fixture", Path: alias}}}
	_, err = New([]integration.CleanupProvider{fakeProvider{name: "fixture", calls: &calls}}).Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupConfirmed})
	if err == nil || !strings.Contains(err.Error(), "associated with agent fixture-owner") || len(calls) != 0 {
		t.Fatalf("deduplication bypassed a later target's owner: %v %v", err, calls)
	}
	release, err := workspacelease.Acquire(physical, true)
	if err != nil {
		t.Fatal("owner rejection leaked the earlier lease", err)
	}
	release()
}
