package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Never include a response body or the transport's arbitrary error text in a
// credential check failure. Servers/proxies can echo authentication headers.
func Get(ctx context.Context, client *http.Client, endpoint string, auth func(*http.Request), result any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid service endpoint")
	}
	req.Header.Set("Accept", "application/json")
	if auth != nil {
		auth(req)
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed (check network connectivity)")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("service returned HTTP %d", res.StatusCode)
	}
	if result != nil {
		if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(result) != nil {
			return fmt.Errorf("service returned an invalid response")
		}
	}
	return nil
}

func Required(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a value is required")
	}
	return nil
}

func ValidateSiteURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("enter an HTTPS site URL without credentials, a path or query")
	}
	return nil
}

func ExpandPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(value, "~"), "/"))
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("use an absolute path or ~/path")
	}
	return filepath.Clean(value), nil
}

func ValidateAPIURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("enter an HTTPS API base URL without credentials or a query")
	}
	return nil
}

func DirectoryPath(value string) (string, error) {
	path, err := ExpandPath(value)
	if err != nil {
		return "", err
	}
	current := path
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("path must be a directory")
			}
			return path, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("cannot access directory: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("directory has no accessible parent")
		}
		current = parent
	}
}
