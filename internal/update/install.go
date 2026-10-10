package update

import (
	"context"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

const notifierPath = "libexec/radar/RadarNotifier.app"
const receiptPath = "libexec/radar/install.json"
const journalSchema = 2

const transactionPath = "libexec/radar/.upgrade"

type Receipt struct {
	Schema          int    `json:"schema"`
	Version         string `json:"version"`
	BinarySHA256    string `json:"binary_sha256"`
	NotifierVersion string `json:"notifier_version"`
	NotifierSHA256  string `json:"notifier_sha256"`
}
type Installation struct {
	Prefix         string
	BinarySHA256   string
	NotifierSHA256 string
	Receipt        *Receipt
}

func DefaultPrefix() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".local"), err
}

// Inspect never adopts arbitrary symlink targets, Homebrew, or development
// executables. Source builds at the normal path still need explicit adoption.
func Inspect(executable string) (Installation, error) {
	prefix, err := DefaultPrefix()
	if err != nil {
		return Installation{}, err
	}
	if runtime.GOOS != "darwin" {
		return Installation{}, errors.New("managed updates are macOS-only; use make install")
	}
	if filepath.Clean(executable) != filepath.Join(prefix, "bin/radar") {
		return Installation{}, fmt.Errorf("automatic updates support %s/bin/radar only; keep using your existing manual/package-manager installation", prefix)
	}
	return inspectPrefix(prefix)
}
func inspectPrefix(prefix string) (Installation, error) {
	i := Installation{Prefix: prefix}
	if err := checkInstallPaths(prefix); err != nil {
		return i, err
	}
	var err error
	i.BinarySHA256, err = FileDigest(filepath.Join(prefix, "bin/radar"))
	if err != nil {
		return i, err
	}
	app := filepath.Join(prefix, notifierPath)
	if _, err := os.Lstat(app); err == nil {
		i.NotifierSHA256, err = TreeDigest(app)
		if err != nil {
			return i, err
		}
	} else if !os.IsNotExist(err) {
		return i, err
	}
	data, err := os.ReadFile(filepath.Join(prefix, receiptPath))
	if os.IsNotExist(err) {
		return i, nil
	}
	if err != nil {
		return i, err
	}
	var r Receipt
	if err := DecodeStrict(data, &r); err != nil {
		return i, err
	}
	if r.Schema != 1 || !Stable(r.Version) || !digestPattern.MatchString(r.BinarySHA256) || !digestPattern.MatchString(r.NotifierSHA256) {
		return i, errors.New("unsupported installation receipt")
	}
	if r.BinarySHA256 != i.BinarySHA256 {
		return i, errors.New("installed binary differs from its managed receipt; restore it or explicitly return to manual installation")
	}
	i.Receipt = &r
	return i, nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(append(data, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// AcquireInstallation serializes downloads, recovery and activation. Do not
// delete lock files: inode replacement would allow simultaneous transactions.
func AcquireInstallation(prefix string) (func(), error) {
	if err := checkInstallPaths(prefix); err != nil {
		return nil, err
	}
	dir := filepath.Join(prefix, "libexec/radar")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "upgrade.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another Radar update is running")
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}

type Journal struct {
	Manuals          map[string]ManualChange `json:"manuals"`
	Schema           int                     `json:"schema"`
	Committed        bool                    `json:"committed"`
	Manifest         Manifest                `json:"manifest"`
	Arch             string                  `json:"arch"`
	PreviousBinary   string                  `json:"previous_binary"`
	PreviousNotifier string                  `json:"previous_notifier"`
	PreviousReceipt  *Receipt                `json:"previous_receipt"`
	ChangeNotifier   bool                    `json:"change_notifier"`
}

func ReadJournal(prefix string) (*Journal, error) {
	data, err := os.ReadFile(filepath.Join(prefix, transactionPath, "journal.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := DecodeStrict(data, &j); err != nil {
		return nil, err
	}
	if j.Schema != journalSchema {
		return nil, errors.New("unsupported update recovery journal; recover an unfinished update with the previous Radar version, or explicitly remove a completed recovery directory before updating")
	}
	if err := validateManuals(j.Manuals); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(j.PreviousBinary) || (j.PreviousNotifier != "" && !digestPattern.MatchString(j.PreviousNotifier)) {
		return nil, errors.New("invalid update recovery journal")
	}
	if err := j.Manifest.Validate(); err != nil {
		return nil, err
	}
	if _, ok := j.Manifest.Artifacts[j.Arch]; !ok {
		return nil, errors.New("invalid recovery architecture")
	}
	return &j, nil
}

type Staged struct {
	Prefix, Root string
	Journal      Journal
}

func Stage(ctx context.Context, c *Client, i Installation, m Manifest, arch string) (*Staged, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	a, ok := m.Artifacts[arch]
	if !ok {
		return nil, errors.New("unsupported architecture")
	}
	old, err := ReadJournal(i.Prefix)
	if err != nil {
		return nil, err
	}
	if old != nil && !old.Committed {
		return nil, errors.New("interrupted update needs recovery first")
	}
	base := filepath.Join(i.Prefix, transactionPath)
	// Never discard recovery data from an incomplete transaction.
	if err := os.RemoveAll(base); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	var disk unix.Statfs_t
	if err := unix.Statfs(base, &disk); err != nil {
		return nil, err
	}
	if uint64(disk.Bavail)*uint64(disk.Bsize) < uint64(2*MaxExpandedSize+a.Size) {
		return nil, errors.New("update requires at least 1 GiB of free staging/recovery space")
	}
	var binStat, stageStat unix.Stat_t
	if err := unix.Stat(filepath.Join(i.Prefix, "bin"), &binStat); err != nil {
		return nil, err
	}
	if err := unix.Stat(base, &stageStat); err != nil {
		return nil, err
	}
	if binStat.Dev != stageStat.Dev {
		return nil, errors.New("bin and libexec must be on the same filesystem")
	}
	archive := filepath.Join(base, "release.tar.gz")
	if err := c.Download(ctx, m, arch, archive); err != nil {
		return nil, err
	}
	root := strings.TrimSuffix(a.File, ".tar.gz")
	if err := Extract(archive, base, root); err != nil {
		return nil, err
	}
	staged := filepath.Join(base, root)
	for _, executable := range []string{"bin/radar", notifierPath + "/Contents/MacOS/radar-notifier"} {
		if err := verifyArchitecture(filepath.Join(staged, executable), arch); err != nil {
			return nil, err
		}
	}
	if sum, err := FileDigest(filepath.Join(staged, "bin/radar")); err != nil || sum != a.BinarySHA256 {
		return nil, errors.New("staged binary digest mismatch")
	}
	if sum, err := TreeDigest(filepath.Join(staged, notifierPath)); err != nil || sum != a.NotifierSHA256 {
		return nil, errors.New("staged notifier identity mismatch")
	}
	info, err := os.Stat(filepath.Join(staged, "bin/radar"))
	if err != nil || info.Mode()&0111 == 0 {
		return nil, errors.New("staged binary is not executable")
	}
	manuals, err := inspectManuals(i.Prefix, staged)
	if err != nil {
		return nil, err
	}
	return &Staged{Prefix: i.Prefix, Root: staged, Journal: Journal{Schema: journalSchema, Manuals: manuals, Manifest: m, Arch: arch, PreviousBinary: i.BinarySHA256, PreviousNotifier: i.NotifierSHA256, PreviousReceipt: i.Receipt, ChangeNotifier: i.NotifierSHA256 != a.NotifierSHA256}}, nil
}

// Hooks run under the caller's exclusive mutation gate. Health starts/checks the
// new daemon. On failure the prior files are restored before RestartPrevious.
type Hooks struct {
	Stop            func() error
	Health          func(binary, identity string) error
	Register        func(app string) error
	RestartPrevious func() error
}

func (s *Staged) Activate(h Hooks) error {
	if h.Health == nil {
		return errors.New("daemon health verification is required for activation")
	}
	base := filepath.Join(s.Prefix, transactionPath)
	binary := filepath.Join(s.Prefix, "bin/radar")
	app := filepath.Join(s.Prefix, notifierPath)
	current, err := inspectPrefix(s.Prefix)
	if err != nil {
		return err
	}
	if current.BinarySHA256 != s.Journal.PreviousBinary || current.NotifierSHA256 != s.Journal.PreviousNotifier {
		return errors.New("installation changed during download; review again")
	}
	if err := s.backupManuals(); err != nil {
		return err
	}
	if err := os.Link(binary, filepath.Join(base, "previous-radar")); err != nil {
		return err
	}
	if err := syncDir(base); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(base, "journal.json"), s.Journal); err != nil {
		return err
	}
	fail := func(cause error) error {
		if h.Stop != nil {
			if err := h.Stop(); err != nil {
				return errors.Join(cause, fmt.Errorf("daemon could not be stopped for rollback; preserve recovery data: %w", err))
			}
		}
		recovery := Recover(s.Prefix)
		var restart, registration error
		if recovery == nil && s.Journal.ChangeNotifier && s.Journal.PreviousNotifier != "" && h.Register != nil {
			registration = h.Register(app)
		}
		if recovery == nil && h.RestartPrevious != nil {
			restart = h.RestartPrevious()
		}
		return errors.Join(cause, recovery, registration, restart)
	}
	if h.Stop != nil {
		if err := h.Stop(); err != nil {
			return errors.Join(err, Recover(s.Prefix))
		}
	}
	if s.Journal.ChangeNotifier {
		if s.Journal.PreviousNotifier != "" {
			if err := os.Rename(app, filepath.Join(base, "previous.app")); err != nil {
				return fail(err)
			}
			if err := syncDir(filepath.Dir(app)); err != nil {
				return fail(err)
			}
			if err := syncDir(base); err != nil {
				return fail(err)
			}
		}
		if err := os.Rename(filepath.Join(s.Root, notifierPath), app); err != nil {
			return fail(err)
		}
		if err := syncDir(filepath.Dir(app)); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(filepath.Join(s.Root, "bin/radar"), binary); err != nil {
		return fail(err)
	}
	if err := syncDir(filepath.Dir(binary)); err != nil {
		return fail(err)
	}
	if err := s.activateManuals(); err != nil {
		return fail(err)
	}
	a := s.Journal.Manifest.Artifacts[s.Journal.Arch]
	if s.Journal.ChangeNotifier && h.Register != nil {
		if err := h.Register(app); err != nil {
			return fail(err)
		}
	}
	if err := h.Health(binary, s.Journal.Manifest.Version+"+"+a.BinarySHA256); err != nil {
		return fail(err)
	}
	r := Receipt{Schema: 1, Version: s.Journal.Manifest.Version, BinarySHA256: a.BinarySHA256, NotifierVersion: a.NotifierVersion, NotifierSHA256: a.NotifierSHA256}
	if err := writeJSON(filepath.Join(s.Prefix, receiptPath), r); err != nil {
		return fail(err)
	}
	s.Journal.Committed = true
	if err := writeJSON(filepath.Join(base, "journal.json"), s.Journal); err != nil {
		return fmt.Errorf("new daemon is running but durable commit was not confirmed; preserve recovery data: %w", err)
	}
	// Preserve the previous files and journal until the next update. No Pi or
	// persisted application data is part of this filesystem transaction.
	return nil
}

func Recover(prefix string) error {
	j, err := ReadJournal(prefix)
	if err != nil || j == nil {
		return err
	}
	if j.Committed {
		return nil
	}
	if err := checkManualInstallPaths(prefix); err != nil {
		return err
	}
	base := filepath.Join(prefix, transactionPath)
	binary := filepath.Join(prefix, "bin/radar")
	app := filepath.Join(prefix, notifierPath)
	previous := filepath.Join(base, "previous-radar")
	if sum, err := FileDigest(binary); err == nil && sum != j.PreviousBinary && sum != j.Manifest.Artifacts[j.Arch].BinarySHA256 {
		return errors.New("binary changed after interrupted update; refusing to overwrite it")
	}
	if sum, err := FileDigest(previous); err == nil {
		if sum != j.PreviousBinary {
			return errors.New("recovery binary was modified")
		}
		if err := os.Rename(previous, binary); err != nil {
			return err
		}
	} else if sum, err := FileDigest(binary); err != nil || sum != j.PreviousBinary {
		return errors.New("previous binary unavailable; keep recovery directory and reinstall manually")
	}
	if err := syncDir(filepath.Dir(binary)); err != nil {
		return err
	}
	if j.ChangeNotifier {
		previousApp := filepath.Join(base, "previous.app")
		_, backupErr := os.Lstat(previousApp)
		if backupErr == nil || j.PreviousNotifier == "" {
			if sum, err := TreeDigest(app); err == nil {
				if sum != j.Manifest.Artifacts[j.Arch].NotifierSHA256 {
					return errors.New("notifier changed after interrupted update; refusing to delete it")
				}
				if err := os.RemoveAll(app); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if j.PreviousNotifier != "" {
				if sum, err := TreeDigest(previousApp); err != nil || sum != j.PreviousNotifier {
					return errors.New("recovery notifier was modified")
				}
				if err := os.Rename(previousApp, app); err != nil {
					return err
				}
			}
		} else if sum, err := TreeDigest(app); err != nil || sum != j.PreviousNotifier {
			return errors.New("previous notifier unavailable; preserve recovery directory")
		}
		if err := syncDir(filepath.Dir(app)); err != nil {
			return err
		}
	}
	if err := recoverManuals(prefix, j.Manuals); err != nil {
		return err
	}
	if j.PreviousReceipt != nil {
		if err := writeJSON(filepath.Join(prefix, receiptPath), j.PreviousReceipt); err != nil {
			return err
		}
	} else {
		if err := os.Remove(filepath.Join(prefix, receiptPath)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := syncDir(filepath.Join(prefix, "libexec/radar")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(base, "journal.json")); err != nil {
		return err
	}
	return syncDir(base)
}

func verifyArchitecture(path, arch string) error {
	f, err := macho.Open(path)
	if err != nil {
		return fmt.Errorf("not a macOS executable: %w", err)
	}
	defer f.Close()
	cpu := macho.CpuArm64
	if arch == "amd64" {
		cpu = macho.CpuAmd64
	}
	if f.Type != macho.TypeExec || f.Cpu != cpu {
		return errors.New("staged executable architecture does not match the release")
	}
	return nil
}

type installationPath struct {
	relative            string
	required, directory bool
}

func checkInstallPaths(prefix string) error {
	if err := checkOwnedPaths(prefix, []installationPath{
		{"", true, true}, {"bin", true, true}, {"bin/radar", true, false},
		{"libexec", false, true}, {"libexec/radar", false, true},
	}); err != nil {
		return err
	}
	return checkManualInstallPaths(prefix)
}

func checkManualInstallPaths(prefix string) error {
	return checkOwnedPaths(prefix, []installationPath{
		{"share", false, true}, {"share/man", false, true},
		{"share/man/man1", false, true}, {"share/man/man5", false, true},
		{manualPaths[0], false, false}, {manualPaths[1], false, false},
	})
}

func checkOwnedPaths(prefix string, paths []installationPath) error {
	for _, entry := range paths {
		p := filepath.Join(prefix, entry.relative)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) && !entry.required {
			continue
		}
		if err != nil {
			return err
		}
		if (entry.directory && !info.IsDir()) || (!entry.directory && !info.Mode().IsRegular()) || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsupported writable/shared, symlink or non-regular installation path: %s", p)
		}
		var stat unix.Stat_t
		if err := unix.Lstat(p, &stat); err != nil {
			return err
		}
		if stat.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("installation is not owned by the current user: %s", p)
		}
	}
	return nil
}
