package integration

import "context"

type Source interface {
	Name() string
	Collect(ctx context.Context, req CollectRequest) CollectResult
}

type LocalSource interface {
	Local() bool
}
