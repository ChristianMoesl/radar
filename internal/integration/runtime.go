package integration

type RuntimeProvider interface {
	Source
	ActionProvider
	DeleteProvider
}

type CodeReviewProvider interface {
	Source
	Reconciler
}

type WorkTracker interface {
	Source
	Reconciler
}
