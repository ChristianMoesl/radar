package settings

import (
	"fmt"
	"strings"
)

var supportedSites = map[string]bool{
	"datadoghq.com":     true,
	"us3.datadoghq.com": true,
	"us5.datadoghq.com": true,
	"datadoghq.eu":      true,
	"ap1.datadoghq.com": true,
	"ap2.datadoghq.com": true,
	"ddog-gov.com":      true,
}

func NormalizeSite(site string) (string, error) {
	site = strings.TrimSpace(strings.ToLower(site))
	site = strings.TrimPrefix(site, "https://")
	site = strings.TrimPrefix(site, "http://")
	site = strings.TrimSuffix(site, "/")
	site = strings.TrimPrefix(site, "api.")
	site = strings.TrimPrefix(site, "app.")
	if strings.ContainsAny(site, "/?#") || !supportedSites[site] {
		return "", fmt.Errorf("datadog.site must be a supported Datadog site hostname")
	}
	return site, nil
}
