package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"radar/internal/protocol"
)

type stopProviderFixture struct {
	name     string
	prepared int
}

func (f *stopProviderFixture) Descriptor() Descriptor { return Descriptor{Name: f.name} }
func (f *stopProviderFixture) PrepareDiagnosticAgentStop(context.Context, string, protocol.AgentWorkspaceBinding, AgentStopGuard) (AgentStopPlan, error) {
	f.prepared++
	return nil, errors.New("fixture must not prepare during selection")
}

type genericAgentFixture struct{ calls int }

func (*genericAgentFixture) Descriptor() Descriptor { return Descriptor{Name: "agent"} }
func (f *genericAgentFixture) ManageAgent(context.Context, protocol.AgentRequest) (json.RawMessage, error) {
	f.calls++
	return nil, errors.New("generic control must not be a diagnostic fallback")
}

func TestAgentStopDiagnosticsRequiresOneExplicitProvider(t *testing.T) {
	for _, kind := range []string{"none", "generic_only", "unique", "unique_with_generic", "two_names", "same_name", "duplicate_instance"} {
		t.Run(kind, func(t *testing.T) {
			first := &stopProviderFixture{name: "agent"}
			second := &stopProviderFixture{name: "other"}
			generic := &genericAgentFixture{}
			var candidates []Integration
			switch kind {
			case "none":
				candidates = []Integration{nil}
			case "generic_only":
				candidates = []Integration{generic}
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
			provider, err := NewRegistry(candidates...).AgentStopDiagnostics()
			if kind == "unique" || kind == "unique_with_generic" {
				if err != nil || provider != first {
					t.Fatal("unique provider lost", err)
				}
			} else if err == nil || provider != nil {
				t.Fatal("ambiguous/unsupported selection admitted")
			}
			if first.prepared != 0 || second.prepared != 0 || generic.calls != 0 {
				t.Fatal("selection performed control/capture")
			}
		})
	}
}
