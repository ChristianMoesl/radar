package integration

import (
	"context"
	"radar/internal/protocol"
)

type ActivityPublisher interface {
	Integration
	PublishActivity(ctx context.Context, activity protocol.Activity) error
}
