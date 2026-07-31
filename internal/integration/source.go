package integration

import "context"

type Source interface {
	Integration
	Collect(ctx context.Context, req CollectRequest) CollectResult
}

type LocalSource interface {
	Local() bool
}
