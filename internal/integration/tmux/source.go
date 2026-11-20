package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "tmux", Label: "tmux", DisplayOrder: 3, CleanupOrder: 0}
}

func (Source) Local() bool {
	return true
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status := SourceStatus(ctx)
	return integration.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	sourceRefs, status := FetchSessions(ctx, req.Logger, req.LinkingMarks)
	cleanup.ObserveIssues(ctx, Source{}, sourceRefs)
	if status.Status == "error" {
		req.Logger.Warn("tmux session collection failed", "detail", status.Detail)
		return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress)}
	}
	return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress), Complete: status.Status == "ok"}
}

func (Source) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if req.Mode == integration.CleanupRetire {
		return workspace.RuntimeCleanupTargets(req.Task, "tmux")
	}
	_ = ctx
	targets := make([]protocol.CleanupTarget, 0)
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != "tmux" || ref.Kind != "session" {
			continue
		}
		session := SessionTarget(protocol.Task{Kind: "session", SourceRefs: []protocol.SourceRef{ref}})
		if session == "" {
			return nil, fmt.Errorf("tmux session has no cleanup target")
		}
		targets = append(targets, protocol.CleanupTarget{
			SourceRefID:  ref.ID,
			Source:       "tmux",
			Presentation: protocol.CleanupPresentation{Singular: "terminal session", Plural: "terminal sessions"},
			Kind:         "session",
			Title:        ref.Title, Path: ref.Path, ResourceRole: "multiplexer", ResourceID: session,
			Description: "tmux session " + session,
		})
	}
	return targets, nil
}

func (Source) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	bound, release, err := admitCleanup(ctx, workspace.ExecRunner{}, []protocol.CleanupTarget{req.Target}, req.Mode, false)
	if err != nil {
		return req.Target, err
	}
	defer release()
	return bound.Cleanup(ctx, req)
}

func cleanupSessionTarget(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget, verify func() (sessionlease.Identity, error)) (protocol.CleanupTarget, error) {
	remove := func() error {
		identity, err := verify()
		if err != nil || identity.ID == "" {
			return err
		}
		_, err = runner.Run(ctx, "", "tmux", "kill-session", "-t", identity.ID)
		return err
	}
	root, err := workspace.DefaultRoot()
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	for _, group := range registry.Workspaces {
		// Preview uses the runtime session ID as ResourceID and keeps the
		// registered session name in Title. Direct name targets work too.
		if group.SessionName == "" || (group.SessionName != target.Title && group.SessionName != target.ResourceID) {
			continue
		}
		err := workspacegroup.WithNoteLock(root, func() error {
			registry, err := workspacegroup.Load(root)
			if err != nil {
				return err
			}
			current, found := workspacegroup.FindByID(registry, group.ID)
			if !found || current.SessionName != group.SessionName {
				return fmt.Errorf("managed tmux session registration changed; preview cleanup again")
			}
			return remove()
		})
		if err != nil {
			return protocol.CleanupTarget{}, err
		}
		return target, nil
	}
	if err := remove(); err != nil {
		return protocol.CleanupTarget{}, err
	}
	return target, nil
}

func (Source) ClientActive() bool {
	return os.Getenv("TMUX") != ""
}

func (Source) PublishActivity(ctx context.Context, publication integration.ActivityPublication) error {
	activity := publication.Activity
	if !activity.Valid() {
		return fmt.Errorf("invalid activity %q", activity)
	}
	for _, id := range publication.PromptIDs {
		if !validPromptIdentity(id) {
			return fmt.Errorf("invalid prompt identity")
		}
	}
	if activity != protocol.ActivityWaiting && len(publication.PromptIDs) != 0 {
		return fmt.Errorf("prompt identities require waiting activity")
	}
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if pane == "" {
		return nil
	}
	args := []string{"set-option", "-p", "-t", pane, "@radar_activity", activity.String()}
	if activity == protocol.ActivityIdle {
		args = []string{"set-option", "-p", "-u", "-t", pane, "@radar_activity"}
	}
	if len(publication.PromptIDs) > 0 {
		args = append([]string{"set-option", "-p", "-t", pane, "@radar_prompt_ids", strings.Join(publication.PromptIDs, ","), ";"}, args...)
	} else {
		args = append(args, ";", "set-option", "-p", "-u", "-t", pane, "@radar_prompt_ids")
	}
	_, err := workspace.ExecRunner{}.Run(ctx, "", "tmux", args...)
	return err
}

func validPromptIdentity(id string) bool {
	return id != "" && len(id) <= 128 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) < 0
}

func (Source) Current(ctx context.Context) (integration.SessionContext, bool, error) {
	if !(Source{}).ClientActive() {
		return integration.SessionContext{}, false, nil
	}
	output, err := workspace.ExecRunner{}.Run(ctx, "", "tmux", "display-message", "-p", "#{session_name}\t#{session_id}\t#{pane_current_path}")
	if err != nil {
		return integration.SessionContext{}, false, err
	}
	fields := strings.Split(output, "\t")
	if len(fields) < 2 {
		return integration.SessionContext{}, false, fmt.Errorf("unexpected tmux current context output: %q", output)
	}
	current := integration.SessionContext{Name: fields[0], ID: fields[1]}
	if len(fields) > 2 {
		current.Path = fields[2]
	}
	return current, true, nil
}

func (Source) OpenSession(ctx context.Context, req integration.OpenSessionRequest) (integration.Session, error) {
	return openSession(ctx, workspace.ExecRunner{}, req)
}

func (Source) Target(task protocol.Task) string {
	return SessionTarget(task)
}

func (Source) MatchesCurrent(ref protocol.SourceRef, current protocol.CurrentContext) bool {
	return SessionRefMatchesCurrent(ref, current)
}

func (Source) Switch(ctx context.Context, target integration.SessionTarget) error {
	return switchSession(ctx, workspace.ExecRunner{}, target)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.CleanupProvider = Source{}
var _ integration.MultiplexerProvider = Source{}
