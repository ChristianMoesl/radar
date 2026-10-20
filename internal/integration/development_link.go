package integration

// DevelopmentLink carries a structured remote PR relationship. The review
// provider validates it and owns its source identity and linking-key format.
type DevelopmentLink struct {
	URL, Repository, ExternalID, Branch string
}

type DevelopmentLinkResolution struct {
	EntityKey   string
	LinkingKeys []string
}

type DevelopmentLinkResolver interface {
	ResolveDevelopmentLink(DevelopmentLink) (DevelopmentLinkResolution, bool, error)
}
