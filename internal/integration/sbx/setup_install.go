package sbx

import (
	"context"
	"fmt"
	"time"

	"radar/internal/setup"
)

// Keep the Linux download pinned and authenticated before running Docker's
// installer. macOS uses Docker's official Homebrew cask and its checksum.
const linuxSBXInstall = `set -euo pipefail
sbx_prefix=$1
case "$(uname -m)" in
  x86_64) sbx_arch=amd64; sbx_sha=7acd87b5795119b24efbabb7641b714690683f044924c710ac9b4babe74cd546 ;;
  aarch64|arm64) sbx_arch=arm64; sbx_sha=2bccab5812d5728e96ff860389ff4ed150c0a81dc7d188d8b95136c6de8b64bd ;;
  *) echo 'Docker Sandboxes requires Linux amd64 or arm64.' >&2; exit 1 ;;
esac
if [ -e "$sbx_prefix/bin/sbx" ] || [ -L "$sbx_prefix/bin/sbx" ]; then
  echo 'An SBX installation already exists at the target; repair its PATH instead of replacing it.' >&2
  exit 1
fi
sbx_stage=$(mktemp -d)
trap 'rm -rf -- "$sbx_stage"' EXIT
curl --fail --location --proto '=https' --tlsv1.2 --output "$sbx_stage/release.tar.gz" \
  "https://github.com/docker/sbx-releases/releases/download/v0.50.1/DockerSandboxes-linux-$sbx_arch.tar.gz"
printf '%s  %s\n' "$sbx_sha" "$sbx_stage/release.tar.gz" | sha256sum --check --status
tar -xzf "$sbx_stage/release.tar.gz" -C "$sbx_stage"
if [ "$(id -u)" = 0 ]; then
  env PREFIX="$sbx_prefix" bash "$sbx_stage/docker-sbx/install.sh"
else
  sudo env PREFIX="$sbx_prefix" bash "$sbx_stage/docker-sbx/install.sh"
fi
`

func (w *setupWizard) installSandboxCLI(ctx context.Context) error {
	var install setup.Command
	switch w.system.Platform() {
	case "darwin":
		if !w.system.LookPath("brew") {
			return fmt.Errorf("install Docker Sandboxes with `brew install docker/tap/sbx`, or follow https://docs.docker.com/ai/sandboxes/ and retry")
		}
		install = setup.Command{Name: "brew", Args: []string{"install", "docker/tap/sbx"}}
		w.ui.Print("SBX is missing. Docker's official Homebrew cask installs native Docker Sandboxes:\n  brew install docker/tap/sbx\n")
	case "linux":
		for _, tool := range []string{"bash", "curl", "tar", "sha256sum"} {
			if !w.system.LookPath(tool) {
				return fmt.Errorf("SBX installation needs %s; install it or follow https://docs.docker.com/ai/sandboxes/ and retry", tool)
			}
		}
		install = setup.Command{Name: "bash", Args: []string{"-c", linuxSBXInstall, "radar-install-sbx", "/usr/local"}}
		w.ui.Print("SBX is missing. Download and checksum-verify Docker Sandboxes 0.50.1, then run Docker's installer in /usr/local.\nThe installer may request an administrator password; existing SBX installations are not replaced.\n")
	default:
		return fmt.Errorf("assisted SBX installation requires macOS or native Linux")
	}
	yes, err := w.ui.Confirm("Install Docker Sandboxes now?", true)
	if err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("SBX installation was declined; install Docker Sandboxes or continue setup with SBX disabled")
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := w.system.Run(bounded, install); err != nil {
		return fmt.Errorf("SBX installation did not complete; repair the installation or retry setup")
	}
	if w.completed != nil {
		w.completed("SBX installation")
	}
	if !w.system.LookPath("sbx") {
		return fmt.Errorf("SBX is not on this terminal's PATH after installation; check the installer output, update PATH and rerun radar setup")
	}
	return nil
}
