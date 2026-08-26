package integration

type DevelopmentLink struct {
	URL        string
	Repository string
	ExternalID string
	Branch     string
}

type DevelopmentLinkResolution struct {
	EntityKey   string
	LinkingKeys []string
}

type DevelopmentLinkResolver interface {
	ResolveDevelopmentLink(link DevelopmentLink) (DevelopmentLinkResolution, bool, error)
}
