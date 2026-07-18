package integration

type RuntimeProvider interface {
	Source
	ActionProvider
	CleanupProvider
}

type CodeReviewProvider interface {
	Source
	Reconciler
}

type WorkTracker interface {
	Source
	Reconciler
}
