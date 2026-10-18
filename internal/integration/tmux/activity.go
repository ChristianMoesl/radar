package tmux

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func (Source) ReadActivity(ctx context.Context, task protocol.Task) (protocol.Activity, error) {
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, ref := range task.SourceRefs {
		if ref.Role != protocol.SourceRefRoleAuthoritative {
			continue
		}
		if ref.Source == "tmux" && ref.Kind == "session" {
			ids[ref.ID] = true
		}
		if ref.ProvidesWorkspace && ref.Path != "" {
			paths[filepath.Clean(ref.Path)] = true
		}
	}
	if len(ids) == 0 && len(paths) == 0 {
		return protocol.ActivityIdle, nil
	}
	refs, status := FetchSessions(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), linking.NewMarkMatcher(nil))
	if status.Status != "ok" {
		// A stopped server has no live panes. Permission errors, malformed
		// output and unavailable commands must still fail closed.
		if status.Detail == "no tmux server running" ||
			(strings.Contains(status.Detail, "error connecting to ") && strings.Contains(status.Detail, "No such file or directory")) {
			return protocol.ActivityIdle, nil
		}
		return protocol.ActivityIdle, fmt.Errorf("%s", status.Detail)
	}
	activity := protocol.ActivityIdle
	for _, ref := range refs {
		// A workspace match also catches sessions created after collection.
		// Stable IDs (not server-local IDs alone) exclude reused sessions.
		matches := ids[ref.ID]
		for path := range paths {
			relative, err := filepath.Rel(path, ref.Path)
			matches = matches || (ref.Path != "" && err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
		}
		if matches {
			activity = protocol.MergeActivity(activity, ref.Activity)
		}
	}
	return activity, nil
}

var _ integration.ActivityReader = Source{}
