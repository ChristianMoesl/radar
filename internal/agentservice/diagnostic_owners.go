package agentservice

import (
	"context"
	"fmt"
	"path/filepath"
)

// DiagnosticWorkspaceRemovalOwners observes only registrations using the native
// decoder and removal-owner predicate. The private fingerprint detects registry
// drift, not execution eligibility. No runner, checkpoint, token or lease is read.
// The narrower diagnostic scan fails unavailable rather than returning a partial
// count when any registration is unsafe/invalid or its scan budget is exceeded.
// Up to 512 absolute target paths share one bounded registry pass, not one scan
// per resource. Counts are per path, not unique owners of the whole bundle.
func DiagnosticWorkspaceRemovalOwners(ctx context.Context, paths []string) (map[string]int, [32]byte, error) {
	return diagnosticWorkspaceRemovalOwners(ctx, paths, nil)
}

// DiagnosticWorkspaceOwnerIDs supplies the same complete bounded registration
// observation for inventories that already expose owner identities. Partial IDs
// are discarded on any failure; no second scan or runtime/private-file read.
func DiagnosticWorkspaceOwnerIDs(ctx context.Context, paths []string) (map[string][]string, error) {
	owners := make(map[string][]string)
	if _, _, err := diagnosticWorkspaceRemovalOwners(ctx, paths, owners); err != nil {
		return nil, err
	}
	return owners, nil
}

func diagnosticWorkspaceRemovalOwners(ctx context.Context, paths []string, owners map[string][]string) (map[string]int, [32]byte, error) {
	var empty [32]byte
	unavailable := func() (map[string]int, [32]byte, error) {
		return nil, empty, fmt.Errorf("registered ownership evidence unavailable")
	}
	if len(paths) < 1 || len(paths) > 512 || diagnosticRegistrationExpired(ctx) {
		return unavailable()
	}
	counts := make(map[string]int, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) || len(path) > 4096 {
			return unavailable()
		}
		counts[path] = 0
		if owners != nil {
			owners[path] = []string{}
		}
	}
	fingerprint, err := diagnosticRegistrationScan(ctx, func(reg Registration) {
		for path := range counts {
			if registrationWithinWorkspace(reg, filepath.Clean(path)) {
				counts[path]++ // Desired=false still holds ownership, exactly as production.
				if owners != nil {
					owners[path] = append(owners[path], reg.ID)
				}
			}
		}
	})
	if err != nil {
		return unavailable()
	}
	return counts, fingerprint, nil
}
