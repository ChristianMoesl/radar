// Package update implements the authenticated macOS release contract. Apple
// signing is independent: a verified notifier may still need Gatekeeper approval.
package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const Repository = "ChristianMoesl/radar"
const Package = "@christianmoesl/pi-radar"
const StateEpoch = 1 // Bump for incompatible persisted-data changes: updates must reject them.
const MaxArchiveSize = 128 << 20
const MaxExpandedSize = 512 << 20

// Empty until the maintainer commits the initial public key. No TOFU, environment
// override, or unsigned fallback: development builds fail closed.
//
//go:embed keys.json
var keyFiles embed.FS

func TrustedKeys() map[string]string {
	data, _ := keyFiles.ReadFile("keys.json")
	var keys map[string]string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil
	}
	return keys
}

type Signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

type Artifact struct {
	File            string `json:"file"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	BinarySHA256    string `json:"binary_sha256"`
	NotifierVersion string `json:"notifier_version"`
	NotifierSHA256  string `json:"notifier_sha256"` // canonical bundle tree, not archive bytes
}

type Manifest struct {
	Schema     int                 `json:"schema"`
	Version    string              `json:"version"` // stable vX.Y.Z
	StateEpoch int                 `json:"state_epoch"`
	MinMacOS   string              `json:"min_macos"`
	PiVersion  string              `json:"pi_version"`
	MinPi      string              `json:"min_pi"`
	MinNode    int                 `json:"min_node"`
	Artifacts  map[string]Artifact `json:"artifacts"`
}

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Stable(v string) bool {
	return len(v) <= 64 && stableVersion.MatchString(strings.TrimPrefix(v, "v"))
}
func Compare(a, b string) int {
	aa, bb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var av, bv string
		if i < len(aa) {
			av = strings.TrimLeft(aa[i], "0")
		}
		if i < len(bb) {
			bv = strings.TrimLeft(bb[i], "0")
		}
		if len(av) != len(bv) {
			if len(av) > len(bv) {
				return 1
			}
			return -1
		}
		if c := strings.Compare(av, bv); c != 0 {
			return c
		}
	}
	return 0
}

func DecodeStrict(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func Verify(data, signature []byte, keys map[string]string) error {
	var s Signature
	if err := DecodeStrict(signature, &s); err != nil {
		return fmt.Errorf("release signature: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(keys[s.KeyID])
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("release signing key is not trusted")
	}
	sig, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil || !ed25519.Verify(key, data, sig) {
		return errors.New("invalid release signature")
	}
	return nil
}

func ParseManifest(data, signature []byte, keys map[string]string) (Manifest, error) {
	var m Manifest
	if err := Verify(data, signature, keys); err != nil {
		return m, err
	}
	if err := DecodeStrict(data, &m); err != nil {
		return m, err
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	return m, nil
}
func (m Manifest) Validate() error {
	if m.Schema != 1 || m.StateEpoch != StateEpoch {
		return errors.New("unsupported release schema or incompatible persisted data; use a maintainer-approved manual rollout")
	}
	if !strings.HasPrefix(m.Version, "v") || !Stable(m.Version) || m.PiVersion != strings.TrimPrefix(m.Version, "v") || !Stable(m.MinPi) || m.MinNode < 24 || m.MinNode > 100 {
		return errors.New("invalid release versions or prerequisites")
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+$`).MatchString(m.MinMacOS) {
		return errors.New("invalid minimum macOS")
	}
	if len(m.Artifacts) != 2 {
		return errors.New("release must provide both macOS architectures")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		a, ok := m.Artifacts[arch]
		if !ok || a.File != "radar_"+m.Version+"_darwin_"+arch+".tar.gz" || a.Size <= 0 || a.Size > MaxArchiveSize || !digestPattern.MatchString(a.SHA256) || !digestPattern.MatchString(a.BinarySHA256) || !Stable(a.NotifierVersion) || !digestPattern.MatchString(a.NotifierSHA256) {
			return fmt.Errorf("invalid %s artifact", arch)
		}
	}
	return nil
}

func FileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("expected regular file, not a link")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("expected regular file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TreeDigest includes every regular file's path, executable bit and content.
// Refuse links/special files rather than let bundle identity escape its root.
func TreeDigest(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular bundle entry %s", p)
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", errors.New("empty notifier bundle")
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		info, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		sum, err := FileDigest(p)
		if err != nil {
			return "", err
		}
		executable := 0
		if info.Mode()&0111 != 0 {
			executable = 1
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", filepath.ToSlash(rel), strconv.Itoa(executable), sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
