// Command homebrew prepares Formula/radar.rb for ChristianMoesl/homebrew-tap.
// It never writes to a tap or publishes anything. Redirect stdout to a new file
// and review it before updating the tap after npm publication.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"radar/internal/update"
)

//go:embed radar.rb.tmpl
var formulaTemplate string

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/homebrew vX.Y.Z > radar.rb")
		os.Exit(2)
	}
	// Run from the release's source checkout, not a newer generator against an
	// older CLI that lacks package-manager setup support.
	if err := checkSourceVersion(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "Homebrew formula:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := generate(ctx, update.NewClient(), os.Args[1], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Homebrew formula:", err)
		os.Exit(1)
	}
}

func checkSourceVersion(tag string) error {
	data, err := os.ReadFile("package.json")
	if err != nil {
		return fmt.Errorf("run from the release's Radar source root: %w", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || !update.Stable(pkg.Version) || tag != "v"+pkg.Version {
		return fmt.Errorf("requested tag must match package.json; use the requested release's source checkout")
	}
	return nil
}

func generate(ctx context.Context, client *update.Client, tag string, out io.Writer) error {
	if !strings.HasPrefix(tag, "v") || !update.Stable(tag) {
		return fmt.Errorf("expected a stable vX.Y.Z release")
	}
	// Reuse the updater's signed-manifest, state-epoch, public-asset and npm
	// readiness checks. Never silently generate an older version when the
	// requested release is still staged or incomplete.
	manifest, err := client.Latest(ctx, "")
	if err != nil {
		return err
	}
	if manifest == nil || manifest.Version != tag {
		return fmt.Errorf("%s is not the latest coordinated public release; finish release/npm publication first", tag)
	}
	formula, err := renderFormula(*manifest)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "radar-homebrew-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Check actual bytes for both architectures, not only GitHub asset metadata.
	for _, arch := range []string{"arm64", "amd64"} {
		if err := client.Download(ctx, *manifest, arch, filepath.Join(dir, arch+".tar.gz")); err != nil {
			return fmt.Errorf("verify %s archive: %w", arch, err)
		}
	}
	_, err = out.Write(formula)
	return err
}

func renderFormula(manifest update.Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	// A new macOS baseline needs an explicit Homebrew dependency change too.
	if manifest.MinMacOS != "13.0" {
		return nil, fmt.Errorf("unsupported Homebrew macOS baseline %s; update the formula template", manifest.MinMacOS)
	}
	// Display the plain numeric minimum in the user-managed Pi prerequisite
	// instructions; the manifest permits a leading v.
	manifest.MinPi = strings.TrimPrefix(manifest.MinPi, "v")
	tmpl, err := template.New("radar.rb").Parse(formulaTemplate)
	if err != nil {
		return nil, err
	}
	var result bytes.Buffer
	if err := tmpl.Execute(&result, manifest); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}
