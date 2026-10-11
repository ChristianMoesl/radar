package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"radar"
	"radar/internal/command"
	"radar/internal/update"
	"radar/internal/version"
)

// Each profile owns its own receipt and lock. Only successful, eligible package
// updates are recorded, so a failure or a subsequently removed pin can be retried
// at the next daemon startup without updating already-synchronized packages.
type syncReceipt struct {
	RadarVersion string            `json:"radar_version"`
	Packages     map[string]string `json:"packages"`
}

// SyncPackages runs once at daemon startup, after its socket is listening. The
// caller captures the startup profile explicitly; no profile discovery or global
// environment mutation is performed here. Failures are retryable on restart.
func SyncPackages(ctx context.Context, profile, radarVersion string, logger *slog.Logger) error {
	// Development builds have no corresponding published Pi release. Do not
	// replace their extensions with npm latest on every rebuild.
	if !update.Stable(radarVersion) {
		logger.Debug("Pi package synchronization skipped for non-release Radar", "version", radarVersion)
		return nil
	}
	radarVersion = strings.TrimPrefix(radarVersion, "v")
	profile, err := filepath.EvalSymlinks(profile)
	if os.IsNotExist(err) {
		logger.Debug("Pi package synchronization skipped: profile does not exist")
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve Pi profile: %w", err)
	}
	profile, err = filepath.Abs(profile)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logger = logger.With("profile", profile, "radar_version", radarVersion)
	directory := filepath.Join(profile, "radar")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "package-sync.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	// Closing (rather than explicitly unlocking) lets an inherited descriptor
	// retain the lock while a Pi process survives an abrupt daemon termination.
	defer lock.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	path := filepath.Join(directory, "package-sync.json")
	receipt, err := readSyncReceipt(path)
	if err != nil {
		return fmt.Errorf("read Pi synchronization receipt: %w; inspect/remove %s and restart Radar", err, path)
	}
	if receipt.RadarVersion != radarVersion {
		receipt = syncReceipt{RadarVersion: radarVersion, Packages: make(map[string]string)}
	}
	var failures []error
	prerequisitesChecked := false
	for _, name := range []string{update.Package, SandboxPackage} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := version.CheckInstalled(); err != nil {
			return err
		}
		// The daemon is syncing a user profile, not the directory it happened to
		// start in. Pi itself ignores project configuration via --no-approve.
		before := inspectRelease(profile, home, name)
		if !before.CanUpdate {
			logger.Info("Pi package left unchanged", "package", name, "reason", before.Reason)
			continue
		}
		if receipt.Packages[name] == before.Installed {
			logger.Debug("Pi package already synchronized", "package", name, "installed", before.Installed)
			continue
		}
		if !prerequisitesChecked {
			checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := checkSyncPrerequisites(checkCtx)
			cancel()
			if err != nil {
				return err
			}
			prerequisitesChecked = true
		}
		logger.Info("Synchronizing Pi package", "package", name, "installed", before.Installed)
		packageCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		after, err := updateRelease(packageCtx, before, home, lock)
		cancel()
		if err == nil && name == update.Package && after.Installed != radarVersion {
			err = fmt.Errorf("pi-radar %s does not match Radar %s; npm latest may not yet match the CLI release", after.Installed, radarVersion)
		}
		if err != nil {
			logger.Warn("Pi package synchronization failed; retry with radar restart", "package", name, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := version.CheckInstalled(); err != nil {
			return err
		}
		receipt.Packages[name] = after.Installed
		if err := writeSyncReceipt(path, receipt); err != nil {
			return fmt.Errorf("record Pi package synchronization: %w", err)
		}
		logger.Info("Pi package synchronized; running sessions left untouched", "package", name, "previous", before.Installed, "installed", after.Installed)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(failures...)
}

func readSyncReceipt(path string) (syncReceipt, error) {
	receipt := syncReceipt{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return receipt, nil
	}
	if err != nil {
		return receipt, err
	}
	if len(data) > 16384 {
		return receipt, errors.New("receipt is too large")
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return receipt, err
	}
	if !update.Stable(receipt.RadarVersion) || receipt.Packages == nil {
		return receipt, errors.New("invalid synchronization receipt")
	}
	for name, installed := range receipt.Packages {
		if !supportedPackage(name) || !update.Stable(installed) {
			return receipt, errors.New("invalid synchronization package receipt")
		}
	}
	return receipt, nil
}

func writeSyncReceipt(path string, receipt syncReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".package-sync-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func checkSyncPrerequisites(ctx context.Context) error {
	// Pi deliberately treats an offline package update as a successful no-op.
	// Do not turn that no-op into a persisted synchronization success.
	switch strings.ToLower(os.Getenv("PI_OFFLINE")) {
	case "1", "true", "yes":
		return errors.New("Pi is offline; restart Radar without PI_OFFLINE to retry package synchronization")
	}
	node, pi, err := radar.PiPrerequisites()
	if err != nil {
		return err
	}
	for _, spec := range []struct{ name, minimum string }{{"node", node}, {"pi", pi}} {
		output, err := command.CommandContext(ctx, spec.name, "--version").Output()
		if err != nil {
			return fmt.Errorf("%s is required for Pi package synchronization: %w", spec.name, err)
		}
		installed := strings.TrimPrefix(strings.TrimSpace(string(output)), "v")
		if !update.Stable(installed) || update.Compare(installed, spec.minimum) < 0 {
			return fmt.Errorf("%s >= %s required; Radar will not update Pi core or Node", spec.name, spec.minimum)
		}
	}
	return nil
}
