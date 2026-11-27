package agentservice

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"radar/internal/integration"
)

// Hash the complete decoded native state, including schedule identity/config,
// pending payloads and admission metadata. No second planner or private payload
// retention; both preparation and observation reuse the native decoder/transform.
func removalScheduleFingerprint(state scheduleState) ([32]byte, error) {
	data, err := json.Marshal(state)
	return sha256.Sum256(data), err
}

// Caller holds manager serialization after the single executor was entered.
// Bounded no-follow reads are independent of its return error. Bind ORIGINAL
// directory identities around BOTH reads; diagnosticCheckpoint additionally
// binds each open directory/leaf across its own read. These are observations,
// not an atomic transaction or absence beyond this registration. The reader
// parameter enables deterministic boundary tests, not replaceable plan hooks.
func (c *removeCapture) observeRemoval(ctx context.Context, read func(string) ([]byte, os.FileInfo, error)) integration.AgentRemoveObservation {
	var observed integration.AgentRemoveObservation
	dir := filepath.Join(c.files.rootPath, c.reg.ID)
	current := func() bool {
		root, rootErr := os.Lstat(c.files.rootPath)
		instance, instanceErr := os.Lstat(dir)
		return ctx.Err() == nil && time.Now().Before(c.expires) && rootErr == nil && instanceErr == nil &&
			root.IsDir() && instance.IsDir() && sameStopFile(c.files.root, root) && sameStopFile(c.files.instance, instance)
	}
	if !current() {
		return observed
	}
	data, info, err := read(filepath.Join(dir, "registration.json"))
	registrationInfo := info
	if errors.Is(err, os.ErrNotExist) {
		observed.Registration = integration.AgentRemoveRegistrationAbsent
	} else if err == nil && sameStopLeaf(c.files.registration, stopFile{info: info, hash: sha256.Sum256(data)}) {
		observed.Registration = integration.AgentRemoveRegistrationPresent
	}
	if !current() {
		return integration.AgentRemoveObservation{}
	}
	data, info, err = read(filepath.Join(dir, "schedules.json"))
	leaf := stopFile{}
	if err == nil {
		leaf = stopFile{info: info, hash: sha256.Sum256(data)}
	}
	if c.preview.Schedules == 0 {
		// Native pause does not rewrite/create a zero-schedule checkpoint. Do
		// not equate a replacement empty file to preservation of the original.
		if (err == nil || errors.Is(err, os.ErrNotExist)) && sameStopLeaf(c.schedule, leaf) {
			observed.Schedules = integration.AgentRemoveSchedulesPaused
		}
	} else if err == nil && ctx.Err() == nil {
		state, decodeErr := decodeSchedules(data)
		fingerprint, hashErr := removalScheduleFingerprint(state)
		if decodeErr == nil && hashErr == nil && fingerprint == c.pausedSchedule {
			observed.Schedules = integration.AgentRemoveSchedulesPaused
		} else if decodeErr == nil && sameStopLeaf(c.schedule, leaf) {
			observed.Schedules = integration.AgentRemoveSchedulesUnchanged
		}
	}
	if !current() {
		return integration.AgentRemoveObservation{}
	}
	// Close the cross-checkpoint interval with leaf metadata checks as well.
	// In particular, an observed reappearing registration must not retain an
	// earlier absence fact. No claim is made about unobserved/later changes.
	stableLeaf := func(path string, captured os.FileInfo) bool {
		now, err := os.Lstat(path)
		if captured == nil {
			return errors.Is(err, os.ErrNotExist)
		}
		return err == nil && sameStopLeaf(stopFile{info: captured}, stopFile{info: now})
	}
	if !stableLeaf(filepath.Join(dir, "registration.json"), registrationInfo) {
		observed.Registration = integration.AgentRemoveRegistrationUnknown
	}
	if !stableLeaf(filepath.Join(dir, "schedules.json"), info) {
		observed.Schedules = integration.AgentRemoveSchedulesUnknown
	}
	if !current() {
		return integration.AgentRemoveObservation{}
	}
	// Timestamp bounds these completed reads, including explicitly unknown
	// fields. It is not a freshness promise for a later reader or resource.
	observed.ObservedAt = time.Now().UTC()
	return observed
}
