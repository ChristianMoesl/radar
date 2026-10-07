package config

import (
	"go.yaml.in/yaml/v3"

	"radar/internal/configfile"
)

const configHeader = "Radar configuration — managed by `radar setup`.\n" +
	"Re-run `radar setup` to update it, or edit it by hand.\n" +
	"See README.md (Config) in the radar repo for configuration options."

// Guidance lives beside the schema; values always come from Config/Default.
var configComments = map[string]string{
	"repository_dirs":                      "Directories searched for existing repository checkouts, not Radar workspaces.",
	"workspace.root_dir":                   "Radar-managed workspaces live here, separately from your original checkouts.",
	"workspace.auto_confirm":               "Apply validated workspace changes without prompting. Set false to require confirmation.",
	"workspace.cleanup.disposable_entries": "Exact workspace-root names cleanup may delete, even with uncommitted content. No paths or globs.",
	"model":                                "Pi model for new workspace sessions; repository settings can override this.",
	"thinking":                             "Pi thinking level; repository settings can override this.",
	"linking_mark_prefixes":                "Ticket prefixes used to link related work, for example [ABC]. An empty list disables only ticket-prefix linking.",
	"sbx":                                  "Sandbox settings for new workspaces; existing workspaces keep their runtime.\nOmit enabled for automatic detection, or set true/false to require/disable sandboxing.",
	"sbx.kit":                              "Sandbox kit to launch. An optional path selects a local kit definition.",
	"sbx.additional_mounts":                "Extra host directories mounted into the sandbox; repository mounts are appended. Use absolute or ~/ paths, with :ro for read-only access.",
	"sbx.env_file":                         "Optional host file passed to SBX as --env-file. Keep credentials out of this configuration.",
	"sbx.ready_command":                    "Command and arguments run inside the sandbox before member setup. An empty list disables the readiness check.",
	"tmux":                                 "Windows for new workspace sessions. Include $RADAR_PI_ARGS exactly once in the Pi command.",
	"github":                               "Omit enabled to detect prerequisites automatically; true requires them and false disables collection.",
	"github.track":                         "Additional open PRs (including drafts) beyond your own PRs, review requests and participation.\nRequires repos with a concrete owner; * is allowed in the repository name.\nOptional authors are exact logins; omit authors to include everyone. Entries are additive.\nTracking adds visibility, not a subscription to every discussion.\nExample entry: {repos: [\"acme/platform-*\"], authors: [\"renovate[bot]\"]}",
	"github.pull_request_rules":            "Control each PR's contribution, not the whole linked task. Local-only work is unaffected.\nMatch repos, authors, or both on the SAME PR. Fields use AND; values within a field use OR.\nOmitted selectors are unrestricted; at least one nonempty selector is required.\nPatterns are case-insensitive and support *. Confirmed bots match name and name[bot].\nFirst matching rule wins per PR; put keep exceptions before broader policies.\nActions: keep (normal), mute (no contribution), deprioritize (low-priority contribution).\nOther sources keep their signals. Muted PRs remain linked and still affect completion.\nA task with only muted PRs is hidden, including in Done; explicit task muting is separate.\nExample entry: {repos: [\"acme/platform-*\"], authors: [\"renovate[bot]\"], action: deprioritize}",
	"github.activity_rules":                "Filter otherwise relevant comments/reviews by repos, actors, or both.\nSame AND/OR, wildcard, bot-alias and first-match rules as pull_request_rules.\nActions: keep (normal involvement-based relevance) or ignore. keep never forces attention.\nFeedback on your PRs and replies in review threads you joined can raise attention.\nUnrelated tracked discussions do not. Ignoring activity does not cancel a direct review request.\nPR-level mute/deprioritize still applies after activity rules; other sources are unaffected.\nExample entry: {actors: [\"review-bot[bot]\"], action: ignore}",
	"jira":                                 "Omit enabled to detect prerequisites automatically; true requires them and false disables collection.\nCredentials belong in secrets.yaml; RADAR_JIRA_* environment values override saved settings.",
	"jira.authoritative_issue_types":       "Issue types collected as assigned work. An empty list disables assigned collection and makes title discoveries informational.",
	"jira.status_mapping":                  "Map Jira status names to low_priority, in_progress, attention, or immediate. Use {} for no explicit mappings.",
	"jira.unmapped_status":                 "Signal used for Jira statuses not listed above.",
	"datadog":                              "Omit enabled to detect prerequisites automatically; true requires them and false disables collection.\nCredentials belong in secrets.yaml; RADAR_DATADOG_* environment values override saved settings.",
	"datadog.monitor_query":                "Scope monitor collection to the monitors you own. An empty query disables automatic collection.",
	"datadog.monitor_statuses":             "Unhealthy monitor states to collect: Alert, Warn, and/or No Data.",
	"obsidian.vault_path":                  "Task notes are stored in a Tasks/ folder here. An ordinary directory or an Obsidian vault works.",
}

var secretComments = map[string]string{
	"jira":    "Jira credentials. RADAR_JIRA_API_TOKEN overrides the saved token.",
	"datadog": "Datadog credentials. RADAR_DATADOG_API_KEY and RADAR_DATADOG_APP_KEY override these values.",
}

func configDocument(cfg Config) (*yaml.Node, error) {
	node, err := configfile.Node(cfg)
	if err != nil {
		return nil, err
	}
	node.HeadComment = configHeader
	configfile.Annotate(node, configComments)
	return node, nil
}

func marshalConfig(cfg Config) ([]byte, error) {
	node, err := configDocument(cfg)
	if err != nil {
		return nil, err
	}
	return configfile.Encode(node)
}

func secretsDocument(secrets Secrets) (*yaml.Node, error) {
	node, err := configfile.Node(secrets)
	if err != nil {
		return nil, err
	}
	node.HeadComment = "Plaintext credentials: keep this file private (chmod 600) and out of version control.\nEnvironment credentials override saved values."
	configfile.Annotate(node, secretComments)
	return node, nil
}
