package settings

import (
	"fmt"
	"regexp"
	"strings"
)

type Config struct {
	Enabled      *bool    `yaml:"enabled,omitempty"`
	Profile      string   `yaml:"profile"`
	Organization string   `yaml:"organization"`
	Workspaces   []string `yaml:"workspaces"`
}

func Default() Config { return Config{Profile: "default", Workspaces: []string{}} }

func ApplyDefaults(c *Config) {
	if c.Profile == "" {
		c.Profile = "default"
	}
	if c.Workspaces == nil {
		c.Workspaces = []string{}
	}
}

var selector = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func ValidSelector(value string) bool {
	return len(value) <= 255 && selector.MatchString(value) && value != "." && value != ".."
}

func Validate(c Config) error {
	if !ValidSelector(c.Profile) {
		return fmt.Errorf("terraform.profile must name an existing tfctl profile")
	}
	if c.Organization != "" && !ValidSelector(c.Organization) {
		return fmt.Errorf("terraform.organization must be an organization name")
	}
	seen := map[string]bool{}
	for _, name := range c.Workspaces {
		if !ValidSelector(name) || strings.TrimSpace(name) != name || seen[name] {
			return fmt.Errorf("terraform.workspaces must contain distinct workspace names")
		}
		seen[name] = true
	}
	return nil
}
