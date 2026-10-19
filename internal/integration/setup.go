package integration

import (
	"context"
	"net/http"
	"sort"

	"radar/internal/config"
	"radar/internal/setup"
)

type SetupPhase int

const (
	SetupEnvironment SetupPhase = iota
	SetupDirectories
	SetupConnections
)

type SetupStage struct {
	Phase SetupPhase
	Order int
}

// SetupRequest is supplied only by the foreground wizard. Setup may perform
// explicitly accepted installs/logins/probes; record such effects with Completed.
// Persistent settings, secrets and files are published only after review.
type SetupRequest struct {
	UI              setup.UI
	System          setup.System
	HTTP            *http.Client
	Draft           *config.SetupDraft
	Config          *config.Config
	Path            string
	Secret          func(title, hint, environment, namespace, key string) (string, error)
	EnsureExtension func(context.Context, string, string) error
	Completed       func(string)
}

type SetupProvider interface {
	Integration
	SetupStage() SetupStage
	Setup(context.Context, SetupRequest) (setup.Plan, error)
}

func (r Registry) SetupProviders(phase SetupPhase) []SetupProvider {
	var providers []SetupProvider
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(SetupProvider); ok && provider.SetupStage().Phase == phase {
			providers = append(providers, provider)
		}
	}
	sort.SliceStable(providers, func(i, j int) bool { return providers[i].SetupStage().Order < providers[j].SetupStage().Order })
	return providers
}
