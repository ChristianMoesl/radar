package sbx

import (
	"context"
	"testing"

	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/linking"
	"radar/internal/protocol"
)

type fakeMultiplexer struct {
	opened   integration.OpenSessionRequest
	existing bool
	calls    int
	switches int
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
func (fakeMultiplexer) AttachCommand(context.Context, integration.SessionTarget) (integration.SessionAttachment, error) {
	return integration.SessionAttachment{Validate: func() error { return nil }, Release: func() {}}, nil
}
func (fakeMultiplexer) Current(context.Context) (integration.SessionContext, bool, error) {
	return integration.SessionContext{}, false, nil
}
func (m *fakeMultiplexer) OpenSession(_ context.Context, req integration.OpenSessionRequest) (integration.Session, error) {
	m.opened = req
	m.calls++
	return integration.Session{Name: req.Name, Created: !m.existing}, nil
}
func (m *fakeMultiplexer) Switch(context.Context, integration.SessionTarget) error {
	m.switches++
	return nil
}
func (fakeMultiplexer) Target(protocol.Task) string { return "" }
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
	wantCommand := "sbx run --name 'radar-repo-ABC-600-shell'"
	if sbxclient.IsWSL() {
		wantCommand = "env " + sbxclient.CacheEnvironment + "=0 " + wantCommand
	}
	if multiplexer.opened.Name != "repo-ABC-600-shell" || multiplexer.opened.Command != wantCommand || multiplexer.opened.Window != "sbx" || !multiplexer.opened.Switch || multiplexer.calls != 1 || multiplexer.switches != 0 {
		t.Fatalf("request = %+v calls=%d split switches=%d", multiplexer.opened, multiplexer.calls, multiplexer.switches)
	}
}

func TestOpenShellOpensWindowThroughMultiplexer(t *testing.T) {
	multiplexer := &fakeMultiplexer{existing: true}
	ref := sandbox{Name: "radar-repo-shell", Workspaces: []string{"/work/repo/shell"}}.SourceRef(linking.MarkMatcher{}, "")

	result, err := openShell(context.Background(), availableRunner{}, multiplexer, ref, OpenShellOptions{SessionTarget: "$3"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedSession || result.SessionName != "$3" || multiplexer.opened.Name != "$3" || multiplexer.opened.Switch || multiplexer.calls != 1 || multiplexer.switches != 0 {
		t.Fatalf("result = %+v request = %+v", result, multiplexer.opened)
	}
}

var _ integration.MultiplexerProvider = (*fakeMultiplexer)(nil)
