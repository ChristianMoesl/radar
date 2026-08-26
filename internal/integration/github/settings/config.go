package settings

import "radar/internal/integration/github/filters"

type Config struct {
	Filters filters.Config `json:"filters"`
}

func Default() Config {
	return Config{Filters: filters.Config{
		MuteRepos:         []string{},
		DeprioritizeRepos: []string{},
		MuteUsers:         []string{},
		DeprioritizeUsers: []string{},
		Rules: []filters.Rule{{
			Name:   "Track bot PRs in selected repos",
			Repos:  []string{"example-org/*"},
			Users:  []string{"dependabot[bot]", "renovate[bot]"},
			Action: "deprioritize",
		}},
	}}
}
