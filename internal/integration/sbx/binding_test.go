package sbx

import (
	"reflect"
	"strings"
	"testing"

	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestSandboxBindingTracksRuntimeLifetimeNotNameOrMount(t *testing.T) {
	for _, mounts := range [][]string{nil, {"/work/feature"}} {
		name := "unmounted"
		if len(mounts) > 0 {
			name = "mounted"
		}
		t.Run(name, func(t *testing.T) {
			original := sandbox{Name: "feature-sandbox", ID: "container-one", Status: "running", Workspaces: mounts}
			first := original.SourceRef(linking.MarkMatcher{}, "")
			sameRuntime := original
			sameRuntime.Status = "stopped"
			same := sameRuntime.SourceRef(linking.MarkMatcher{}, "")
			if first.Binding() != same.Binding() || first.Binding().LinkingKey() != same.Binding().LinkingKey() {
				t.Fatalf("runtime status changed binding: first=%+v same=%+v", first.Binding(), same.Binding())
			}
			reusedName := original
			reusedName.ID = "container-two"
			replacement := reusedName.SourceRef(linking.MarkMatcher{}, "")
			if first.BindingKey == "" || replacement.BindingKey == "" || first.Binding().LinkingKey() == replacement.Binding().LinkingKey() {
				t.Fatalf("reused sandbox name/mount inherited old binding: first=%+v replacement=%+v", first.Binding(), replacement.Binding())
			}
			assertSandboxPublicIdentityUnchanged(t, first, replacement)
			for _, ref := range []protocol.SourceRef{first, replacement} {
				binding := ref.Binding()
				if binding.Source != "sbx" || binding.Kind != "sandbox" || binding.ID != ref.ID || binding.Key != "sbx:sandbox:"+ref.Metadata["id"] || binding.WorkItem || ref.BindingError != "" {
					t.Fatalf("invalid runtime binding: %+v", binding)
				}
				if ref.Lifecycle != protocol.SourceRefLifecycleResource || ref.Authority != protocol.SourceRefAuthorityNone || ref.ProvidesWorkspace {
					t.Fatalf("lifetime key changed runtime authority/capabilities: %+v", ref)
				}
			}
		})
	}
}

func TestSandboxBindingSurvivesRenameOfSameRuntime(t *testing.T) {
	first := (sandbox{Name: "one", ID: "container-one"}).SourceRef(linking.MarkMatcher{}, "")
	renamed := (sandbox{Name: "two", ID: "container-one", Workspaces: []string{"/work/other"}}).SourceRef(linking.MarkMatcher{}, "")
	if first.ID == renamed.ID {
		t.Fatal("public IDs no longer follow existing name-based identity")
	}
	if first.BindingKey == "" || first.BindingKey != renamed.BindingKey || first.Binding().LinkingKey() != renamed.Binding().LinkingKey() {
		t.Fatalf("runtime rename/mount change broke binding: first=%+v renamed=%+v", first.Binding(), renamed.Binding())
	}
}

func TestRegisteredSandboxRecreationUsesCurrentWorkspaceAssociation(t *testing.T) {
	first := (sandbox{Name: "feature-sandbox", ID: "container-one", Workspaces: []string{"/repo/.git"}}).SourceRef(linking.MarkMatcher{}, "/work/feature")
	recreated := (sandbox{Name: "feature-sandbox", ID: "container-two", Workspaces: []string{"/repo/.git"}}).SourceRef(linking.MarkMatcher{}, "/work/feature")
	if first.BindingKey == "" || first.Binding().LinkingKey() == recreated.Binding().LinkingKey() {
		t.Fatalf("recreated registered runtime inherited old lifetime binding: first=%+v recreated=%+v", first.Binding(), recreated.Binding())
	}
	assertSandboxPublicIdentityUnchanged(t, first, recreated)
	if recreated.Path != "/work/feature" || !reflect.DeepEqual(recreated.LinkingKeys, []string{"workspace:/work/feature"}) {
		t.Fatalf("registered recreation lost current workspace/note association: %+v", recreated)
	}

	// After workspace cleanup, the old exact binding must not match a new
	// standalone sandbox merely because the old registered name was reused.
	standalone := (sandbox{Name: "feature-sandbox", ID: "container-three"}).SourceRef(linking.MarkMatcher{}, "")
	if first.Binding().LinkingKey() == standalone.Binding().LinkingKey() || len(standalone.LinkingKeys) != 0 {
		t.Fatalf("standalone sandbox inherited removed workspace binding: %+v", standalone)
	}
}

func TestSandboxBindingUsesIDWhenNameIsMissing(t *testing.T) {
	ref := (sandbox{ID: " container-one "}).SourceRef(linking.MarkMatcher{}, "")
	if ref.ID != "sbx:sandbox:container-one" || ref.BindingKey != "sbx:sandbox:container-one" {
		t.Fatalf("unnamed sandbox identity = %+v", ref)
	}
}

func TestSandboxBindingDoesNotFallbackToReusableName(t *testing.T) {
	ref := (sandbox{Name: "feature-sandbox", Workspaces: []string{"/work/feature"}}).SourceRef(linking.MarkMatcher{}, "")
	if ref.BindingKey != "" || !strings.Contains(ref.BindingError, "does not include a container ID") || ref.ID != "sbx:sandbox:feature-sandbox" || ref.Path != "/work/feature" || ref.Authority != protocol.SourceRefAuthorityNone || ref.ProvidesWorkspace {
		t.Fatalf("missing concrete identity changed existing collection or used reusable name: %+v", ref)
	}
}

func assertSandboxPublicIdentityUnchanged(t *testing.T, first, replacement protocol.SourceRef) {
	t.Helper()
	if first.ID != replacement.ID || first.EntityID != replacement.EntityID || first.CanonicalKey != replacement.CanonicalKey || !reflect.DeepEqual(first.LinkingKeys, replacement.LinkingKeys) || first.Path != replacement.Path {
		t.Fatalf("lifetime key changed public identity or workspace linking: first=%+v replacement=%+v", first, replacement)
	}
}
