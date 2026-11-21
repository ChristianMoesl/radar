// agentbundle creates a deterministic, portable SDK payload from pnpm deploy.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func bundle(source, output string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".runtime-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	z := zip.NewWriter(f)
	err = filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || file == source {
			return err
		}
		name, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		// pnpm's install metadata and command shims contain timestamps/absolute
		// build paths. Node imports need neither; runners use node + entrypoint.
		if entry.IsDir() && entry.Name() == ".bin" {
			return filepath.SkipDir
		}
		if entry.Name() == ".modules.yaml" || entry.Name() == ".pnpm-workspace-state-v1.json" || filepath.ToSlash(name) == "node_modules/.pnpm/lock.yaml" {
			return nil
		}
		if strings.HasSuffix(name, ".node") {
			return fmt.Errorf("native Node addon %s requires target-specific packaging", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		h := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Deflate}
		h.SetMode(0600 | info.Mode().Perm()&0100)
		switch {
		case entry.IsDir():
			h.Name += "/"
			h.SetMode(os.ModeDir | 0700)
			_, err := z.CreateHeader(h)
			return err
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(file)
			if err != nil {
				return err
			}
			real, err := filepath.EvalSymlinks(file)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(source, real)
			if err != nil || filepath.IsAbs(target) || !filepath.IsLocal(relative) {
				return fmt.Errorf("runtime symlink escapes deployment: %s", name)
			}
			h.SetMode(os.ModeSymlink | 0700)
			w, err := z.CreateHeader(h)
			if err == nil {
				_, err = io.WriteString(w, filepath.ToSlash(target))
			}
			return err
		case info.Mode().IsRegular():
			r, err := os.Open(file)
			if err != nil {
				return err
			}
			defer r.Close()
			w, err := z.CreateHeader(h)
			if err == nil {
				_, err = io.Copy(w, r)
			}
			return err
		default:
			return fmt.Errorf("unsupported runtime file %s", name)
		}
	})
	if closeErr := z.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), output)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: agentbundle <deployment-directory> <runtime.zip>")
		os.Exit(2)
	}
	if err := bundle(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
