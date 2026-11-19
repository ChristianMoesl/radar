package workspace

import (
	"context"
	"fmt"
	"time"

	"radar/internal/integration/tmux/sessionlease"
)

// One registered operation owns both the reserved name and its selected or
// newly emitted incarnation until its final native effect. Missing admission
// must not turn into permission to adopt a runtime created by another actor.
type workspaceSession struct {
	name      string
	admission *sessionlease.Set
	identity  sessionlease.Identity
	selected  bool
}

func newWorkspaceSession(name string) *workspaceSession {
	return &workspaceSession{name: name, admission: sessionlease.New(false)}
}
func (s *workspaceSession) close() { s.admission.Close() }

func (s *workspaceSession) probe(ctx context.Context, runner Runner, cwd string) (bool, error) {
	if s.selected && s.identity.ID != "" {
		return true, s.admit(ctx, runner)
	}
	_, probeErr := runner.Run(ctx, cwd, "tmux", "has-session", "-t", "="+s.name)
	if err := s.admit(ctx, runner); err != nil {
		return false, err
	}
	if (probeErr == nil) != (s.identity.ID != "") {
		return false, fmt.Errorf("tmux session changed before recovery; refresh and retry")
	}
	return probeErr == nil, nil
}

func (s *workspaceSession) admit(ctx context.Context, runner Runner) error {
	request := sessionlease.Request{Selector: "=" + s.name, Name: s.name, AllowMissing: true}
	if s.selected && s.identity.ID != "" {
		request.Selector = s.identity.ID
		request.Expected = s.identity.Key()
		request.AllowMissing = false
	}
	current, err := s.admission.Admit(ctx, runner, request)
	if err != nil {
		return err
	}
	if s.selected && s.identity.ID == "" && current.ID != "" {
		return fmt.Errorf("tmux session changed before recovery; refresh and retry")
	}
	s.identity, s.selected = current, true
	return nil
}

func (s *workspaceSession) run(ctx context.Context, runner Runner, cwd string, args ...string) (string, error) {
	if err := s.admit(ctx, runner); err != nil {
		return "", err
	}
	if s.identity.ID == "" {
		return "", fmt.Errorf("tmux session no longer exists; refresh and retry")
	}
	return runner.Run(ctx, cwd, "tmux", args...)
}

func (s *workspaceSession) switchClient(ctx context.Context, runner Runner, cwd string) error {
	_, err := s.run(ctx, runner, cwd, "switch-client", "-t", s.identity.ID)
	return err
}

// An earlier canceled/failed admission may already have closed its Set. Any
// best-effort post-failure effect must freshly verify the original incarnation,
// independently of cancellation, never follow its reusable name.
func (s *workspaceSession) afterFailure(ctx context.Context, runner Runner, cwd string, timeout time.Duration, args ...string) {
	if s.identity.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	admission := sessionlease.New(false)
	defer admission.Close()
	if _, err := admission.Admit(ctx, runner, sessionlease.Request{Selector: s.identity.ID, Name: s.name, Expected: s.identity.Key()}); err != nil {
		return
	}
	_, _ = runner.Run(ctx, cwd, "tmux", args...)
}
