package sbx

import (
	"context"
	"testing"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

type fakeMultiplexer struct {
	created  integration.EnsureSessionRequest
	opened   integration.OpenWindowRequest
	existing bool
}

type availableRunner struct{}

func (availableRunner) LookPath(string) error { return nil }
func (availableRunner) Run(context.Context, string, string, ...string) (string, error) {
	return "", nil
}

func (fakeMultiplexer) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "multiplexer"}
}
func (fakeMultiplexer) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (fakeMultiplexer) ClientActive() bool { return false }
func (fakeMultiplexer) Current(context.Context) (integration.SessionContext, bool, error) {
	return integration.SessionContext{}, false, nil
}
func (m *fakeMultiplexer) EnsureSession(_ context.Context, req integration.EnsureSessionRequest) (integration.Session, error) {
	m.created = req
	return integration.Session{Name: req.Name, Created: !m.existing}, nil
}
func (m *fakeMultiplexer) OpenWindow(_ context.Context, req integration.OpenWindowRequest) error {
	m.opened = req
	return nil
}
func (fakeMultiplexer) Switch(context.Context, integration.SessionTarget) error { return nil }
func (fakeMultiplexer) Target(protocol.Task) string                             { return "" }
func (fakeMultiplexer) MatchesCurrent(protocol.SourceRef, protocol.CurrentContext) bool {
	return false
}

func TestOpenShellCreatesSessionThroughMultiplexer(t *testing.T) {
	multiplexer := &fakeMultiplexer{}
	ref := sandbox{Name: "radar-repo-ABC-600-shell", Workspaces: []string{"/work/repo/ABC-600-shell"}}.SourceRef(linking.MarkMatcher{}, "")

	result, err := openShell(context.Background(), availableRunner{}, multiplexer, ref, OpenShellOptions{SwitchClient: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedSession || result.SessionName != "repo-ABC-600-shell" {
		t.Fatalf("result = %+v", result)
	}
	if multiplexer.created.Name != "repo-ABC-600-shell" || multiplexer.created.FirstCommand != "sbx run --name 'radar-repo-ABC-600-shell'" {
		t.Fatalf("request = %+v", multiplexer.created)
	}
}

func TestOpenShellOpensWindowThroughMultiplexer(t *testing.T) {
	multiplexer := &fakeMultiplexer{existing: true}
	ref := sandbox{Name: "radar-repo-shell", Workspaces: []string{"/work/repo/shell"}}.SourceRef(linking.MarkMatcher{}, "")

	result, err := openShell(context.Background(), availableRunner{}, multiplexer, ref, OpenShellOptions{SessionTarget: "$3"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedSession || result.SessionName != "$3" || multiplexer.opened.SessionName != "$3" {
		t.Fatalf("result = %+v request = %+v", result, multiplexer.opened)
	}
}

var _ integration.MultiplexerProvider = (*fakeMultiplexer)(nil)
