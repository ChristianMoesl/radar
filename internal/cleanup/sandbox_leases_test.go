package cleanup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type sandboxLeaseProvider struct {
	fakeProvider
	cleanup func(context.Context, protocol.CleanupTarget) error
}

func (p sandboxLeaseProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, p.cleanup(ctx, req.Target)
}

func sandboxLeaseService(t *testing.T, cleanup func(context.Context, protocol.CleanupTarget) error) Service {
	t.Helper()
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	return New([]integration.CleanupProvider{sandboxLeaseProvider{fakeProvider: fakeProvider{name: "sbx"}, cleanup: cleanup}})
}
func sandboxLeaseTarget(name, path string) protocol.CleanupTarget {
	return protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: name, Path: path}
}
func assertSandboxNameReleased(t *testing.T, name string) {
	t.Helper()
	release, err := workspacelease.AcquireSandbox(name, true)
	if err != nil {
		t.Fatal("sandbox name lease leaked", err)
	}
	release()
}

func TestCleanupSandboxNameRejectsActiveUsersBeforeAnyProvider(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		for _, withPath := range []bool{false, true} {
			t.Run(map[bool]string{false: "shared", true: "exclusive"}[exclusive]+"/path="+map[bool]string{false: "absent", true: "different"}[withPath], func(t *testing.T) {
				calls := 0
				service := sandboxLeaseService(t, func(context.Context, protocol.CleanupTarget) error { calls++; return nil })
				release, err := workspacelease.AcquireSandbox("shared-runtime", exclusive)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				path := ""
				if withPath {
					path = t.TempDir()
				}
				result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{sandboxLeaseTarget("shared-runtime", path)}}, ExecuteOptions{Mode: integration.CleanupConfirmed})
				if err == nil || !strings.Contains(err.Error(), "sandbox is in use by another operation") || calls != 0 || len(result.Targets) != 0 {
					t.Fatalf("cleanup bypassed runtime name: %v calls=%d result=%+v", err, calls, result)
				}
			})
		}
	}
}

func TestCleanupSandboxNameHeldThroughProviderAndReleased(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := sandboxLeaseService(t, func(context.Context, protocol.CleanupTarget) error {
				if release, err := workspacelease.AcquireSandbox("shared-runtime", false); err == nil {
					release()
					t.Fatal("activation entered cleanup provider")
				}
				release, err := workspacelease.AcquireSandbox("unrelated-runtime", true)
				if err != nil {
					t.Fatal(err)
				}
				release()
				if outcome == "failure" {
					return errors.New("fixture provider failed")
				}
				if outcome == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			})
			_, err := service.Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{sandboxLeaseTarget("shared-runtime", t.TempDir())}}, ExecuteOptions{Mode: integration.CleanupConfirmed})
			if (err != nil) != (outcome != "success") {
				t.Fatal(err)
			}
			if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			assertSandboxNameReleased(t, "shared-runtime")
		})
	}
}

func TestCleanupSandboxNamePartialAdmissionRollsBackAllLeases(t *testing.T) {
	calls := 0
	service := sandboxLeaseService(t, func(context.Context, protocol.CleanupTarget) error { calls++; return nil })
	release, err := workspacelease.AcquireSandbox("blocked-runtime", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := t.TempDir()
	preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{sandboxLeaseTarget("first-runtime", path), sandboxLeaseTarget("blocked-runtime", path)}}
	if _, err := service.Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupConfirmed}); err == nil || calls != 0 {
		t.Fatal("partial admission dispatched providers", err, calls)
	}
	assertSandboxNameReleased(t, "first-runtime")
	pathRelease, err := workspacelease.Acquire(path, true)
	if err != nil {
		t.Fatal("path lease leaked", err)
	}
	pathRelease()
}

func TestCleanupSandboxNameInvalidTargetRejectsBundleBeforeProviders(t *testing.T) {
	for _, name := range []string{"", "invalid\nname"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			service := sandboxLeaseService(t, func(context.Context, protocol.CleanupTarget) error { calls++; return nil })
			path := t.TempDir()
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
				sandboxLeaseTarget("valid-runtime", path),
				sandboxLeaseTarget(name, path),
			}}
			result, err := service.Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupConfirmed})
			if err == nil || !strings.Contains(err.Error(), "sandbox name is required") || calls != 0 || len(result.Targets) != 0 {
				t.Fatalf("invalid identity reached provider: %v calls=%d result=%+v", err, calls, result)
			}
			assertSandboxNameReleased(t, "valid-runtime")
			release, err := workspacelease.Acquire(path, true)
			if err != nil {
				t.Fatal("path lease leaked after invalid sandbox", err)
			}
			release()
		})
	}
}

func TestCleanupSandboxNameDeduplicatesAdmissionNotProviderTargets(t *testing.T) {
	var paths []string
	service := sandboxLeaseService(t, func(_ context.Context, target protocol.CleanupTarget) error {
		paths = append(paths, target.Path)
		return nil
	})
	first, second := t.TempDir(), t.TempDir()
	preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{sandboxLeaseTarget("shared-runtime", first), sandboxLeaseTarget("shared-runtime", second)}}
	result, err := service.Execute(context.Background(), preview, ExecuteOptions{Mode: integration.CleanupConfirmed})
	if err != nil || len(result.Targets) != 2 || len(paths) != 2 || paths[0] != first || paths[1] != second {
		t.Fatalf("runtime admission lost original target order: %v %+v %v", err, result, paths)
	}
	assertSandboxNameReleased(t, "shared-runtime")
}
