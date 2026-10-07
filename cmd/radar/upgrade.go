package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"time"

	"radar/internal/client"
	"radar/internal/operationlock"
	"radar/internal/pi"
	"radar/internal/process"
	"radar/internal/socket"
	"radar/internal/update"
	"radar/internal/version"
)

func runUpgrade(args []string) {
	fromTUI := len(args) == 1 && args[0] == "--from-tui"
	if len(args) > 0 && !fromTUI {
		fatal(errors.New("usage: radar upgrade"))
	}
	err := upgrade(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Radar upgrade:", err)
	}
	if fromTUI {
		fmt.Fprint(os.Stdout, "\nPress Enter to return to Radar. ")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	if err != nil {
		os.Exit(1)
	}
}
func confirmUpgrade(scanner *bufio.Scanner, out io.Writer, text string) bool {
	fmt.Fprintf(out, "%s [y/N] ", text)
	return scanner.Scan() && strings.EqualFold(strings.TrimSpace(scanner.Text()), "y")
}
func upgrade(in io.Reader, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("in-app upgrades are macOS-only; use make install on this platform")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	prefix, err := update.DefaultPrefix()
	if err != nil {
		return err
	}
	if executable != filepath.Join(prefix, "bin/radar") {
		return errors.New("run the installed ~/.local/bin/radar; development, symlink and package-manager installations stay manual")
	}
	release, err := update.AcquireInstallation(prefix)
	if err != nil {
		return err
	}
	defer release()
	scanner := bufio.NewScanner(in)
	journal, err := update.ReadJournal(prefix)
	if err != nil {
		return err
	}
	path, err := socket.Path()
	if err != nil {
		return err
	}
	if journal != nil && !journal.Committed {
		if !confirmUpgrade(scanner, out, "Interrupted update found. Restore the previous CLI/notifier (Pi and application data are not rolled back)?") {
			return nil
		}
		unlock, err := operationlock.Acquire(true)
		if err != nil {
			return err
		}
		defer unlock()
		if err := stopUpgradeDaemon(path); err != nil {
			return err
		}
		if err := update.Recover(prefix); err != nil {
			return err
		}
		identity, err := restoredIdentity(executable, journal.PreviousBinary)
		if err != nil {
			return err
		}
		if journal.ChangeNotifier && journal.PreviousNotifier != "" {
			if err := registerUpgradeNotifier(filepath.Join(prefix, "libexec/radar/RadarNotifier.app")); err != nil {
				return err
			}
		}
		if err := startUpgradeDaemon(path, executable, identity); err != nil {
			return err
		}
		fmt.Fprintln(out, "Previous installation restored and its daemon verified. Reopen Radar before attempting another upgrade.")
		return nil
	}
	installed, err := update.Inspect(executable)
	if err != nil {
		return err
	}
	current := version.Number
	if installed.Receipt == nil {
		current = ""
	} // Explicit adoption may reinstall the same version to authenticate it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	service := update.NewClient()
	fmt.Fprintln(out, "Checking authenticated GitHub releases and exact npm availability…")
	target, err := service.Latest(ctx, current)
	if err != nil {
		return err
	}
	if target == nil {
		fmt.Fprintln(out, "No newer coordinated stable release is ready.")
		cwd, _ := os.Getwd()
		printPiReleaseState(out, pi.InspectRelease(cwd), strings.TrimPrefix(version.Number, "v"))
		return nil
	}
	osVersion, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
	if err != nil {
		return err
	}
	if update.Compare(strings.TrimSpace(string(osVersion)), target.MinMacOS) < 0 {
		return fmt.Errorf("release requires macOS %s or newer", target.MinMacOS)
	}
	baseline := strings.SplitN(strings.TrimPrefix(version.Number, "v"), "-", 2)[0]
	if update.Stable(baseline) && update.Compare(target.Version, baseline) < 0 {
		return errors.New("refusing to downgrade Radar")
	}
	artifact, ok := target.Artifacts[runtime.GOARCH]
	if !ok {
		return errors.New("this release does not support the running architecture")
	}
	cwd, _ := os.Getwd()
	integration := pi.InspectRelease(cwd)
	fmt.Fprintf(out, "\nRadar: %s → %s (%s)\nInstall: %s\n", version.Number, target.Version, runtime.GOARCH, prefix)
	if installed.NotifierSHA256 == artifact.NotifierSHA256 {
		fmt.Fprintf(out, "Notifier %s unchanged: installed app will not be touched.\n", artifact.NotifierVersion)
	} else {
		old := "unmanaged/missing"
		if installed.Receipt != nil {
			old = installed.Receipt.NotifierVersion
		}
		fmt.Fprintf(out, "Notifier: %s → %s. macOS may require Open Anyway again; use N in Radar afterward.\n", old, artifact.NotifierVersion)
	}
	printPiReleaseState(out, integration, target.PiVersion)
	fmt.Fprintln(out, "The daemon/dashboard will restart; workspaces, tmux, Pi and sandboxes stay running. Previous files and a recovery journal are retained. Pi is a separate operation, not an atomic part of the CLI update.")
	if installed.Receipt == nil {
		fmt.Fprintln(out, "Before first adoption, close Radar dashboards that predate this updater. Leave tmux, Pi sessions and workspaces running.")
	}
	if installed.Receipt == nil && !confirmUpgrade(scanner, out, "Adopt this manual/source installation into managed GitHub releases?") {
		return nil
	}
	updatePi := false
	if integration.CanUpdate && integration.Installed != target.PiVersion {
		fmt.Fprintf(out, "Exact Pi target: PI_CODING_AGENT_DIR=%q pi install npm:%s@%s\nThis changes only pi-radar to an exact Radar-managed pin. Custom/pinned/disabled/project packages are not adopted.\n", integration.Profile, update.Package, target.PiVersion)
		updatePi = confirmUpgrade(scanner, out, "Also install/update this Pi package and consent to the exact pin?")
		if updatePi {
			prerequisiteCtx, prerequisiteCancel := context.WithTimeout(context.Background(), 20*time.Second)
			prerequisiteErr := pi.CheckReleasePrerequisites(prerequisiteCtx, *target)
			prerequisiteCancel()
			if err := prerequisiteErr; err != nil {
				return err
			}
		}
	}
	question := "Download, verify and install this Radar release now?"
	if !updatePi && integration.Installed != target.PiVersion {
		fmt.Fprintln(out, "Pi will remain as detected above. An older/custom pi-radar integration may not be compatible with the new CLI.")
		question = "Download, verify and install Radar while leaving Pi unchanged?"
	}
	if !confirmUpgrade(scanner, out, question) {
		return nil
	}
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Refresh eligibility after potentially long interactive approval. Nothing has
	// been replaced yet; a manifest changed under an immutable tag is rejected.
	fresh, err := service.ReadManifest(ctx, target.Version)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(fresh, *target) {
		return errors.New("release changed during confirmation; refusing to proceed")
	}
	if err := service.NPMReady(ctx, fresh); err != nil {
		return err
	}
	fmt.Fprintln(out, "Downloading and verifying; the current installation remains active…")
	downloadCtx, downloadCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer downloadCancel()
	staged, err := update.Stage(downloadCtx, service, installed, fresh, runtime.GOARCH)
	if err != nil {
		return err
	}
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
	verifyErr := exec.CommandContext(verifyCtx, "codesign", "--verify", "--strict", filepath.Join(staged.Root, "libexec/radar/RadarNotifier.app")).Run()
	verifyCancel()
	if verifyErr != nil {
		return fmt.Errorf("notifier code signature verification failed: %w", verifyErr)
	}
	unlock, err := operationlock.Acquire(true)
	if err != nil {
		return err
	}
	originalIdentity := version.Current()
	health := func(binary, identity string) error { return startUpgradeDaemon(path, binary, identity) }
	err = staged.Activate(update.Hooks{Stop: func() error { return stopUpgradeDaemon(path) }, Health: health, Register: registerUpgradeNotifier, RestartPrevious: func() error { return health(executable, originalIdentity) }})
	unlock()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Radar %s installed; the new daemon is healthy.\n", fresh.Version)
	if staged.Journal.ChangeNotifier {
		fmt.Fprintln(out, "Notification setup may be required: press N in Radar. Gatekeeper approval and notification authorization are separate.")
	}
	if updatePi {
		packageCtx, packageCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer packageCancel()
		if err := pi.InstallRelease(packageCtx, integration, cwd, fresh.PiVersion); err != nil {
			return err
		}
		fmt.Fprintf(out, "Pi package %s verified. Existing Pi sessions still need /reload or restart at a safe point.\n", fresh.PiVersion)
	} else {
		fmt.Fprintln(out, "Pi package left unchanged. Any version mismatch needs a separate, targeted Pi update; running sessions were not reloaded.")
	}
	return nil
}

// Only stop the daemon answering this socket, never every radar-like process on
// the host. The PID is returned by the same health endpoint used for verification.
func stopUpgradeDaemon(path string) error {
	r, err := client.CallWithTimeout(path, "version", 2*time.Second)
	if err != nil {
		pid, pidErr := process.ReadPID()
		if os.IsNotExist(pidErr) || (pidErr == nil && !process.Running(pid)) {
			return nil
		}
		return errors.New("daemon is not responding; stop/restart it explicitly before upgrading")
	}
	if !r.OK || r.PID <= 0 || r.PID == os.Getpid() {
		return errors.New("restart Radar with the current binary before upgrading; daemon identity is unavailable")
	}
	p, err := os.FindProcess(r.PID)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	for range 100 {
		if !process.Running(r.PID) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("daemon did not stop; no files should be activated while it is still running")
}

func startUpgradeDaemon(path, binary, identity string) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	child, err := os.StartProcess(binary, []string{binary, "daemon"}, &os.ProcAttr{Files: []*os.File{devNull, devNull, devNull}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { _, err := child.Wait(); done <- err }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("new daemon exited before becoming healthy (%v); inspect Radar logs", err)
		default:
		}
		response, err := client.CallWithTimeout(path, "version", time.Second)
		if err == nil && response.OK && response.PID == child.Pid && response.Version == identity {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// This is our own newly started child, not a PID discovered by a broad scan.
	// Stop a broken/unresponsive new daemon before filesystem rollback.
	_ = child.Kill()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		return errors.New("new daemon failed health verification and could not be reaped; preserve recovery data")
	}
	return errors.New("new daemon did not become healthy with the expected build identity")
}
func restoredIdentity(binary, digest string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(output), "\n")
	number, ok := strings.CutPrefix(first, "radar ")
	if !ok || number == "" || len(number) > 256 {
		return "", errors.New("could not determine restored Radar version")
	}
	return number + "+" + digest, nil
}
func registerUpgradeNotifier(app string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", "-f", app).Run()
}
func printPiReleaseState(out io.Writer, s pi.ReleaseState, target string) {
	fmt.Fprintf(out, "Pi profile: %s\nPi package installed: %s; target: %s\n%s\n", s.Profile, s.Installed, target, s.Reason)
	if len(s.Loaded) == 0 {
		fmt.Fprintln(out, "Loaded Pi versions: not reported (older/non-Radar sessions may be running).")
	}
	for _, r := range s.Loaded {
		status := "already loaded"
		if r.Version != target {
			status = "/reload or restart required after installing the target package"
		}
		fmt.Fprintf(out, "Pi PID %d at %s loaded %s: %s.\n", r.PID, r.CWD, r.Version, status)
	}
	if s.CanUpdate && s.Installed != target && update.Stable(target) {
		fmt.Fprintf(out, "Exact-version repair (changes the pin; run only with your consent): PI_CODING_AGENT_DIR=%q pi install npm:%s@%s\n", s.Profile, update.Package, target)
	}
}
