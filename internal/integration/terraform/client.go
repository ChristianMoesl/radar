// Package terraform tracks personally created HCP Terraform runs through tfctl.
package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"radar/internal/command"
	"radar/internal/integration/terraform/settings"
	"radar/internal/setup"
)

const minimumVersion = "0.4.0"

type outputFunc func(context.Context, setup.Command) (string, error)

type client struct {
	profile string
	output  outputFunc
}

func (c client) version(ctx context.Context) (string, error) {
	env := cliEnv()
	if c.output != nil {
		return c.output(ctx, setup.Command{Name: "tfctl", Args: []string{"--version"}, Env: env, CaptureStderr: true})
	}
	data, err := command.Version(ctx, "tfctl", env)
	return string(data), err
}

func cliEnv() []string {
	return append(os.Environ(), "CHECKPOINT_DISABLE=1", "TFCTL_TELEMETRY=disabled", "DO_NOT_TRACK=true")
}

func (c client) run(ctx context.Context, args ...string) (string, error) {
	// tfctl 0.4.0 requires global flags after the leaf subcommand.
	args = append(args, "--profile="+c.profile, "--json", "--no-color")
	env := cliEnv()
	if c.output != nil {
		return c.output(ctx, setup.Command{Name: "tfctl", Args: args, Env: env})
	}
	data, err := command.OutputResult(ctx, "tfctl", args, env)
	return string(data), err
}

func (c client) get(ctx context.Context, path string) (document, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return document{}, fmt.Errorf("invalid Terraform API path")
	}
	data, err := c.run(ctx, "api", path, "--method=GET")
	if err != nil {
		return document{}, fmt.Errorf("Terraform read failed; check tfctl authentication, access and connectivity")
	}
	var doc document
	if json.Unmarshal([]byte(data), &doc) != nil || len(doc.Data) == 0 || len(doc.Errors) != 0 {
		return document{}, fmt.Errorf("tfctl returned an invalid API response")
	}
	return doc, nil
}

type session struct {
	client client
	host   string
	userID string
	spaces map[string]string // immutable remote workspace ID -> configured name
}

func (c client) connect(ctx context.Context, cfg settings.Config) (session, error) {
	data, err := c.run(ctx, "auth", "status")
	var status struct {
		Hostname string `json:"hostname"`
		Active   bool   `json:"active"`
	}
	if json.Unmarshal([]byte(data), &status) != nil || err != nil || !status.Active {
		// Released 0.4.0 does not distinguish absent/rejected/unreachable auth.
		// Do not silently disable a configured source on this ambiguous result.
		return session{}, fmt.Errorf("Terraform authentication could not be verified; run tfctl auth status --profile=%s", cfg.Profile)
	}
	host, err := normalizeHost(status.Hostname)
	if err != nil {
		return session{}, err
	}
	doc, err := c.get(ctx, "/account/details")
	if err != nil {
		return session{}, err
	}
	account, err := doc.one("users")
	if err != nil || !validID(account.ID, "user-") {
		return session{}, fmt.Errorf("Terraform account identity is incomplete")
	}
	var attrs struct {
		Service *bool `json:"is-service-account"`
	}
	if json.Unmarshal(account.Attributes, &attrs) != nil || attrs.Service == nil || *attrs.Service {
		return session{}, fmt.Errorf("Terraform requires a personal user login for own-run tracking")
	}
	if auth := account.related("authenticated-resource"); auth.ID != "" && (auth.Type != "users" || auth.ID != account.ID) {
		return session{}, fmt.Errorf("Terraform authentication is not a personal user")
	}
	s := session{client: c, host: host, userID: account.ID, spaces: map[string]string{}}
	for _, name := range cfg.Workspaces {
		doc, err := c.get(ctx, "/organizations/"+url.PathEscape(cfg.Organization)+"/workspaces/"+url.PathEscape(name))
		if err != nil {
			return session{}, fmt.Errorf("Terraform workspace %s is unavailable", name)
		}
		space, err := doc.one("workspaces")
		var attrs struct {
			Name string `json:"name"`
		}
		if err != nil || !validID(space.ID, "ws-") || json.Unmarshal(space.Attributes, &attrs) != nil || attrs.Name != name {
			return session{}, fmt.Errorf("Terraform workspace identity does not match %s", name)
		}
		if org := space.related("organization"); org.ID != "" && org.ID != cfg.Organization {
			return session{}, fmt.Errorf("Terraform workspace organization does not match")
		}
		s.spaces[space.ID] = name
	}
	return s, nil
}

func normalizeHost(value string) (string, error) {
	parsed, err := url.Parse("https://" + value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(value, "\\\r\n\t ") {
		return "", fmt.Errorf("tfctl returned an invalid service hostname")
	}
	return strings.ToLower(parsed.Host), nil
}
