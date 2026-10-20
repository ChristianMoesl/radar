package jira

import (
	"radar/internal/integration/contracttest"
	"testing"
)

func TestTWGFixtureProcess(t *testing.T)              { contracttest.RunTWGFixtureProcess(t) }
func installTWGFixture(t *testing.T, endpoint string) { contracttest.InstallTWGFixture(t, endpoint) }
func fixtureConnection(t *testing.T, endpoint string) Config {
	installTWGFixture(t, endpoint)
	return Config{BaseURL: "https://jira.example.test", CloudID: "cloud-fixture", Profile: "radar", AccountID: "fixture-account"}
}
