package integration

import (
	"context"
	"fmt"

	"radar/internal/protocol"
)

type ActivityPublisher interface {
	Integration
	PublishActivity(ctx context.Context, activity protocol.Activity) error
}

// ActivityReader verifies current runtime activity at completion/cleanup time.
// Providers match task resources themselves; core never interprets pane IDs.
// An unavailable or malformed observation is an error, not evidence of idle.
type ActivityReader interface {
	Source
	ReadActivity(ctx context.Context, task protocol.Task) (protocol.Activity, error)
}

func ReadTaskActivity(ctx context.Context, task protocol.Task, sources []Source) (protocol.Activity, error) {
	activity := protocol.ActivityIdle
	read := map[string]bool{}
	for _, source := range sources {
		if reader, ok := source.(ActivityReader); ok {
			current, err := reader.ReadActivity(ctx, task)
			if err != nil {
				return activity, fmt.Errorf("could not verify %s activity: %w", source.Descriptor().Name, err)
			}
			if !current.Valid() {
				return activity, fmt.Errorf("invalid %s activity", source.Descriptor().Name)
			}
			read[source.Descriptor().Name] = true
			activity = protocol.MergeActivity(activity, current)
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative && !read[ref.Source] {
			activity = protocol.MergeActivity(activity, ref.Activity)
		}
	}
	return activity, nil
}
