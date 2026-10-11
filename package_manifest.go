package radar

import (
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"
)

// The daemon must enforce the same prerequisites as the published Pi package,
// even when Homebrew (rather than the signed standalone manifest) owns upgrades.
//
//go:embed package.json
var piPackageManifest []byte

// PiPrerequisites returns the minimum Node and Pi runtime versions.
func PiPrerequisites() (node, pi string, err error) {
	var pkg struct {
		Engines          map[string]string `json:"engines"`
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(piPackageManifest, &pkg); err != nil {
		return "", "", err
	}
	n := regexp.MustCompile(`^>=([1-9][0-9]*)$`).FindStringSubmatch(pkg.Engines["node"])
	p := regexp.MustCompile(`^>=([0-9]+\.[0-9]+\.[0-9]+)$`).FindStringSubmatch(pkg.PeerDependencies["@earendil-works/pi-coding-agent"])
	if n == nil || p == nil {
		return "", "", errors.New("unsupported Pi package prerequisite policy")
	}
	return n[1] + ".0.0", p[1], nil
}
