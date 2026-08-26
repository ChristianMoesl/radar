package settings

import "strings"

type Config struct {
	Enabled          bool      `json:"enabled"`
	Kit              KitConfig `json:"kit"`
	AdditionalMounts []string  `json:"additional_mounts"`
}

type KitConfig struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

func Default() Config {
	return Config{Kit: KitConfig{Name: "shell"}, AdditionalMounts: []string{}}
}

func ApplyDefaults(config *Config) {
	if strings.TrimSpace(config.Kit.Name) == "" {
		config.Kit.Name = Default().Kit.Name
	}
}
