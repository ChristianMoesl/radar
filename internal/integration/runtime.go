package integration

import "radar/internal/protocol"

type RuntimeProvider interface {
	Source
	ActionProvider
	CleanupProvider
	ResourceName(ref protocol.SourceRef) (string, bool)
}

type CodeReviewProvider interface {
	Source
	Reconciler
}

type WorkTracker interface {
	Source
	Reconciler
}
