package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type releaseAsset struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	State string `json:"state"`
}

type Client struct {
	HTTP                     *http.Client
	Keys                     map[string]string
	API, Downloads, Registry string
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" {
			return errors.New("unsafe release redirect")
		}
		return nil
	}}, Keys: TrustedKeys(), API: "https://api.github.com/repos/" + Repository, Downloads: "https://github.com/" + Repository + "/releases/download", Registry: "https://registry.npmjs.org"}
}
func (c *Client) get(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Radar-release-updater")
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", req.URL.Host, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release response exceeds size limit")
	}
	return data, nil
}
func (c *Client) ReadManifest(ctx context.Context, tag string) (Manifest, error) {
	if !Stable(tag) || !strings.HasPrefix(tag, "v") {
		return Manifest{}, errors.New("not a stable release")
	}
	base := c.Downloads + "/" + tag
	data, err := c.get(ctx, base+"/release.json", 1<<20)
	if err != nil {
		return Manifest{}, err
	}
	sig, err := c.get(ctx, base+"/release.json.sig", 4096)
	if err != nil {
		return Manifest{}, err
	}
	m, err := ParseManifest(data, sig, c.Keys)
	if err != nil {
		return m, err
	}
	if m.Version != tag {
		return m, errors.New("release tag does not match signed manifest")
	}
	return m, nil
}
func (c *Client) NPMReady(ctx context.Context, m Manifest) error {
	data, err := c.get(ctx, c.Registry+"/"+url.PathEscape(Package)+"/"+m.PiVersion, 1<<20)
	if err != nil {
		return fmt.Errorf("matching Pi package is not public: %w", err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Name != Package || pkg.Version != m.PiVersion {
		return errors.New("npm returned a different package/version")
	}
	return nil
}
func (c *Client) Latest(ctx context.Context, current string) (*Manifest, error) {
	if len(c.Keys) == 0 {
		return nil, errors.New("release signing is not configured in this build; see docs/releases.md")
	}
	var tags []string
	assets := map[string][]releaseAsset{}
	for page := 1; page <= 5; page++ {
		data, err := c.get(ctx, fmt.Sprintf("%s/releases?per_page=100&page=%d", c.API, page), 8<<20)
		if err != nil {
			return nil, err
		}
		var releases []struct {
			Tag        string         `json:"tag_name"`
			Draft      bool           `json:"draft"`
			Prerelease bool           `json:"prerelease"`
			Assets     []releaseAsset `json:"assets"`
		}
		if err := json.Unmarshal(data, &releases); err != nil {
			return nil, err
		}
		for _, r := range releases {
			if !r.Draft && !r.Prerelease && strings.HasPrefix(r.Tag, "v") && Stable(r.Tag) && (!Stable(current) || Compare(r.Tag, current) > 0) {
				tags = append(tags, r.Tag)
				assets[r.Tag] = r.Assets
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	sort.Slice(tags, func(i, j int) bool { return Compare(tags[i], tags[j]) > 0 })
	var skipped []string
	for _, tag := range tags {
		m, err := c.ReadManifest(ctx, tag)
		if err == nil {
			err = checkAssets(m, assets[tag])
		}
		if err == nil {
			err = c.NPMReady(ctx, m)
		}
		if err == nil {
			return &m, nil
		}
		skipped = append(skipped, tag+": "+err.Error())
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if len(skipped) > 0 {
		return nil, fmt.Errorf("no coordinated release is ready (%s)", strings.Join(skipped, "; "))
	}
	return nil, nil
}

func (c *Client) Download(ctx context.Context, m Manifest, arch, destination string) error {
	a, ok := m.Artifacts[arch]
	if !ok {
		return errors.New("unsupported architecture")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Downloads+"/"+m.Version+"/"+a.File, nil)
	if err != nil {
		return err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("artifact download HTTP %d", res.StatusCode)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, a.Size+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("artifact size/hash verification failed")
	}
	return nil
}

// Extract deliberately supports only the files produced by make dist. Links,
// devices, duplicate paths, noncanonical paths and zip-bomb style archives fail.
func Extract(archive, destination, root string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var size int64
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > 2000 {
			return errors.New("too many archive entries")
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.Clean(name) != name || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") || !(name == root || strings.HasPrefix(name, root+"/")) || seen[name] {
			return errors.New("unsafe or duplicate archive path")
		}
		seen[name] = true
		rel := strings.TrimPrefix(name, root+"/")
		if h.Typeflag != tar.TypeDir && !allowedFile(rel) {
			return fmt.Errorf("unexpected release file %s", rel)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || h.Size > MaxExpandedSize-size {
				return errors.New("expanded archive exceeds size limit")
			}
			size += h.Size
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if h.Mode&0111 != 0 {
				mode = 0755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, tr, h.Size)
			syncErr := out.Sync()
			closeErr := out.Close()
			if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
				return err
			}
		default:
			return errors.New("links and special files are not allowed in release archives")
		}
	}
	// Consume and validate the gzip checksum, including archives ending at tar EOF.
	_, err = io.Copy(io.Discard, io.LimitReader(gz, 1024))
	return err
}
func allowedFile(p string) bool {
	switch p {
	case "bin/radar", "README.md", "LICENSE", "install.sh", "install-agent-instructions.sh", "install-notifier.sh", "share/radar/AGENTS.md", "share/man/man1/radar.1", "share/man/man5/radar-config.5":
		return true
	}
	return strings.HasPrefix(p, "libexec/radar/RadarNotifier.app/Contents/")
}

func checkAssets(m Manifest, assets []releaseAsset) error {
	for _, a := range m.Artifacts {
		count := 0
		for _, asset := range assets {
			if asset.Name == a.File && asset.Size == a.Size && asset.State == "uploaded" {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("release artifact %s is not publicly uploaded with the expected size", a.File)
		}
	}
	return nil
}
