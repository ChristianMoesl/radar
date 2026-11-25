package cleanup

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type borrowedPreviewProvider struct {
	name    string
	targets []protocol.CleanupTarget
	preview func()
}

func (p borrowedPreviewProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name}
}
func (p borrowedPreviewProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if p.preview != nil {
		p.preview()
	}
	return p.targets, nil
}
func (borrowedPreviewProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, nil
}

func TestPreviewOwnsEachProviderResultBeforeNextCallback(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupRetire, integration.CleanupExpired} {
		t.Run(string(mode), func(t *testing.T) {
			original := protocol.CleanupTarget{Source: "first", Kind: "runtime", ResourceID: "original", Operation: map[string]string{"lifetime": "original", "empty": ""}, Safety: []protocol.CleanupSafety{{Kind: "structural", Summary: "keep", Message: "original warning", BlocksAutomatic: true}}}
			borrowed := []protocol.CleanupTarget{original, {Source: "first", Kind: "workspace", ResourceID: "second", Operation: map[string]string{}, Safety: []protocol.CleanupSafety{}}}
			want := protocol.CleanupTarget{Source: "first", Kind: "runtime", ResourceID: "original", Operation: map[string]string{"lifetime": "original", "empty": ""}, Safety: []protocol.CleanupSafety{{Kind: "structural", Summary: "keep", Message: "original warning", BlocksAutomatic: true}}}
			later := borrowedPreviewProvider{name: "later", preview: func() {
				borrowed[0].ResourceID = "replacement"
				borrowed[0].Operation["lifetime"] = "replacement"
				delete(borrowed[0].Operation, "empty")
				borrowed[0].Safety[0] = protocol.CleanupSafety{Kind: "local_changes", Message: "rewritten warning", Expires: true}
				borrowed[1].Operation["added"] = "later"
			}, targets: []protocol.CleanupTarget{{Source: "later", Kind: "last"}}}
			got, err := New([]integration.CleanupProvider{borrowedPreviewProvider{name: "first", targets: borrowed}, later}).Preview(context.Background(), protocol.Task{ID: 7, Title: "ABC-123"}, mode)
			if err != nil || got.TaskID != 7 || got.TaskTitle != "ABC-123" || len(got.Targets) != 3 {
				t.Fatalf("preview: %+v %v", got, err)
			}
			if !reflect.DeepEqual(got.Targets[0], want) || got.Targets[1].Operation == nil || len(got.Targets[1].Operation) != 0 || got.Targets[1].Safety == nil || got.Targets[2].Source != "later" {
				t.Fatalf("later provider rewrote an earlier native preview: %+v", got)
			}
		})
	}
}

func TestPreviewOwnsNestedValuesInBothDirections(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupRetire, integration.CleanupExpired} {
		for _, shape := range []string{"nil", "empty", "populated"} {
			t.Run(string(mode)+"/"+shape, func(t *testing.T) {
				target := protocol.CleanupTarget{Source: "provider", Kind: "runtime"}
				if shape != "nil" {
					target.Operation = map[string]string{}
					target.Safety = []protocol.CleanupSafety{}
				}
				if shape == "populated" {
					target.Operation["lifetime"] = "original"
					target.Safety = append(target.Safety, protocol.CleanupSafety{Kind: "notice", Message: "original"})
				}
				p := borrowedPreviewProvider{name: "provider", targets: []protocol.CleanupTarget{target}}
				service := New([]integration.CleanupProvider{p})
				first, err := service.Preview(context.Background(), protocol.Task{ID: 7}, mode)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(first.Targets[0], target) {
					t.Fatal("copy changed nil/empty or native values")
				}
				if shape == "nil" {
					return
				}
				first.Targets[0].Operation["consumer"] = "changed"
				if shape == "populated" {
					first.Targets[0].Safety[0].Message = "consumer"
				}
				if target.Operation["consumer"] != "" || shape == "populated" && target.Safety[0].Message != "original" {
					t.Fatal("consumer rewrote provider-owned native values")
				}
				second, err := service.Preview(context.Background(), protocol.Task{ID: 7}, mode)
				if err != nil {
					t.Fatal(err)
				}
				target.Operation["provider"] = "changed"
				if shape == "populated" {
					target.Safety[0].Message = "provider"
				}
				if second.Targets[0].Operation["provider"] != "" || shape == "populated" && second.Targets[0].Safety[0].Message != "original" {
					t.Fatal("provider rewrote returned native values")
				}
				if first.Targets[0].Operation["provider"] != "" || second.Targets[0].Operation["consumer"] != "" {
					t.Fatal("separate previews share nested values")
				}
			})
		}
	}
}

func TestPreviewOwnershipDoesNotChangeErrorsOrEmptyTargets(t *testing.T) {
	sentinel := errors.New("provider unavailable")
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-resources", true: "later-error"}[failure], func(t *testing.T) {
			providers := []integration.CleanupProvider{borrowedPreviewProvider{name: "empty"}}
			if failure {
				providers = append(providers, fakeProvider{name: "failed", previewErr: sentinel})
			}
			got, err := New(providers).Preview(context.Background(), protocol.Task{}, integration.CleanupRetire)
			want := ErrNoResources
			if failure {
				want = sentinel
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(got, protocol.CleanupPreview{}) {
				t.Fatalf("error contract changed: %+v %v", got, err)
			}
		})
	}
}
