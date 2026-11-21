// Package agentruntime materializes the SDK embedded in this exact Radar binary.
// Installers and atomic binary updates therefore cannot mix CLI/SDK revisions.
package agentruntime

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

//go:embed payload
var payload embed.FS

// Ensure returns an immutable, content-addressed runner. Old runtime directories
// are deliberately retained: a running Node process may still import from them.
// No registration, checkpoint, credentials or user project is changed here.
func Ensure(agentRoot string) (string, error) {
	data, err := payload.ReadFile("payload/runtime.zip")
	if err != nil {
		return "", errors.New("agent runtime not bundled in this development binary; build Radar with make build")
	}
	return materialize(agentRoot, data)
}

func materialize(agentRoot string, data []byte) (string, error) {
	if err := os.MkdirAll(agentRoot, 0700); err != nil {
		return "", err
	}
	if err := safeDirectory(agentRoot, false); err != nil {
		return "", err
	}
	cache := filepath.Join(agentRoot, ".runtime")
	if err := os.Mkdir(cache, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := safeDirectory(cache, true); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%x", sha256.Sum256(data))
	final := filepath.Join(cache, id)
	runner := filepath.Join(final, "dist", "src", "runner.js")
	ready := func() error {
		if err := safeDirectory(final, true); err != nil {
			return err
		}
		marker, err := os.ReadFile(filepath.Join(final, ".complete"))
		if err != nil || string(marker) != id {
			return fmt.Errorf("incomplete supplied agent runtime %s; preserve agent state", final)
		}
		info, err := os.Lstat(runner)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("supplied agent runner missing at %s", runner)
		}
		return nil
	}
	if _, err := os.Lstat(final); err == nil {
		return runner, ready()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stage, err := os.MkdirTemp(cache, ".prepare-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := extract(stage, data); err != nil {
		return "", fmt.Errorf("unpack supplied agent runtime: %w", err)
	}
	if info, err := os.Lstat(filepath.Join(stage, "dist", "src", "runner.js")); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("supplied agent runtime has no runner")
	}
	if err := os.WriteFile(filepath.Join(stage, ".complete"), []byte(id), 0600); err != nil {
		return "", err
	}
	// A competing daemon may have prepared the same content. Never replace an
	// existing tree; verify the winner before returning it.
	if err := os.Rename(stage, final); err != nil {
		if check := ready(); check != nil {
			return "", fmt.Errorf("activate supplied runtime: %w (%v)", err, check)
		}
	}
	return runner, ready()
}

func safeDirectory(dir string, private bool) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	mask := os.FileMode(0022)
	if private {
		mask = 0077
	}
	if !info.IsDir() || info.Mode()&mask != 0 {
		return fmt.Errorf("supplied agent runtime requires a non-symlink, non-shared directory: %s", dir)
	}
	return nil
}

func extract(dir string, data []byte) error {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	links := map[string]string{}
	seen := map[string]bool{}
	directories := map[string]bool{".": true}
	mkdir := func(name string) error {
		if directories[name] {
			return nil
		}
		if err := root.MkdirAll(name, 0700); err != nil {
			return err
		}
		directories[name] = true
		return nil
	}
	var total uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !filepath.IsLocal(name) || path.Clean(name) != name || strings.Contains(name, "\\") || seen[name] || name == ".complete" {
			return fmt.Errorf("invalid runtime member %q", f.Name)
		}
		seen[name] = true
		total += f.UncompressedSize64
		if total > 512<<20 || f.UncompressedSize64 > 128<<20 {
			return errors.New("supplied agent runtime exceeds size limit")
		}
		if f.FileInfo().IsDir() {
			if err := mkdir(name); err != nil {
				return err
			}
			continue
		}
		if err := mkdir(path.Dir(name)); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			target, readErr := io.ReadAll(io.LimitReader(r, 4097))
			r.Close()
			if readErr != nil || len(target) > 4096 || !filepath.IsLocal(path.Join(path.Dir(name), string(target))) || path.IsAbs(string(target)) || strings.Contains(string(target), "\\") {
				return fmt.Errorf("unsafe runtime symlink %q", name)
			}
			links[name] = string(target)
			continue
		}
		if !f.Mode().IsRegular() {
			r.Close()
			return fmt.Errorf("unsupported runtime member %q", name)
		}
		mode := os.FileMode(0600) | f.Mode().Perm()&0100
		w, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			r.Close()
			return err
		}
		_, copyErr := io.Copy(w, r)
		closeErr := w.Close()
		r.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
	}
	// Files are all written before links: no archive member can write through a
	// link, even to another directory inside the cache. os.Root confines access.
	for name, target := range links {
		if err := root.Symlink(target, name); err != nil {
			return err
		}
	}
	for name := range links {
		if _, err := root.Stat(name); err != nil {
			return fmt.Errorf("unresolvable runtime link %s: %w", name, err)
		}
	}
	return nil
}
