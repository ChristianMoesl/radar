package github

import (
	"context"
	"radar/internal/protocol"
	"testing"
)

func TestOpenResourcesCanonicalURLs(t *testing.T) {
	ref := protocol.SourceRef{ID: "github:pr:acme/app:123", Source: "github", Kind: "pull_request", Repo: "acme/app", CanonicalKey: "github:pr:acme/app:123", URL: "javascript:untrusted", Status: "merged PR"}
	targets, err := (Source{}).OpenResources(context.Background(), ref)
	if err != nil || len(targets) != 3 {
		t.Fatalf("%#v %v", targets, err)
	}
	expected := []string{"https://github.com/acme/app/pull/123", "https://github.com/acme/app/pull/123/files", "https://github.com/acme/app"}
	for i, target := range targets {
		if target.URL != expected[i] || target.SourceRefID != ref.ID || target.Source != "github" {
			t.Fatalf("%#v", target)
		}
	}
}

func TestOpenResourcesRejectMalformedIdentity(t *testing.T) {
	for _, change := range []func(*protocol.SourceRef){
		func(r *protocol.SourceRef) { r.ID = "github:pr:acme/app:0" },
		func(r *protocol.SourceRef) { r.Repo = "acme/other" },
		func(r *protocol.SourceRef) { r.Repo = "acme/app?payload=x" },
		func(r *protocol.SourceRef) { r.Repo = "https://github.com/acme/app" },
		func(r *protocol.SourceRef) { r.CanonicalKey = "github:pr:acme/app:124" },
		func(r *protocol.SourceRef) { r.ID = "github:pr:acme/app%2Fother:123" },
	} {
		ref := protocol.SourceRef{ID: "github:pr:acme/app:123", Source: "github", Kind: "pull_request"}
		change(&ref)
		if _, err := (Source{}).OpenResources(context.Background(), ref); err == nil {
			t.Fatalf("accepted %#v", ref)
		}
	}
	for _, ref := range []protocol.SourceRef{{Source: "other", Kind: "pull_request"}, {Source: "github", Kind: "issue"}} {
		targets, err := (Source{}).OpenResources(context.Background(), ref)
		if err != nil || len(targets) != 0 {
			t.Fatalf("%#v %v", targets, err)
		}
	}
}
