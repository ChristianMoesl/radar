package state

import (
	"reflect"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestCanonicalOnlyRefLinkKeysPreserveExplicitAndLifetimeOwnership(t *testing.T) {
	for _, tt := range []struct {
		name     string
		role     protocol.SourceRefRole
		links    []string
		lifetime string
		want     []string
	}{
		{"canonical only", protocol.SourceRefRoleAuthoritative, nil, "", []string{"source-ref:alerts:monitor:alerts:ref:one", "alerts:monitor:one"}},
		{"blank explicit keys", protocol.SourceRefRoleAuthoritative, []string{"", " \t"}, "", []string{"source-ref:alerts:monitor:alerts:ref:one", "alerts:monitor:one"}},
		{"explicit keys unchanged", protocol.SourceRefRoleAuthoritative, []string{" mark:ABC-1 ", "mark:ABC-1"}, "", []string{"source-ref:alerts:monitor:alerts:ref:one", "mark:ABC-1"}},
		{"concrete lifetime unchanged", protocol.SourceRefRoleAuthoritative, nil, "instance-one", []string{"source-ref:alerts:monitor:instance-one"}},
		{"informational cannot own", protocol.SourceRefRoleInformational, nil, "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref := protocol.SourceRef{ID: "alerts:ref:one", Source: "alerts", Kind: "monitor", Role: tt.role,
				CanonicalKey: " alerts:monitor:one ", LinkingKeys: tt.links, BindingKey: tt.lifetime}
			if got := linkKeysForSourceRef(ref); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("link keys = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanonicalOnlyAssociationCannotBridgeDistinctAuthoredTasks(t *testing.T) {
	const canonical = "alerts:monitor:one"
	a, b := authoredLinkRef("one", canonical), authoredLinkRef("two", canonical)
	remote := protocol.SourceRef{ID: "alerts:ref:one", Source: "alerts", Kind: "monitor", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: canonical, Signal: "immediate"}
	eachTaskOrder([]protocol.Task{makeTask("done", "One", a), makeTask("done", "Two", b), makeTask("immediate", "Alert", remote)}, func(tasks []protocol.Task) {
		got := reconcileState(persistedState{}, tasks, time.Now().UTC())
		if err := validateAuthoredLinks(got); err != nil || len(got.Records) != 3 {
			t.Fatalf("canonical-only bridge crossed authored identity: %+v (%v)", linkPartitions(got), err)
		}
	})
}

func TestCanonicalFallbackDoesNotAdoptExistingUnassociatedWorkspace(t *testing.T) {
	note := authoredLinkRef("existing", "workspace:/work/existing")
	note.Signal = "low_priority"
	workspace := protocol.SourceRef{ID: "workspace:existing", Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkspace, CanonicalKey: "workspace:/work/existing", LinkingKeys: []string{"workspace:/work/existing", note.ID}, Signal: "in_progress"}
	remote := protocol.SourceRef{ID: "alerts:ref:one", Source: "alerts", Kind: "monitor", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: "alerts:monitor:one", Signal: "immediate"}
	tasks := []protocol.Task{makeTask("low_priority", "Same title", note), makeTask("in_progress", "Same title", workspace), makeTask("immediate", "Same title", remote)}
	prior := reconcileState(persistedState{}, tasks, time.Now().UTC())
	fresh := reconcileState(prior, tasks, time.Now().UTC())
	if len(prior.Records) != 2 || len(fresh.Records) != 2 || !reflect.DeepEqual(linkPartitions(prior), linkPartitions(fresh)) {
		t.Fatalf("existing unassociated work was adopted: before=%v after=%v", linkPartitions(prior), linkPartitions(fresh))
	}
}
