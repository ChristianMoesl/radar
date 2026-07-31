package datadog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIClientSearchUsesOneScopedMonitorRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/monitor/search" {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		if got, want := req.URL.Query().Get("query"), `(tag:team:cap) status:(Alert OR Warn OR "No Data")`; got != want {
			t.Fatalf("query = %q, want %q", got, want)
		}
		if req.URL.Query().Get("page") != "0" || req.URL.Query().Get("per_page") != "1000" {
			t.Fatalf("pagination = %s", req.URL.RawQuery)
		}
		if req.Header.Get("DD-API-KEY") != "api-secret" || req.Header.Get("DD-APPLICATION-KEY") != "app-secret" {
			t.Fatal("request is missing Datadog authentication headers")
		}
		_ = json.NewEncoder(w).Encode(monitorSearchResponse{Monitors: []monitor{{ID: 42, Name: "API errors", Status: "Alert"}}})
	}))
	defer server.Close()

	response, err := (apiClient{httpClient: server.Client()}).Search(context.Background(), credentials{
		APIKey:     "api-secret",
		AppKey:     "app-secret",
		APIBaseURL: server.URL,
	}, "tag:team:cap")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if len(response.Monitors) != 1 || response.Monitors[0].ID != 42 {
		t.Fatalf("response = %+v", response)
	}
}

func TestCredentialsComeOnlyFromEnvironment(t *testing.T) {
	t.Setenv("RADAR_DATADOG_API_KEY", "api-secret")
	t.Setenv("RADAR_DATADOG_APP_KEY", "app-secret")
	t.Setenv("RADAR_DATADOG_SITE", "datadoghq.eu")

	cfg, missing, err := credentialsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	if cfg.APIKey != "api-secret" || cfg.AppKey != "app-secret" {
		t.Fatalf("credentials = %+v", cfg)
	}
	if cfg.APIBaseURL != "https://api.datadoghq.eu" || cfg.AppBaseURL != "https://app.datadoghq.eu" {
		t.Fatalf("Datadog URLs = %q, %q", cfg.APIBaseURL, cfg.AppBaseURL)
	}
}

func TestCredentialsReportMissingRequiredEnvironment(t *testing.T) {
	t.Setenv("RADAR_DATADOG_API_KEY", "")
	t.Setenv("RADAR_DATADOG_APP_KEY", "")
	t.Setenv("RADAR_DATADOG_SITE", "")

	cfg, missing, err := credentialsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 || missing[0] != "RADAR_DATADOG_API_KEY" || missing[1] != "RADAR_DATADOG_APP_KEY" {
		t.Fatalf("missing = %v", missing)
	}
	if cfg.Site != defaultSite {
		t.Fatalf("site = %q, want %q", cfg.Site, defaultSite)
	}
}

func TestCredentialsRejectUnsupportedSiteBeforeSendingSecrets(t *testing.T) {
	t.Setenv("RADAR_DATADOG_API_KEY", "api-secret")
	t.Setenv("RADAR_DATADOG_APP_KEY", "app-secret")
	t.Setenv("RADAR_DATADOG_SITE", "example.com")

	if _, _, err := credentialsFromEnv(); err == nil {
		t.Fatal("credentialsFromEnv() error = nil, want unsupported site error")
	}
}

func TestRegionalDatadogAppURL(t *testing.T) {
	if got := datadogAppBaseURL("us3.datadoghq.com"); got != "https://us3.datadoghq.com" {
		t.Fatalf("app URL = %q", got)
	}
}
