package agentservice

import (
	"context"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Called under manager serialization after entering the single existing native
// effect sequence, independently of its returned error. Reuse native bounded
// no-follow registration/owner/socket capture and exact runner/owner checks, not
// cached snapshots or private checkpoints. Original directories bind both reads;
// all observed leaves must remain stable across the live probe. Parameters allow
// deterministic read-boundary tests, not replaceable execution hooks in plans.
func (c *stopCapture) observeStop(ctx context.Context,
	capture func(string) (stopFiles, Registration, error),
	probe func(context.Context, Registration) (protocol.AgentStatus, error),
) integration.AgentStopObservation {
	var observed integration.AgentStopObservation
	live := func() bool { return removalContextError(ctx) == nil && time.Now().Before(c.expires) }
	if !live() {
		return observed
	}
	before, reg, err := capture(c.reg.ID)
	sameDirectories := func(files stopFiles) bool {
		return files.rootPath == c.files.rootPath && files.socketPath == c.files.socketPath &&
			sameStopFile(c.files.root, files.root) && sameStopFile(c.files.instance, files.instance)
	}
	if err != nil || !sameDirectories(before) || !live() {
		return observed
	}
	stopped := c.reg
	stopped.Desired = false
	if reg == stopped {
		observed.Registration = integration.AgentStopRegistrationStopped
	} else if reg == c.reg && sameStopLeaf(c.files.registration, before.registration) {
		observed.Registration = integration.AgentStopRegistrationOriginal
	} else {
		// A replaced/changed definition cannot authorize a post-attempt probe.
		return observed
	}
	status, probeErr := probe(ctx, reg)
	if !live() {
		return integration.AgentStopObservation{}
	}
	alive, ownerErr := ownerAlive(reg)
	if probeErr == nil && status.Running {
		observed.Runner = integration.AgentStopRunnerPresent
	} else if probeErr == nil && !status.Running && ownerErr == nil && !alive && (!c.running || !runnerProcessAlive(c.pid)) {
		observed.Runner = integration.AgentStopRunnerAbsent
	}
	if !live() {
		return integration.AgentStopObservation{}
	}
	after, _, err := capture(c.reg.ID)
	if err != nil || !sameDirectories(after) || !before.same(after) || !live() {
		return integration.AgentStopObservation{}
	}
	observed.ObservedAt = time.Now().UTC()
	return observed
}
