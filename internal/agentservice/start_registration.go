package agentservice

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
)

// Startup owns no authority to overwrite a registration changed while native
// workspace/runner evidence is being obtained. Keep only stable directory/leaf
// identities and a bounded hash, not private extension bytes or a public token.
// Reuse the existing exact native admission file-identity comparisons.
type startRegistration struct {
	rootPath, instancePath string
	root, instance         os.FileInfo
	registration           stopFile
}

func startRegistrationUnavailable() error {
	return errors.New("agent registration changed or unavailable; refusing startup")
}

func captureStartRegistration(ctx context.Context, expected Registration) (startRegistration, error) {
	var capture startRegistration
	if diagnosticRegistrationExpired(ctx) {
		return capture, startRegistrationUnavailable()
	}
	dir, err := instanceDir(expected.ID)
	if err != nil {
		return capture, startRegistrationUnavailable()
	}
	capture.instancePath, capture.rootPath = dir, filepath.Dir(dir)
	capture.root, err = os.Lstat(capture.rootPath)
	if err != nil || !capture.root.IsDir() {
		return startRegistration{}, startRegistrationUnavailable()
	}
	capture.instance, err = os.Lstat(dir)
	if err != nil || !capture.instance.IsDir() {
		return startRegistration{}, startRegistrationUnavailable()
	}
	// The existing reader is no-follow, nonblocking and bounded, and verifies
	// descriptor/path/directory stability. It never creates or repairs data.
	data, info, err := diagnosticCheckpoint(filepath.Join(dir, "registration.json"))
	if err != nil {
		return startRegistration{}, startRegistrationUnavailable()
	}
	reg, err := decodeRegistration(data, expected.ID)
	if err != nil || reg != expected {
		return startRegistration{}, startRegistrationUnavailable()
	}
	capture.registration = stopFile{info: info, hash: sha256.Sum256(data)}
	root, rootErr := os.Lstat(capture.rootPath)
	instance, instanceErr := os.Lstat(dir)
	if rootErr != nil || instanceErr != nil || !sameStopFile(capture.root, root) || !sameStopFile(capture.instance, instance) || diagnosticRegistrationExpired(ctx) {
		return startRegistration{}, startRegistrationUnavailable()
	}
	return capture, nil
}

func (capture startRegistration) current(ctx context.Context, expected Registration) bool {
	after, err := captureStartRegistration(ctx, expected)
	return err == nil && capture.rootPath == after.rootPath && capture.instancePath == after.instancePath &&
		sameStopFile(capture.root, after.root) && sameStopFile(capture.instance, after.instance) &&
		sameStopLeaf(capture.registration, after.registration)
}
