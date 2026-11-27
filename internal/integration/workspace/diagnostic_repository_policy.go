package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"radar/internal/configfile"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

const diagnosticRepositoryLimit = 64 << 10

var _ integration.RepositoryPolicyDiagnostics = Source{}

func (Source) DiagnosticRepositoryPolicy(ctx context.Context, parent, memberID, scope string) (protocol.DiagnosticRepositoryPolicy, error) {
	root, err := DefaultRoot()
	if err != nil {
		return protocol.DiagnosticRepositoryPolicy{}, fmt.Errorf("repository policy registration unavailable")
	}
	return diagnosticRepositoryPolicy(ctx, root, parent, memberID, scope, captureRepositoryConfiguration)
}

func diagnosticRepositoryPolicy(ctx context.Context, root, parent, memberID, scope string, capture func(string) (*repositoryConfigurationCapture, string)) (protocol.DiagnosticRepositoryPolicy, error) {
	registry, err := workspacegroup.DiagnosticLoad(root)
	if err != nil {
		return protocol.DiagnosticRepositoryPolicy{}, fmt.Errorf("repository policy registration unavailable")
	}
	var repository string
	matches := 0
	for _, group := range registry.Workspaces {
		if group.ID != parent || (scope != "" && filepath.Clean(scope) != filepath.Clean(group.Path)) {
			continue
		}
		for _, member := range group.Members {
			view := protocol.DiagnosticWorkspaceResource{Kind: "member", Member: &protocol.DiagnosticMember{Path: member.Path, Repository: member.Repository}}
			if protocol.DiagnosticWorkspaceResourceID(group.ID, view) == memberID {
				matches++
				repository = member.Repository
			}
		}
	}
	if matches != 1 {
		return protocol.DiagnosticRepositoryPolicy{}, fmt.Errorf("repository policy member unavailable or ambiguous in selected scope")
	}
	view := protocol.DiagnosticRepositoryPolicy{ID: memberID, WorkspaceID: parent, Origin: "current_registered_member_repository", Evidence: "unavailable", Settings: []protocol.DiagnosticSetting{}}
	unavailable := func(reason string) (protocol.DiagnosticRepositoryPolicy, error) {
		view.Available, view.Evidence, view.Reason, view.Settings = false, "unavailable", reason, []protocol.DiagnosticSetting{}
		return view, nil
	}
	expired := func() bool {
		deadline, bounded := ctx.Deadline()
		return ctx.Err() != nil || (bounded && !time.Now().Before(deadline))
	}
	if expired() {
		return unavailable("cancelled_or_timed_out")
	}
	captured, reason := capture(repository)
	if captured == nil {
		return unavailable(reason)
	}
	defer captured.close()
	cfg := RepoConfig{}
	var doc *yaml.Node
	if !captured.absent {
		cfg, doc, err = decodeRepoConfig(captured.data)
		if err != nil {
			return unavailable("configuration_invalid")
		}
	}
	add := func(key, value string) {
		setting := protocol.DiagnosticSetting{Key: key, Value: value, Origin: "repository_default", Declaration: "absent"}
		node := doc
		for _, part := range strings.Split(key, ".") {
			if node == nil {
				break
			}
			node = configfile.Field(node, part)
		}
		for node != nil && node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if node != nil {
			if node.Tag == "!!null" {
				setting.Declaration = "null"
			} else {
				setting.Origin, setting.Declaration = "repository_file", "value"
			}
		}
		view.Settings = append(view.Settings, setting)
	}
	add("copy_files", strconv.Itoa(len(cfg.CopyFiles)))
	add("setup", strconv.Itoa(len(cfg.Setup)))
	add("model", strconv.FormatBool(cfg.Model != ""))
	add("thinking", strconv.FormatBool(cfg.Thinking != ""))
	sandbox := SandboxConfig{}
	if cfg.SBX != nil {
		sandbox = *cfg.SBX
	}
	mode := "inherit"
	if sandbox.Enabled != nil {
		if *sandbox.Enabled {
			mode = "required"
		} else {
			mode = "disabled"
		}
	}
	add("sbx.enabled", mode)
	add("sbx.ready_command", strconv.FormatBool(sandbox.ReadyCommand != nil && len(*sandbox.ReadyCommand) != 0))
	add("sbx.env_file", strconv.FormatBool(sandbox.EnvFile != nil && *sandbox.EnvFile != ""))
	kit := SandboxKitConfig{}
	if sandbox.Kit != nil {
		kit = *sandbox.Kit
	}
	add("sbx.kit.name", strconv.FormatBool(kit.Name != ""))
	add("sbx.kit.path", strconv.FormatBool(kit.Path != ""))
	add("sbx.additional_mounts", strconv.Itoa(len(sandbox.AdditionalMounts)))
	if expired() {
		return unavailable("cancelled_or_timed_out")
	}
	after, err := workspacegroup.DiagnosticLoad(root)
	beforeJSON, _ := json.Marshal(registry)
	afterJSON, _ := json.Marshal(after)
	if err != nil || string(beforeJSON) != string(afterJSON) {
		return unavailable("registration_changed_or_unavailable")
	}
	if !captured.unchanged() {
		return unavailable("configuration_changed_or_read_incomplete")
	}
	// Final registration/configuration revalidation is also part of this
	// cooperative read. Absolute expiry applies even before Err is notified.
	if expired() {
		return unavailable("cancelled_or_timed_out")
	}
	view.Available = true
	view.Evidence = "fresh_bounded_repository_configuration"
	if captured.absent {
		view.Evidence = "repository_configuration_absent"
	}
	return view, nil
}

type repositoryConfigurationCapture struct {
	repository    string
	directory     *os.File
	directoryInfo os.FileInfo
	file          *os.File
	before        os.FileInfo
	data          []byte
	absent        bool
}

// Walk every absolute directory component without following symlinks. A
// registration path is not authority to follow a replaced directory elsewhere.
func openDiagnosticRepository(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return nil, fmt.Errorf("repository unavailable")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "repository")
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		next, err := unix.Openat(int(directory.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		directory.Close()
		if err != nil {
			return nil, err
		}
		directory = os.NewFile(uintptr(next), "repository")
	}
	return directory, nil
}

func openDiagnosticConfiguration(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), ".radar.yaml", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "repository configuration"), nil
}

func captureRepositoryConfiguration(repository string) (*repositoryConfigurationCapture, string) {
	directory, err := openDiagnosticRepository(repository)
	if err != nil {
		return nil, "repository_unavailable"
	}
	captured := &repositoryConfigurationCapture{repository: repository, directory: directory}
	fail := func(reason string) (*repositoryConfigurationCapture, string) { captured.close(); return nil, reason }
	captured.directoryInfo, err = directory.Stat()
	if err != nil {
		return fail("repository_unavailable")
	}
	captured.file, err = openDiagnosticConfiguration(directory)
	if err == unix.ENOENT {
		captured.absent = true
		if !captured.unchanged() {
			return fail("configuration_changed_or_read_incomplete")
		}
		return captured, ""
	}
	if err != nil {
		return fail("configuration_read_unavailable")
	}
	captured.before, err = captured.file.Stat()
	if err != nil || !captured.before.Mode().IsRegular() || captured.before.Size() > diagnosticRepositoryLimit {
		return fail("configuration_not_regular_or_exceeds_bound")
	}
	captured.data, err = io.ReadAll(io.LimitReader(captured.file, diagnosticRepositoryLimit+1))
	if err != nil || len(captured.data) > diagnosticRepositoryLimit || !captured.unchanged() {
		return fail("configuration_changed_or_read_incomplete")
	}
	return captured, ""
}

func (c *repositoryConfigurationCapture) close() {
	if c.file != nil {
		c.file.Close()
	}
	if c.directory != nil {
		c.directory.Close()
	}
}

func (c *repositoryConfigurationCapture) unchanged() bool {
	directory, err := openDiagnosticRepository(c.repository)
	if err != nil {
		return false
	}
	defer directory.Close()
	currentDirectory, err := directory.Stat()
	if err != nil || !os.SameFile(c.directoryInfo, currentDirectory) {
		return false
	}
	current, err := openDiagnosticConfiguration(directory)
	if c.absent {
		if current != nil {
			current.Close()
		}
		return err == unix.ENOENT
	}
	if err != nil {
		return false
	}
	defer current.Close()
	now, pathErr := current.Stat()
	after, statErr := c.file.Stat()
	return pathErr == nil && statErr == nil && now.Mode().IsRegular() && after.Mode().IsRegular() &&
		os.SameFile(c.before, after) && os.SameFile(c.before, now) &&
		c.before.Size() == after.Size() && int64(len(c.data)) == after.Size() && after.Size() == now.Size() &&
		c.before.ModTime().Equal(after.ModTime()) && after.ModTime().Equal(now.ModTime())
}
