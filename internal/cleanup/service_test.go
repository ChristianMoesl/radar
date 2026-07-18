package cleanup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type fakeProvider struct {
	name     string
	targets  []protocol.CleanupTarget
	calls    *[]string
	forces   *[]bool
	failPath string
}

func (f fakeProvider) Name() string { return f.name }
func (fakeProvider) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (f fakeProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return append([]protocol.CleanupTarget(nil), f.targets...), nil
}
func (f fakeProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, f.name+":"+req.Target.Path)
	}
	if f.forces != nil {
		*f.forces = append(*f.forces, req.Force)
	}
	if req.Target.Path == f.failPath {
		return protocol.CleanupTarget{}, errors.New("failed")
	}
	return req.Target, nil
}

func TestPreviewPreservesProviderOrder(t *testing.T) {
	service := New([]integration.CleanupProvider{
		fakeProvider{name: "tmux", targets: []protocol.CleanupTarget{{Source: "tmux", Path: "/one"}}},
		fakeProvider{name: "git", targets: []protocol.CleanupTarget{{Source: "git", Path: "/two"}}},
	})
	preview, err := service.Preview(context.Background(), protocol.Task{ID: 7, Title: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{preview.Targets[0].Source, preview.Targets[1].Source}; !reflect.DeepEqual(got, []string{"tmux", "git"}) {
		t.Fatalf("target order = %v", got)
	}
}

func TestPreviewRejectsTaskWithoutTargets(t *testing.T) {
	_, err := New([]integration.CleanupProvider{fakeProvider{name: "git"}}).Preview(context.Background(), protocol.Task{})
	if err == nil {
		t.Fatal("Preview() error = nil")
	}
}

func TestExecutePassesForceAndReportsPartialCompletion(t *testing.T) {
	var calls []string
	var forces []bool
	service := New([]integration.CleanupProvider{fakeProvider{name: "fake", calls: &calls, forces: &forces, failPath: "/two"}})
	preview := protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{
		{Source: "fake", Path: "/one"},
		{Source: "fake", Path: "/two"},
		{Source: "fake", Path: "/three"},
	}}
	result, err := service.Execute(context.Background(), preview, ExecuteOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "stopped after 1 of 3") {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.Targets) != 1 || !reflect.DeepEqual(calls, []string{"fake:/one", "fake:/two"}) {
		t.Fatalf("result = %+v, calls = %v", result, calls)
	}
	if !reflect.DeepEqual(forces, []bool{true, true}) {
		t.Fatalf("forces = %v", forces)
	}
}

func TestExecuteRejectsUnknownSource(t *testing.T) {
	result, err := New(nil).Execute(context.Background(), protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{{Source: "unknown"}}}, ExecuteOptions{})
	if err == nil || result.TaskID != 7 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}
