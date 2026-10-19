package settings

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Org             string   `yaml:"org"`
	Site            string   `yaml:"site,omitempty"`
	Enabled         *bool    `yaml:"enabled,omitempty"`
	MonitorQuery    string   `yaml:"monitor_query"`
	MonitorStatuses []string `yaml:"monitor_statuses"`
}

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for _, key := range []string{"api_key", "app_key", "access_token"} {
		if _, exists := fields[key]; exists {
			return fmt.Errorf("datadog.%s is no longer supported; remove it manually and run radar setup for Pup OAuth", key)
		}
	}
	type plain Config
	next := plain(*c)
	if err := node.Decode(&next); err != nil {
		return err
	}
	*c = Config(next)
	return nil
}

func Default() Config {
	return Config{Org: "radar", MonitorStatuses: []string{"Alert", "Warn", "No Data"}}
}

func ApplyDefaults(config *Config) {
	if config.Org == "" {
		config.Org = "radar"
	}
	if config.MonitorStatuses == nil {
		config.MonitorStatuses = Default().MonitorStatuses
	}
	for i, status := range config.MonitorStatuses {
		trimmed := strings.TrimSpace(status)
		if canonical, ok := CanonicalMonitorStatus(trimmed); ok {
			trimmed = canonical
		}
		config.MonitorStatuses[i] = trimmed
	}
}

func Validate(config Config) error {
	if strings.TrimSpace(config.Org) == "" {
		return fmt.Errorf("datadog.org must name a Pup OAuth session")
	}
	if config.Site != "" {
		if _, err := NormalizeSite(config.Site); err != nil {
			return err
		}
	}
	if len(config.MonitorStatuses) == 0 {
		return fmt.Errorf("datadog.monitor_statuses must not be empty")
	}
	statuses := map[string]string{}
	for i, status := range config.MonitorStatuses {
		canonical, ok := CanonicalMonitorStatus(status)
		if !ok {
			return fmt.Errorf("datadog.monitor_statuses[%d] has unsupported value %q", i, status)
		}
		normalized := strings.ToLower(canonical)
		if previous, exists := statuses[normalized]; exists {
			return fmt.Errorf("datadog.monitor_statuses values %q and %q match case-insensitively", previous, status)
		}
		statuses[normalized] = status
	}
	return nil
}

func CanonicalMonitorStatus(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "alert":
		return "Alert", true
	case "warn":
		return "Warn", true
	case "no data":
		return "No Data", true
	default:
		return "", false
	}
}
