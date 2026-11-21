package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundleIsDeterministicAndOmitsInstallMetadata(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "sdk")
	for file, content := range map[string]string{
		"dist/src/runner.js": "runner", "node_modules/pkg/index.js": "dependency",
		"node_modules/.modules.yaml": "temporary path and time",
		"node_modules/.bin/tool":     "temporary path", "node_modules/.pnpm/lock.yaml": "build metadata",
	} {
		p := filepath.Join(source, file)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(dir, "runtime.zip")
	if err := bundle(source, output); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(output)
	if err := os.Chtimes(filepath.Join(source, "dist/src/runner.js"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := bundle(source, output); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(output)
	if !bytes.Equal(first, second) {
		t.Fatal("payload depends on file timestamps")
	}
	z, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == "node_modules/.modules.yaml" || f.Name == "node_modules/.bin/tool" || f.Name == "node_modules/.pnpm/lock.yaml" {
			t.Fatalf("install metadata included: %s", f.Name)
		}
	}
}

func TestRejectNonportableDeployment(t *testing.T) {
	for _, kind := range []string{"native", "external-link"} {
		t.Run(kind, func(t *testing.T) {
			dir, source := t.TempDir(), t.TempDir()
			if kind == "native" {
				if err := os.WriteFile(filepath.Join(source, "addon.node"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(dir, filepath.Join(source, "escaped")); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "runtime.zip")
			if err := bundle(source, output); err == nil {
				t.Fatal("nonportable deployment accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("failed bundle replaced output")
			}
		})
	}
}
