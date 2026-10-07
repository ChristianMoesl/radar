package update

import (
	"archive/tar"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func stagedFixture(t *testing.T, changeNotifier bool) (*Staged, Installation) {
	t.Helper()
	prefix := t.TempDir()
	put(t, filepath.Join(prefix, "bin/radar"), "old binary", 0755)
	put(t, filepath.Join(prefix, notifierPath, "Contents/MacOS/radar-notifier"), "old notifier", 0755)
	i, err := inspectPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(prefix, transactionPath, "release")
	put(t, filepath.Join(root, "bin/radar"), "new binary", 0755)
	content := "old notifier"
	if changeNotifier {
		content = "new notifier"
	}
	put(t, filepath.Join(root, notifierPath, "Contents/MacOS/radar-notifier"), content, 0755)
	m := fixtureManifest("v0.2.0")
	a := m.Artifacts["arm64"]
	a.BinarySHA256, _ = FileDigest(filepath.Join(root, "bin/radar"))
	a.NotifierSHA256, _ = TreeDigest(filepath.Join(root, notifierPath))
	m.Artifacts["arm64"] = a
	s := &Staged{Prefix: prefix, Root: root, Journal: Journal{Schema: 1, Manifest: m, Arch: "arm64", PreviousBinary: i.BinarySHA256, PreviousNotifier: i.NotifierSHA256, ChangeNotifier: changeNotifier}}
	return s, i
}
func TestActivationLeavesUnchangedNotifierUntouched(t *testing.T) {
	s, _ := stagedFixture(t, false)
	app := filepath.Join(s.Prefix, notifierPath)
	before, _ := os.Stat(app)
	health := false
	err := s.Activate(Hooks{Stop: func() error { return nil }, Health: func(binary, identity string) error {
		health = true
		if !strings.HasPrefix(identity, "v0.2.0+") {
			t.Fatal(identity)
		}
		return nil
	}, Register: func(string) error { t.Fatal("registered unchanged app"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !health {
		t.Fatal("no health check")
	}
	after, _ := os.Stat(app)
	if !os.SameFile(before, after) {
		t.Fatal("replaced unchanged app")
	}
	j, err := ReadJournal(s.Prefix)
	if err != nil || j == nil || !j.Committed {
		t.Fatalf("journal %v %v", j, err)
	}
	installed, err := inspectPrefix(s.Prefix)
	if err != nil || installed.Receipt == nil || installed.Receipt.Version != "v0.2.0" {
		t.Fatalf("receipt %v %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(s.Prefix, transactionPath, "previous-radar")); err != nil {
		t.Fatal("did not retain previous binary")
	}
}
func TestFailedHealthStopsNewDaemonThenRollsBack(t *testing.T) {
	s, i := stagedFixture(t, true)
	oldApp, _ := os.Stat(filepath.Join(s.Prefix, notifierPath))
	var steps []string
	err := s.Activate(Hooks{Stop: func() error { steps = append(steps, "stop"); return nil }, Health: func(string, string) error { steps = append(steps, "health"); return errors.New("unhealthy") }, Register: func(string) error { steps = append(steps, "register previous"); return nil }, RestartPrevious: func() error { steps = append(steps, "restart previous"); return nil }})
	if err == nil {
		t.Fatal("ignored failure")
	}
	if strings.Join(steps, ",") != "stop,register previous,health,stop,register previous,restart previous" {
		t.Fatal(steps)
	}
	restored, err := inspectPrefix(s.Prefix)
	if err != nil || restored.BinarySHA256 != i.BinarySHA256 || restored.NotifierSHA256 != i.NotifierSHA256 || restored.Receipt != nil {
		t.Fatalf("not restored: %+v %v", restored, err)
	}
	after, _ := os.Stat(filepath.Join(s.Prefix, notifierPath))
	if !os.SameFile(oldApp, after) {
		t.Fatal("did not preserve original app inode")
	}
	if err := Recover(s.Prefix); err != nil {
		t.Fatal("recovery not idempotent", err)
	}
}
func TestCrashRecoveryAtEveryActivationBoundary(t *testing.T) {
	for _, phase := range []string{"prepared", "old app moved", "new app installed", "binary activated", "receipt written"} {
		t.Run(phase, func(t *testing.T) {
			s, i := stagedFixture(t, true)
			base := filepath.Join(s.Prefix, transactionPath)
			binary := filepath.Join(s.Prefix, "bin/radar")
			app := filepath.Join(s.Prefix, notifierPath)
			if err := os.Link(binary, filepath.Join(base, "previous-radar")); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(base, "journal.json"), s.Journal); err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				if err := os.Rename(app, filepath.Join(base, "previous.app")); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "new app installed" || phase == "binary activated" || phase == "receipt written" {
				if err := os.Rename(filepath.Join(s.Root, notifierPath), app); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "binary activated" || phase == "receipt written" {
				if err := os.Rename(filepath.Join(s.Root, "bin/radar"), binary); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "receipt written" {
				if err := writeJSON(filepath.Join(s.Prefix, receiptPath), Receipt{Schema: 1, Version: "v0.2.0"}); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := Recover(s.Prefix); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := inspectPrefix(s.Prefix)
			if err != nil || restored.BinarySHA256 != i.BinarySHA256 || restored.NotifierSHA256 != i.NotifierSHA256 {
				t.Fatalf("%+v %v", restored, err)
			}
		})
	}
}
func TestMissingHelperRollback(t *testing.T) {
	s, _ := stagedFixture(t, true)
	os.RemoveAll(filepath.Join(s.Prefix, notifierPath))
	s.Journal.PreviousNotifier = ""
	if err := s.Activate(Hooks{Health: func(string, string) error { return errors.New("fail") }}); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(s.Prefix, notifierPath)); !os.IsNotExist(err) {
		t.Fatal("new helper retained after rollback")
	}
}
func TestInstallAndRecoveryRefuseExternalChanges(t *testing.T) {
	s, _ := stagedFixture(t, true)
	put(t, filepath.Join(s.Prefix, "bin/radar"), "external build", 0755)
	if err := s.Activate(Hooks{Health: func(string, string) error { return nil }}); err == nil {
		t.Fatal("overwrote changed binary")
	}
	s, _ = stagedFixture(t, true)
	base := filepath.Join(s.Prefix, transactionPath)
	os.Link(filepath.Join(s.Prefix, "bin/radar"), filepath.Join(base, "previous-radar"))
	writeJSON(filepath.Join(base, "journal.json"), s.Journal)
	// Replace rather than write in place: retain the hardlinked original backup.
	put(t, filepath.Join(base, "external"), "external binary", 0755)
	os.Rename(filepath.Join(base, "external"), filepath.Join(s.Prefix, "bin/radar"))
	if err := Recover(s.Prefix); err == nil {
		t.Fatal("overwrote external binary during recovery")
	}
}
func TestInstallationOwnershipAndReceipt(t *testing.T) {
	s, _ := stagedFixture(t, false)
	path := filepath.Join(s.Prefix, "bin/radar")
	os.Rename(path, path+"-dev")
	os.Symlink(path+"-dev", path)
	if _, err := inspectPrefix(s.Prefix); err == nil {
		t.Fatal("accepted symlink target")
	}
	os.Remove(path)
	os.Rename(path+"-dev", path)
	os.Chmod(filepath.Dir(path), 0777)
	if _, err := inspectPrefix(s.Prefix); err == nil {
		t.Fatal("accepted shared-writable directory")
	}
	os.Chmod(filepath.Dir(path), 0755)
	put(t, filepath.Join(s.Prefix, receiptPath), `{"schema":99}`, 0600)
	if _, err := inspectPrefix(s.Prefix); err == nil {
		t.Fatal("accepted bad receipt")
	}
}
func TestUpdateLockAndPendingJournalProtectRecovery(t *testing.T) {
	s, i := stagedFixture(t, true)
	unlock, err := AcquireInstallation(s.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireInstallation(s.Prefix); err == nil {
		release()
		t.Fatal("allowed concurrent update")
	}
	unlock()
	writeJSON(filepath.Join(s.Prefix, transactionPath, "journal.json"), s.Journal)
	if _, err := Stage(context.Background(), nil, i, s.Journal.Manifest, "arm64"); err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("did not stop before download: %v", err)
	}
}

func TestStageAuthenticatedArchiveBeforeActivation(t *testing.T) {
	prefix := t.TempDir()
	put(t, filepath.Join(prefix, "bin/radar"), "old binary", 0755)
	i, err := inspectPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	name := "radar_v0.2.0_darwin_arm64"
	binary := machOBinary("arm64")
	archive := makeArchive(t, []*tar.Header{{Name: name + "/bin/radar", Typeflag: tar.TypeReg, Mode: 0755}, {Name: name + "/" + notifierPath + "/Contents/MacOS/radar-notifier", Typeflag: tar.TypeReg, Mode: 0755}}, [][]byte{binary, binary})
	bytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	app := t.TempDir()
	put(t, filepath.Join(app, "Contents/MacOS/radar-notifier"), string(binary), 0755)
	tree, _ := TreeDigest(app)
	m := fixtureManifest("v0.2.0")
	a := m.Artifacts["arm64"]
	a.Size = int64(len(bytes))
	a.SHA256 = hash(bytes)
	a.BinarySHA256 = hash(binary)
	a.NotifierSHA256 = tree
	m.Artifacts["arm64"] = a
	data, sig, keys := signed(t, m)
	m, err = ParseManifest(data, sig, keys)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(bytes) }))
	defer server.Close()
	c := &Client{HTTP: server.Client(), Downloads: server.URL}
	staged, err := Stage(context.Background(), c, i, m, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := FileDigest(filepath.Join(prefix, "bin/radar"))
	if current != i.BinarySHA256 {
		t.Fatal("changed installation before activation")
	}
	if err := staged.Activate(Hooks{Health: func(string, string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	installed, err := inspectPrefix(prefix)
	if err != nil || installed.Receipt == nil || installed.BinarySHA256 != a.BinarySHA256 {
		t.Fatalf("%+v %v", installed, err)
	}
}
