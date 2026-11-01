package pi

import "time"

// FeedbackAvailable uses a live Pi runtime report, not MCP configuration or
// credentials. The new feedback session checks its own tool registry as well.
func FeedbackAvailable() bool {
	profile, err := ProfileDir()
	if err != nil {
		return false
	}
	for _, report := range loadedReleases(profile) {
		if report.LinearAvailable && time.Now().UnixMilli()-report.Updated <= 15000 {
			return true
		}
	}
	return false
}
