package settings

import (
	"fmt"
	"strings"
)

const DefaultKitName = "docker.io/christianmoesl/radar-kit:latest"

type Config struct {
	Enabled          *bool     `yaml:"enabled,omitempty"`
	Kit              KitConfig `yaml:"kit"`
	AdditionalMounts []string  `yaml:"additional_mounts"`
	EnvFile          string    `yaml:"env_file,omitempty"`
	ReadyCommand     []string  `yaml:"ready_command,omitempty"`
}

type KitConfig struct {
	Name string `yaml:"name"`
	Path string `yaml:"path,omitempty"`
}

func Default() Config {
	return Config{Kit: KitConfig{Name: DefaultKitName}, AdditionalMounts: []string{}}
}

func ApplyDefaults(config *Config) {
	if strings.TrimSpace(config.Kit.Name) == "" {
		config.Kit.Name = DefaultKitName
	}
}

// WorkspaceEnabled resolves only the user default. Repository settings are applied
// afterwards, and explicit enablement is validated before provisioning resources.
func (c Config) WorkspaceEnabled(goos string, lookPath func(string) error) bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return goos == "darwin" && lookPath("sbx") == nil
}

// ValidateReadyCommand checks argv structure only. The executable belongs to the
// sandbox, not the host, and arguments are never trimmed or shell-evaluated.
func ValidateReadyCommand(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	if strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("sbx.ready_command requires a nonempty executable")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("sbx.ready_command must not contain NUL bytes")
		}
	}
	return nil
}
