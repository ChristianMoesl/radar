package settings

import "radar/internal/integration/github/filters"

type Config struct {
	Enabled *bool          `json:"enabled,omitempty"`
	Filters filters.Config `json:"filters"`
}

func Default() Config {
	return Config{Filters: filters.Config{
		MuteRepos:         []string{},
		DeprioritizeRepos: []string{},
		MuteUsers:         []string{},
		DeprioritizeUsers: []string{},
		Rules:             []filters.Rule{},
	}}
}
