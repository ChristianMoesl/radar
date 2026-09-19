package settings

import "strings"

const DefaultKitName = "docker.io/christianmoesl/radar-kit:latest"

type Config struct {
	Enabled          *bool     `json:"enabled,omitempty"`
	Kit              KitConfig `json:"kit"`
	AdditionalMounts []string  `json:"additional_mounts"`
}

type KitConfig struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
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
