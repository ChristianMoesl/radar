package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func pathAdmissionFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	physical, alias, extra := filepath.Join(root, "physical"), filepath.Join(root, "alias"), filepath.Join(root, "extra")
	for _, path := range []string{physical, extra} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	return physical, alias, extra
}

func assertPathHeld(t *testing.T, path string) {
	t.Helper()
	if release, err := workspacelease.Acquire(path, true); err == nil {
		release()
		t.Fatal("required deletion admission not retained", path)
	}
}
func assertPathReleased(t *testing.T, path string) {
	t.Helper()
	release, err := workspacelease.Acquire(path, true)
	if err != nil {
		t.Fatal("deletion admission leaked", path, err)
	}
	release()
}

func TestAcquirePathsDirectAliasesDeduplicateAndRelease(t *testing.T) {
	physical, alias, extra := pathAdmissionFixture(t)
	missing := filepath.Join(extra, "not-created")
	release, err := AcquirePaths(context.Background(), physical, alias, physical, missing)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{physical, alias, missing} {
		assertPathHeld(t, path)
	}
	release()
	for _, path := range []string{physical, alias, missing} {
		assertPathReleased(t, path)
	}
}

func TestAcquirePathsFailuresUnwindAndDoNotBorrowForeignLocks(t *testing.T) {
	for _, kind := range []string{"busy-later", "invalid-later", "cancelled", "foreign-context"} {
		t.Run(kind, func(t *testing.T) {
			physical, _, extra := pathAdmissionFixture(t)
			// Canonical sorting puts extra before physical, so a busy physical
			// path exercises partial acquisition, not just an initial refusal.
			ctx := context.Background()
			paths := []string{extra, physical}
			var unblock func()
			switch kind {
			case "busy-later", "foreign-context":
				var err error
				unblock, err = workspacelease.Acquire(physical, false)
				if err != nil {
					t.Fatal(err)
				}
				defer unblock()
				if kind == "foreign-context" {
					ctx = context.WithValue(ctx, "cleanup paths", map[string]bool{physical: true})
				}
			case "invalid-later":
				bad := filepath.Join(filepath.Dir(extra), "dangling")
				if err := os.Symlink(filepath.Join(extra, "missing-target"), bad); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, bad)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if release, err := AcquirePaths(ctx, paths...); err == nil {
				release()
				t.Fatal("unsafe path set admitted")
			}
			assertPathReleased(t, extra)
			if unblock != nil {
				assertPathHeld(t, physical)
			} else {
				assertPathReleased(t, physical)
			}
		})
	}
}

type pathAdmissionProvider struct {
	fakeProvider
	visit func(context.Context, string) error
}

func (p pathAdmissionProvider) AdmitCleanup(ctx context.Context, _ []protocol.CleanupTarget, _ integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	if err := p.visit(ctx, "admission"); err != nil {
		return nil, nil, err
	}
	return p, func() {}, nil
}
func (p pathAdmissionProvider) PreflightCleanup(ctx context.Context, _ []protocol.CleanupTarget, _ integration.CleanupMode) error {
	return p.visit(ctx, "preflight")
}
func (p pathAdmissionProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, p.visit(ctx, "cleanup")
}

func TestServiceOwnsBorrowedAndDiscoveredPathsThroughEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "admission-error", "preflight-error", "cleanup-error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			physical, alias, extra := pathAdmissionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var retained context.Context
			failure := errors.New("fixture provider failure")
			calls := 0
			provider := pathAdmissionProvider{fakeProvider: fakeProvider{name: "fixture"}, visit: func(ctx context.Context, phase string) error {
				retained = context.WithoutCancel(ctx)
				calls++
				release, err := AcquirePaths(ctx, alias, physical, extra, extra)
				if err != nil {
					t.Fatal("real Service bundle reacquired or alias not deduplicated", err)
				}
				for _, path := range []string{physical, extra} {
					assertPathHeld(t, path)
				}
				release() // Cannot drop borrowed or execution-owned discovered paths.
				for _, path := range []string{physical, extra} {
					assertPathHeld(t, path)
				}
				if outcome == phase+"-error" {
					return failure
				}
				if outcome == "cancelled" && phase == "cleanup" {
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			_, err := New([]integration.CleanupProvider{provider}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Path: physical}}}, ExecuteOptions{})
			if (err != nil) != (outcome != "success") || calls == 0 {
				t.Fatal("unexpected execution outcome", calls, err)
			}
			if strings.HasSuffix(outcome, "-error") && !errors.Is(err, failure) {
				t.Fatal("provider cause lost", err)
			}
			if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			for _, path := range []string{physical, extra} {
				assertPathReleased(t, path)
				hold, err := workspacelease.Acquire(path, true)
				if err != nil {
					t.Fatal(err)
				}
				if release, err := AcquirePaths(retained, path); err == nil {
					release()
					hold()
					t.Fatal("expired Service context bypassed a real foreign owner")
				}
				hold()
				release, err := AcquirePaths(retained, path)
				if err != nil {
					t.Fatal("expired context cannot acquire fresh admission", err)
				}
				release()
			}
		})
	}
}

func TestServicePathExtensionDeduplicatesConcurrentRequests(t *testing.T) {
	physical, alias, extra := pathAdmissionFixture(t)
	provider := pathAdmissionProvider{fakeProvider: fakeProvider{name: "fixture"}, visit: func(ctx context.Context, phase string) error {
		if phase != "cleanup" {
			return nil
		}
		done := make(chan error, 8)
		for i := 0; i < cap(done); i++ {
			go func() {
				release, err := AcquirePaths(ctx, physical, alias, extra)
				if err == nil {
					release()
				}
				done <- err
			}()
		}
		for i := 0; i < cap(done); i++ {
			if err := <-done; err != nil {
				t.Error("concurrent real-owner admission was not deduplicated", err)
			}
		}
		assertPathHeld(t, physical)
		assertPathHeld(t, extra)
		return nil
	}}
	if _, err := New([]integration.CleanupProvider{provider}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Path: physical}}}, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	assertPathReleased(t, physical)
	assertPathReleased(t, extra)
}

func TestServicePathExtensionFailurePreservesExistingBundle(t *testing.T) {
	physical, _, extra := pathAdmissionFixture(t)
	blocked := filepath.Join(filepath.Dir(extra), "z-blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	hold, err := workspacelease.Acquire(blocked, false)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	provider := pathAdmissionProvider{fakeProvider: fakeProvider{name: "fixture"}, visit: func(ctx context.Context, phase string) error {
		if phase != "cleanup" {
			return nil
		}
		if release, err := AcquirePaths(ctx, physical, extra, blocked); err == nil {
			release()
			t.Fatal("partial extension admitted")
		}
		assertPathHeld(t, physical)
		assertPathHeld(t, blocked)
		assertPathReleased(t, extra)
		return nil
	}}
	if _, err := New([]integration.CleanupProvider{provider}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Path: physical}}}, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	assertPathReleased(t, physical)
	assertPathReleased(t, extra)
}
