package contracttest

import (
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestWorkspaceProvidingSourceRefContract(t *testing.T) {
	valid := protocol.SourceRef{
		ID: "tasks:item:1", EntityID: "tasks:item:1", Source: "tasks", Kind: "task",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
		Authority: protocol.SourceRefAuthorityPrimary, CanonicalKey: "tasks:item:1",
		Path: "/work/tasks/one", ProvidesWorkspace: true,
		LinkingKeys: []string{"tasks:item:1", "workspace:/work/tasks/one"},
	}
	if err := validateSourceRefs("tasks", []protocol.SourceRef{valid}); err != nil {
		t.Fatalf("valid workspace ref rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*protocol.SourceRef)
		want string
	}{
		{name: "empty path", edit: func(ref *protocol.SourceRef) { ref.Path = "" }, want: "non-absolute path"},
		{name: "relative path", edit: func(ref *protocol.SourceRef) { ref.Path = "work/tasks/one" }, want: "non-absolute path"},
		{name: "missing workspace key", edit: func(ref *protocol.SourceRef) { ref.LinkingKeys = []string{"tasks:item:1"} }, want: "missing linking key"},
		{name: "unclean workspace key", edit: func(ref *protocol.SourceRef) { ref.Path = "/work/tasks/../one" }, want: "workspace:/work/one"},
		{name: "informational ref", edit: func(ref *protocol.SourceRef) { ref.Role = protocol.SourceRefRoleInformational }, want: "exposes authority"},
		{name: "resource ref", edit: func(ref *protocol.SourceRef) { ref.Lifecycle = protocol.SourceRefLifecycleResource }, want: "resource source ref provides"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := valid
			tt.edit(&ref)
			err := validateSourceRefs("tasks", []protocol.SourceRef{ref})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateSourceRefs() error = %v, want %q", err, tt.want)
			}
		})
	}
}
