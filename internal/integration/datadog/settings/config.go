package settings

import (
	"fmt"
	"strings"
)

type Config struct {
	MonitorQuery    string   `json:"monitor_query"`
	MonitorStatuses []string `json:"monitor_statuses"`
}

func Default() Config {
	return Config{MonitorStatuses: []string{"Alert", "Warn", "No Data"}}
}

func ApplyDefaults(config *Config) {
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
