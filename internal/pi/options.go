package pi

import (
	"fmt"
	"strings"
)

var validThinkingLevels = map[string]struct{}{
	"off":     {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
}

func ValidateThinking(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if value == "" {
			return nil
		}
		return fmt.Errorf("thinking is empty")
	}
	if _, ok := validThinkingLevels[trimmed]; !ok {
		return fmt.Errorf("thinking must be one of: off, minimal, low, medium, high, xhigh")
	}
	return nil
}
