package sbx

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

type leaseMultiplexer struct {
	fakeMultiplexer
	visit func(string) error
}

func (m *leaseMultiplexer) EnsureSession(ctx context.Context, req integration.EnsureSessionRequest) (integration.Session, error) {
	if err := m.visit("ensure"); err != nil {
		return integration.Session{}, err
	}
	return m.fakeMultiplexer.EnsureSession(ctx, req)
}
func (m *leaseMultiplexer) OpenWindow(ctx context.Context, req integration.OpenWindowRequest) error {
	if err := m.visit("window"); err != nil {
		return err
	}
	return m.fakeMultiplexer.OpenWindow(ctx, req)
}
func (m *leaseMultiplexer) Switch(ctx context.Context, target integration.SessionTarget) error {
	return m.visit("switch")
}

func TestOpenShellSandboxNameContentionRejectsAllReferenceForms(t *testing.T) {
	for _, form := range []string{"metadata", "title", "reference-id"} {
		t.Run(form, func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			ref := protocol.SourceRef{Source: "sbx", Kind: "sandbox", Path: t.TempDir()}
			switch form {
			case "metadata":
				ref.Metadata = map[string]string{"name": "shared-runtime"}
			case "title":
				ref.Title = "shared-runtime"
			case "reference-id":
				ref.ID = "sbx:sandbox:shared-runtime"
			}
			release, err := workspacelease.AcquireSandbox("shared-runtime", true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			calls := 0
			mux := &leaseMultiplexer{visit: func(string) error { calls++; return nil }}
			_, err = openShell(context.Background(), availableRunner{}, mux, ref, OpenShellOptions{SwitchClient: true})
			if err == nil || !strings.Contains(err.Error(), "sandbox is in use by another operation") || calls != 0 {
				t.Fatalf("shell bypassed name conflict: %v calls=%d", err, calls)
			}
		})
	}
}

func TestOpenShellHoldsSandboxNameThroughMultiplexerAndReleases(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, outcome := range []string{"success", "ensure", "window", "switch", "cancelled"} {
			if !existing && outcome == "window" {
				continue
			}
			t.Run(map[bool]string{false: "new", true: "existing"}[existing]+"/"+outcome, func(t *testing.T) {
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				visits := 0
				mux := &leaseMultiplexer{fakeMultiplexer: fakeMultiplexer{existing: existing}, visit: func(stage string) error {
					visits++
					if release, err := workspacelease.AcquireSandbox("shared-runtime", true); err == nil {
						release()
						t.Fatal("cleanup acquired named sandbox during shell handoff")
					}
					release, err := workspacelease.AcquireSandbox("unrelated-runtime", true)
					if err != nil {
						t.Fatal(err)
					}
					release()
					if stage == outcome {
						return errors.New("fixture multiplexer failed")
					}
					if outcome == "cancelled" && stage == "switch" {
						cancel()
						return ctx.Err()
					}
					return nil
				}}
				ref := protocol.SourceRef{Source: "sbx", Kind: "sandbox", Title: "shared-runtime", Path: t.TempDir()}
				_, err := openShell(ctx, availableRunner{}, mux, ref, OpenShellOptions{SwitchClient: true})
				if visits == 0 || (err != nil) != (outcome != "success") {
					t.Fatal("unexpected shell outcome", visits, err)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				release, err := workspacelease.AcquireSandbox("shared-runtime", true)
				if err != nil {
					t.Fatal("shell leaked sandbox lease", err)
				}
				release()
			})
		}
	}
}

func TestOpenShellPreCancelledDoesNotEnterMultiplexer(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mux := &leaseMultiplexer{visit: func(string) error { t.Fatal("cancelled shell entered multiplexer"); return nil }}
	_, err := openShell(ctx, availableRunner{}, mux, protocol.SourceRef{Source: "sbx", Kind: "sandbox", Title: "shared-runtime"}, OpenShellOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("pre-admission cancellation lost", err)
	}
	release, err := workspacelease.AcquireSandbox("shared-runtime", true)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
