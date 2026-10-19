package datadog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

// Read-only qualification with a real CLI-owned OAuth session. These explicit
// selectors keep normal test runs offline and never read Pup credential files.
func TestLivePupOAuthCollection(t *testing.T) {
	site := os.Getenv("RADAR_TEST_DATADOG_SITE")
	if site == "" {
		t.Skip("set RADAR_TEST_DATADOG_SITE, RADAR_TEST_DATADOG_ORG and RADAR_TEST_DATADOG_QUERY for approved live reads")
	}
	org, query := os.Getenv("RADAR_TEST_DATADOG_ORG"), os.Getenv("RADAR_TEST_DATADOG_QUERY")
	if org == "" || query == "" {
		t.Fatal("live reads require an explicit named OAuth session and monitor query")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeConfig := func(session string) {
		data, err := json.Marshal(map[string]any{
			"linking_mark_prefixes": []string{"ABC"},
			"datadog":               map[string]any{"enabled": true, "site": site, "org": session, "monitor_query": query},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "radar", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(org)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	source := NewSource()
	if status := source.Status(ctx, testLogger()); !status.CanRun {
		t.Fatalf("source unavailable: %+v", status.Status)
	}
	request := integration.CollectRequest{Logger: testLogger()}
	first := source.Collect(ctx, request)
	if !first.Complete || first.SourceStatus == nil || first.SourceStatus.Status != "ok" {
		t.Fatalf("live OAuth collection failed: %+v", first.SourceStatus)
	}
	var previous []protocol.Task
	var identities []string
	for _, observation := range first.Observations {
		contracttest.AssertValidSourceRefs(t, "datadog", []protocol.SourceRef{observation.Ref})
		identities = append(identities, observation.Ref.ID)
		previous = append(previous, protocol.Task{Attention: string(observation.Signal), SourceRefs: []protocol.SourceRef{observation.Ref}})
	}
	t.Logf("verified %s named OAuth collection: %d unhealthy monitor(s)", site, len(identities))

	// Every collection spawns new headless CLI processes. Static fixture
	// overrides must not displace the saved named OAuth session.
	t.Setenv("DD_ACCESS_TOKEN", "static-e2e-fixture")
	t.Setenv("DD_API_KEY", "key-e2e-fixture")
	t.Setenv("DD_APP_KEY", "app-e2e-fixture")
	request.Previous = previous
	second := source.Collect(ctx, request)
	if !second.Complete {
		t.Fatalf("inherited static credentials defeated OAuth: %+v", second.SourceStatus)
	}
	var secondIdentities []string
	for _, observation := range second.Observations {
		secondIdentities = append(secondIdentities, observation.Ref.ID)
	}
	if !reflect.DeepEqual(identities, secondIdentities) {
		t.Fatalf("live monitor states changed between reads; repeat against stable fixtures: %v -> %v", identities, secondIdentities)
	}

	// A missing explicit session must fail rather than selecting another saved
	// session or the injected static fixtures. Existing live facts must survive.
	writeConfig(org + "-missing-e2e-session")
	failed := source.Collect(ctx, request)
	if failed.Complete || failed.SourceStatus == nil || failed.SourceStatus.Status != "error" || !reflect.DeepEqual(failed.Observations, first.Observations) {
		t.Fatalf("missing session did not fail safely: %+v", failed.SourceStatus)
	}
	if done := source.Reconcile(ctx, integration.ReconcileRequest{Previous: previous, Result: failed}); len(done) != 0 {
		t.Fatal("failed collection falsely recovered a live monitor")
	}
	t.Log("fresh-process reads, static-override rejection and missing-session failure passed; state transitions require separate live daemon checks")
}
