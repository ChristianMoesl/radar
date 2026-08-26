package integration

import "context"

type ActivityPublisher interface {
	Integration
	PublishActivity(ctx context.Context, busy bool) error
}
