package sourceactions

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type openingProvider struct {
	targets map[string][]integration.OpenResource
	err     error
}

func (openingProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "example"}
}
func (p openingProvider) OpenResources(_ context.Context, ref protocol.SourceRef) ([]integration.OpenResource, error) {
	return p.targets[ref.ID], p.err
}

func openingFixture() (openingProvider, protocol.Task) {
	task := protocol.Task{ID: 17, SourceRefs: []protocol.SourceRef{
		{ID: "workspace:one", Source: "workspace", ProvidesWorkspace: true, WorkspaceID: "one"},
		{ID: "ref1", Source: "example"}, {ID: "ref2", Source: "example"},
	}}
	p := openingProvider{targets: map[string][]integration.OpenResource{}}
	for _, ref := range []string{"ref1", "ref2"} {
		p.targets[ref] = []integration.OpenResource{
			{ID: ref + ":changes", Source: "example", SourceRefID: ref, Label: ref, URL: "https://example.test/" + ref},
			{ID: "repository", Source: "example", SourceRefID: ref, Label: "Repository", URL: "https://example.test/repository"},
		}
	}
	return p, task
}

func TestWorkspaceTaskUsesUnambiguousStableID(t *testing.T) {
	_, task := openingFixture()
	other := protocol.Task{ID: 18, Title: task.Title, SourceRefs: []protocol.SourceRef{{ID: "w:other", ProvidesWorkspace: true, WorkspaceID: "other"}}}
	got, err := WorkspaceTask([]protocol.Task{other, task}, "one")
	if err != nil || got.ID != 17 {
		t.Fatalf("%#v %v", got, err)
	}
	for _, tasks := range [][]protocol.Task{nil, {other}, {task, task}} {
		if _, err := WorkspaceTask(tasks, "one"); err == nil {
			t.Fatalf("accepted %#v", tasks)
		}
	}
	if _, err := WorkspaceTask([]protocol.Task{task}, ""); err == nil {
		t.Fatal("empty identity")
	}
}

func TestOpeningManifestSelectionAndDrift(t *testing.T) {
	p, task := openingFixture()
	providers := []integration.ResourceOpenProvider{p}
	manifest, err := OpenableResources(context.Background(), providers, task)
	if err != nil || len(manifest) != 3 {
		t.Fatalf("%#v %v", manifest, err)
	}
	req := integration.ResourceOpenRequest{ResourceIDs: []string{"ref2:changes", "ref1:changes", "ref2:changes"}}
	plan, err := PreviewResourceOpen(context.Background(), providers, "one", task, req)
	if err != nil || !plan.RequiresConfirmation || len(plan.Resources) != 2 || plan.Resources[0].ID != "ref2:changes" {
		t.Fatalf("%#v %v", plan, err)
	}
	var launched []string
	launch := func(_ context.Context, url string) error { launched = append(launched, url); return nil }
	if _, err := ApplyResourceOpen(context.Background(), plan, "", launch); err == nil || len(launched) > 0 {
		t.Fatal("missing confirmation accepted")
	}
	p.targets["ref2"][0].URL = "https://example.test/changed"
	changed, err := PreviewResourceOpen(context.Background(), providers, "one", task, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyResourceOpen(context.Background(), changed, plan.PlanID, launch); err == nil || len(launched) > 0 {
		t.Fatal("stale plan applied")
	}
	result, err := ApplyResourceOpen(context.Background(), plan, plan.PlanID, launch)
	if err != nil || !result.OK || !reflect.DeepEqual(launched, []string{"https://example.test/ref2", "https://example.test/ref1"}) {
		t.Fatalf("%#v %#v %v", result, launched, err)
	}
}

func TestOpeningInvalidBatchHasNoLaunchAndManifestIsProviderOwned(t *testing.T) {
	p, task := openingFixture()
	providers := []integration.ResourceOpenProvider{p}
	for _, ids := range [][]string{nil, {}, {"ref1:changes", "unknown"}, {"https://example.test/arbitrary"}, make([]string, MaxOpenResources+1)} {
		if _, err := PreviewResourceOpen(context.Background(), providers, "one", task, integration.ResourceOpenRequest{ResourceIDs: ids}); err == nil {
			t.Fatalf("accepted %#v", ids)
		}
	}
	for _, change := range []func(*integration.OpenResource){
		func(r *integration.OpenResource) { r.URL = "file:///tmp/page.html" },
		func(r *integration.OpenResource) { r.SourceRefID = "other" },
		func(r *integration.OpenResource) { r.Source = "other" },
		func(r *integration.OpenResource) { r.ID = "" },
	} {
		p, task := openingFixture()
		change(&p.targets["ref1"][0])
		if _, err := OpenableResources(context.Background(), []integration.ResourceOpenProvider{p}, task); err == nil {
			t.Fatal("accepted bad descriptor")
		}
	}
	p, task = openingFixture()
	p.targets["ref2"][1].URL = "https://example.test/conflict"
	if _, err := OpenableResources(context.Background(), []integration.ResourceOpenProvider{p}, task); err == nil {
		t.Fatal("accepted ambiguous target")
	}
	p.err = fmt.Errorf("malformed identity")
	if _, err := OpenableResources(context.Background(), []integration.ResourceOpenProvider{p}, task); err == nil {
		t.Fatal("ignored provider failure")
	}
}

func TestOpeningPartialFailureAndCancellationNeverRetry(t *testing.T) {
	p, task := openingFixture()
	plan, err := PreviewResourceOpen(context.Background(), []integration.ResourceOpenProvider{p}, "one", task, integration.ResourceOpenRequest{ResourceIDs: []string{"ref1:changes", "ref2:changes"}})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	result, err := ApplyResourceOpen(context.Background(), plan, plan.PlanID, func(context.Context, string) error {
		attempts++
		if attempts == 2 {
			return fmt.Errorf("launcher failed with secret diagnostics")
		}
		return nil
	})
	if err != nil || result.OK || attempts != 2 || result.Results[0].Status != "launch_requested" || result.Results[1].Status != "failed" {
		t.Fatalf("%#v %d %v", result, attempts, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = ApplyResourceOpen(ctx, plan, plan.PlanID, func(context.Context, string) error { t.Fatal("launch after cancellation"); return nil })
	if err != nil || result.OK || result.Results[0].Status != "cancelled" {
		t.Fatalf("%#v %v", result, err)
	}
}
