package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func fixtureManifest(v string) Manifest {
	m := Manifest{Schema: 1, Version: v, StateEpoch: StateEpoch, MinMacOS: "13.0", PiVersion: strings.TrimPrefix(v, "v"), MinPi: "0.85.1", MinNode: 24, Artifacts: map[string]Artifact{}}
	for _, arch := range []string{"amd64", "arm64"} {
		m.Artifacts[arch] = Artifact{File: "radar_" + v + "_darwin_" + arch + ".tar.gz", Size: 1, SHA256: hash([]byte("archive")), BinarySHA256: hash([]byte("new")), NotifierVersion: "1.0.0", NotifierSHA256: hash([]byte("bundle"))}
	}
	return m
}
func signed(t *testing.T, m Manifest) ([]byte, []byte, map[string]string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	sig, _ := json.Marshal(Signature{KeyID: "test", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))})
	return data, sig, map[string]string{"test": base64.StdEncoding.EncodeToString(pub)}
}
func TestEmbeddedReleaseTrustStore(t *testing.T) {
	keys := TrustedKeys()
	if len(keys) == 0 {
		t.Fatal("release builds must include a provisioned public trust root")
	}
	for id, encoded := range keys {
		if strings.TrimSpace(id) == "" {
			t.Fatal("release signing key ID must not be empty")
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			t.Fatalf("release key %q must be a base64-encoded raw Ed25519 public key", id)
		}
	}
}

func TestManifestAuthentication(t *testing.T) {
	m := fixtureManifest("v0.2.0")
	data, sig, keys := signed(t, m)
	if _, err := ParseManifest(data, sig, keys); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		data, sig []byte
		keys      map[string]string
	}{
		{"changed", append(append([]byte{}, data...), ' '), sig, keys}, {"unknown key", data, sig, map[string]string{}}, {"bad sig", data, []byte(`{"key_id":"test","signature":"bad"}`), keys}, {"extra JSON", data, append(sig, []byte(`{}`)...), keys},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseManifest(test.data, test.sig, test.keys); err == nil {
				t.Fatal("accepted invalid signature")
			}
		})
	}
}
func TestManifestValidation(t *testing.T) {
	for name, change := range map[string]func(*Manifest){"prerelease": func(m *Manifest) { m.Version = "v0.2.0-rc.1" }, "npm mismatch": func(m *Manifest) { m.PiVersion = "0.3.0" }, "data schema": func(m *Manifest) { m.StateEpoch++ }, "metadata schema": func(m *Manifest) { m.Schema++ }, "architecture": func(m *Manifest) { delete(m.Artifacts, "arm64") }, "path": func(m *Manifest) { a := m.Artifacts["arm64"]; a.File = "../escape"; m.Artifacts["arm64"] = a }, "size": func(m *Manifest) { a := m.Artifacts["arm64"]; a.Size = MaxArchiveSize + 1; m.Artifacts["arm64"] = a }} {
		t.Run(name, func(t *testing.T) {
			m := fixtureManifest("v0.2.0")
			change(&m)
			data, sig, keys := signed(t, m)
			if _, err := ParseManifest(data, sig, keys); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	for _, pair := range [][2]string{{"1.10.0", "1.9.9"}, {"2.0.0", "1.99.99"}, {"99999999999999999999.0.0", "2.0.0"}} {
		if Compare(pair[0], pair[1]) <= 0 {
			t.Fatal(pair)
		}
	}
	for _, v := range []string{"01.0.0", "1.0", "v1.2.3-beta", "1.2.3+build"} {
		if Stable(v) {
			t.Fatalf("accepted %s", v)
		}
	}
}
func makeArchive(t *testing.T, entries []*tar.Header, contents [][]byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "release.tar.gz")
	f, _ := os.Create(file)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(contents[i]))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(contents[i]) > 0 {
			if _, err := tw.Write(contents[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	f.Close()
	return file
}
func TestExtractRejectsUnsafeArchives(t *testing.T) {
	for name, headers := range map[string][]*tar.Header{
		"traversal": {{Name: "release/../escape", Typeflag: tar.TypeReg}}, "absolute": {{Name: "/escape", Typeflag: tar.TypeReg}}, "symlink": {{Name: "release/bin", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}, "hardlink": {{Name: "release/bin/radar", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}}, "special": {{Name: "release/bin/radar", Typeflag: tar.TypeFifo}}, "unknown file": {{Name: "release/arbitrary", Typeflag: tar.TypeReg}}, "duplicates": {{Name: "release/bin/radar", Typeflag: tar.TypeReg}, {Name: "release/bin/radar", Typeflag: tar.TypeReg}},
	} {
		t.Run(name, func(t *testing.T) {
			contents := make([][]byte, len(headers))
			if err := Extract(makeArchive(t, headers, contents), t.TempDir(), "release"); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	archive := makeArchive(t, []*tar.Header{{Name: "release/bin/radar", Typeflag: tar.TypeReg, Mode: 0755}}, [][]byte{[]byte("binary")})
	dest := t.TempDir()
	if err := Extract(archive, dest, "release"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dest, "release/bin/radar"))
	if string(data) != "binary" {
		t.Fatal("not extracted")
	}
}
func TestTreeIdentityIncludesModesAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "file")
	os.WriteFile(p, []byte("a"), 0644)
	first, _ := TreeDigest(root)
	os.Chmod(p, 0755)
	second, _ := TreeDigest(root)
	if first == second {
		t.Fatal("ignored executable bit")
	}
	os.Symlink(p, filepath.Join(root, "link"))
	if _, err := TreeDigest(root); err == nil {
		t.Fatal("followed symlink")
	}
}
func TestGoAndReleaseScriptTreeDigestsMatch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "Contents/MacOS"), 0755)
	os.WriteFile(filepath.Join(root, "Contents/MacOS/notifier"), []byte("fixture"), 0755)
	os.WriteFile(filepath.Join(root, "Contents/Info.plist"), []byte("plist"), 0644)
	want, err := TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	module, _ := filepath.Abs("../../scripts/release-metadata.mjs")
	js := fmt.Sprintf("import {treeDigest} from %q; console.log(treeDigest(%q))", "file://"+module, root)
	out, err := exec.Command("node", "--input-type=module", "-e", js).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("digest mismatch: %s != %s", out, want)
	}
}
func TestLatestSkipsUnapprovedNPMAndIncompleteNewerRelease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]string{"test": base64.StdEncoding.EncodeToString(pub)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases":
			var releases []map[string]any
			for _, v := range []string{"v0.4.0", "v0.3.0", "v0.2.0"} {
				var assets []releaseAsset
				for _, a := range fixtureManifest(v).Artifacts {
					assets = append(assets, releaseAsset{Name: a.File, Size: a.Size, State: "uploaded"})
				}
				releases = append(releases, map[string]any{"tag_name": v, "assets": assets})
			}
			releases = append(releases, map[string]any{"tag_name": "v9.0.0", "draft": true}, map[string]any{"tag_name": "v8.0.0-rc.1", "prerelease": true})
			json.NewEncoder(w).Encode(releases)
		case strings.HasPrefix(r.URL.Path, "/v0.4.0/"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/release.json") || strings.HasSuffix(r.URL.Path, "/release.json.sig"):
			v := strings.Split(r.URL.Path, "/")[1]
			data, _ := json.Marshal(fixtureManifest(v))
			if strings.HasSuffix(r.URL.Path, ".sig") {
				json.NewEncoder(w).Encode(Signature{"test", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))})
			} else {
				w.Write(data)
			}
		case strings.HasSuffix(r.URL.Path, "/0.2.0"):
			fmt.Fprint(w, `{"name":"@christianmoesl/pi-radar","version":"0.2.0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{HTTP: server.Client(), Keys: keys, API: server.URL, Downloads: server.URL, Registry: server.URL}
	m, err := c.Latest(context.Background(), "v0.1.0")
	if err != nil || m == nil || m.Version != "v0.2.0" {
		t.Fatalf("%+v %v", m, err)
	}
	c.Keys = nil
	if _, err := c.Latest(context.Background(), "dev"); err == nil {
		t.Fatal("accepted missing trust root")
	}
}
func TestDownloadRejectsTamperingAndExcessData(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "bad bytes") }))
	defer s.Close()
	c := &Client{HTTP: s.Client(), Downloads: s.URL}
	m := fixtureManifest("v0.2.0")
	if err := c.Download(context.Background(), m, "arm64", filepath.Join(t.TempDir(), "archive")); err == nil {
		t.Fatal("accepted bad archive")
	}
}
func machOBinary(arch string) []byte {
	var b bytes.Buffer
	cpu := uint32(0x0100000c)
	if arch == "amd64" {
		cpu = 0x01000007
	}
	for _, word := range []uint32{0xfeedfacf, cpu, 0, 2, 0, 0, 0, 0} {
		_ = binary.Write(&b, binary.LittleEndian, word)
	}
	return b.Bytes()
}
func TestVerifyArchitecture(t *testing.T) {
	file := filepath.Join(t.TempDir(), "radar")
	os.WriteFile(file, machOBinary("arm64"), 0755)
	if err := verifyArchitecture(file, "arm64"); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchitecture(file, "amd64"); err == nil {
		t.Fatal("accepted wrong architecture")
	}
	os.WriteFile(file, []byte("#!/bin/sh\n"), 0755)
	if err := verifyArchitecture(file, "arm64"); err == nil {
		t.Fatal("accepted non-Mach-O")
	}
}

func TestEligibilityRequiresAllUploadedArchitectures(t *testing.T) {
	m := fixtureManifest("v0.2.0")
	if err := checkAssets(m, nil); err == nil {
		t.Fatal("missing assets eligible")
	}
	var assets []releaseAsset
	for _, a := range m.Artifacts {
		assets = append(assets, releaseAsset{Name: a.File, Size: a.Size, State: "uploaded"})
	}
	if err := checkAssets(m, assets); err != nil {
		t.Fatal(err)
	}
	assets[0].State = "new"
	if err := checkAssets(m, assets); err == nil {
		t.Fatal("unfinished asset eligible")
	}
	assets[0].State = "uploaded"
	assets[0].Size++
	if err := checkAssets(m, assets); err == nil {
		t.Fatal("wrong asset size eligible")
	}
}

func TestNodeReleaseSignatureIsAcceptedByGo(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	script, _ := os.ReadFile("../../scripts/release-metadata.mjs")
	put(t, filepath.Join(root, "scripts/release-metadata.mjs"), string(script), 0644)
	put(t, filepath.Join(root, "package.json"), `{"version":"0.2.0","engines":{"node":">=24"},"peerDependencies":{"@earendil-works/pi-coding-agent":">=0.85.1"}}`, 0644)
	put(t, filepath.Join(root, "internal/update/manifest.go"), "const StateEpoch = 1\n", 0644)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]string{"test": base64.StdEncoding.EncodeToString(pub)}
	keyData, _ := json.Marshal(keys)
	put(t, filepath.Join(root, "internal/update/keys.json"), string(keyData), 0644)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	dist := filepath.Join(root, "dist")
	m := fixtureManifest("v0.2.0")
	for _, a := range m.Artifacts {
		data, _ := json.Marshal(a)
		put(t, filepath.Join(dist, a.File+".json"), string(data), 0644)
	}
	cmd := exec.Command("node", filepath.Join(root, "scripts/release-metadata.mjs"), "release", dist, "v0.2.0")
	cmd.Env = append(os.Environ(), "RADAR_RELEASE_KEY_ID=test", "RADAR_RELEASE_SIGNING_KEY="+string(pemKey))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("metadata script failed: %v %s", err, output)
	}
	data, _ := os.ReadFile(filepath.Join(dist, "release.json"))
	sig, _ := os.ReadFile(filepath.Join(dist, "release.json.sig"))
	parsed, err := ParseManifest(data, sig, keys)
	if err != nil || parsed.Version != "v0.2.0" {
		t.Fatalf("%+v %v", parsed, err)
	}
}
