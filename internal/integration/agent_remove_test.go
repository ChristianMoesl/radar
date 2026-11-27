package integration

import (
	"context"
	"errors"
	"testing"

	"radar/internal/protocol"
)

type removeProviderFixture struct {
	name     string
	prepared int
}

func (f *removeProviderFixture) Descriptor() Descriptor { return Descriptor{Name: f.name} }
func (f *removeProviderFixture) PrepareDiagnosticAgentRemove(context.Context, string, protocol.AgentWorkspaceBinding, AgentRemoveGuard) (AgentRemovePlan, error) {
	f.prepared++
	return nil, errors.New("fixture must not prepare during selection")
}

func TestAgentRemoveDiagnosticsRequiresOneExplicitProvider(t *testing.T) {
	for _, kind := range []string{"none", "generic_only", "stop_only", "stop_with_remove", "unique", "unique_with_generic", "two_names", "same_name", "duplicate_instance"} {
		t.Run(kind, func(t *testing.T) {
			first := &removeProviderFixture{name: "agent"}
			second := &removeProviderFixture{name: "other"}
			generic := &genericAgentFixture{}
			stop := &stopProviderFixture{name: "agent"}
			var candidates []Integration
			switch kind {
			case "none":
				candidates = []Integration{nil}
			case "generic_only":
				candidates = []Integration{generic}
			case "stop_only":
				candidates = []Integration{stop}
			case "stop_with_remove":
				candidates = []Integration{stop, first}
			case "unique":
				candidates = []Integration{nil, first}
			case "unique_with_generic":
				candidates = []Integration{generic, first}
			case "two_names":
				candidates = []Integration{first, second}
			case "same_name":
				second.name = first.name
				candidates = []Integration{first, second}
			case "duplicate_instance":
				candidates = []Integration{first, first}
			}
			provider, err := NewRegistry(candidates...).AgentRemoveDiagnostics()
			if kind == "unique" || kind == "unique_with_generic" || kind == "stop_with_remove" {
				if err != nil || provider != first {
					t.Fatal("unique provider lost", err)
				}
			} else if err == nil || provider != nil {
				t.Fatal("ambiguous/unsupported selection admitted")
			}
			if first.prepared != 0 || second.prepared != 0 || generic.calls != 0 || stop.prepared != 0 {
				t.Fatal("selection performed control/capture")
			}
		})
	}
}
