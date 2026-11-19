package workspacelease

// AcquireMutation serializes short-lived workspace writers independently of
// deletion admission. Active agents may keep their shared deletion lease while
// reconciliation or runtime recovery owns this exclusive mutation lease.
// Callers must also retain the ordinary lease to exclude cleanup.
func AcquireMutation(path string) (func(), error) {
	dir, name, err := location(path)
	if err != nil {
		return nil, err
	}
	return acquire(dir, "mutation-"+name, true, "workspace mutation", path)
}

// AcquireSandboxMutation protects the reusable SBX name even when no runtime
// exists, or when another registered workspace refers to the same sandbox.
func AcquireSandboxMutation(name string) (func(), error) {
	return acquireSandbox(name, true, "mutation-", "sandbox mutation")
}
