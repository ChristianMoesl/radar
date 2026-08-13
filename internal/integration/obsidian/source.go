package obsidian

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/openurl"
	"radar/internal/protocol"
)

const OpenAction = "obsidian_open"

var validID = regexp.MustCompile(`^(?:[0-9A-HJKMNP-TV-Z]{26}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})$`)

type Source struct {
	vaultPath string
}

type note struct {
	ID          string
	Title       string
	State       string
	Priority    string
	CreatedAt   string
	CompletedAt string
	Path        string
	content     string
	fields      map[string]int
}

type discoveredNote struct {
	note note
	err  error
	path string
}

func NewSource() Source { return Source{} }

func NewSourceAt(vaultPath string) Source { return Source{vaultPath: vaultPath} }

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "obsidian", Label: "Obsidian", DisplayOrder: 0}
}

func (Source) Local() bool { return true }

func (s Source) configuredVault() (string, error) {
	if strings.TrimSpace(s.vaultPath) != "" {
		return config.ObsidianConfig{VaultPath: s.vaultPath}.ValidateAndPrepare()
	}
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return cfg.Obsidian.ValidateAndPrepare()
}

func (s Source) Status(_ context.Context, _ *slog.Logger) integration.StatusResult {
	vault, err := s.configuredVault()
	if err != nil {
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "obsidian", Status: "error", Detail: err.Error()}, CanRun: true}
	}
	root := taskRoot(vault)
	if _, err := os.ReadDir(root); err != nil {
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "obsidian", Status: "error", Detail: fmt.Sprintf("read Obsidian task root %s: %v", root, err)}, CanRun: true}
	}
	return integration.StatusResult{Status: protocol.SourceStatus{Name: "obsidian", Status: "ok"}, CanRun: true}
}

func (s Source) Collect(_ context.Context, req integration.CollectRequest) integration.CollectResult {
	vault, err := s.configuredVault()
	if err != nil {
		status := protocol.SourceStatus{Name: "obsidian", Status: "error", Detail: err.Error()}
		return integration.CollectResult{Observations: previousObservations(req.Previous, nil), SourceStatus: &status}
	}
	discovered, scanErr := discover(vault)
	if scanErr != nil {
		status := protocol.SourceStatus{Name: "obsidian", Status: "error", Detail: scanErr.Error()}
		return integration.CollectResult{Observations: previousObservations(req.Previous, nil), SourceStatus: &status}
	}

	byID := map[string][]int{}
	for i := range discovered {
		if discovered[i].err == nil {
			byID[discovered[i].note.ID] = append(byID[discovered[i].note.ID], i)
		}
	}
	for id, indexes := range byID {
		if len(indexes) < 2 {
			continue
		}
		paths := make([]string, 0, len(indexes))
		for _, index := range indexes {
			paths = append(paths, discovered[index].path)
		}
		sort.Strings(paths)
		for _, index := range indexes {
			discovered[index].err = fmt.Errorf("duplicate radar-id %s in %s", id, strings.Join(paths, ", "))
		}
	}

	valid := make([]note, 0, len(discovered))
	invalidPaths := map[string]bool{}
	invalidIDs := map[string]bool{}
	details := make([]string, 0)
	for _, item := range discovered {
		if item.err != nil {
			invalidPaths[filepath.Clean(item.path)] = true
			if item.note.ID != "" {
				invalidIDs[item.note.ID] = true
			}
			details = append(details, fmt.Sprintf("%s: %v", item.path, item.err))
			continue
		}
		valid = append(valid, item.note)
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].ID < valid[j].ID })
	observations := make([]integration.Observation, 0, len(valid))
	for _, current := range valid {
		observations = append(observations, observationsFor(vault, current)...)
	}
	if len(invalidPaths) > 0 {
		observations = append(observations, previousObservations(req.Previous, func(ref protocol.SourceRef) bool {
			return invalidIDs[ref.Metadata["radar_id"]] || invalidPaths[filepath.Clean(ref.Metadata["note_path"])]
		})...)
	}
	status := protocol.SourceStatus{Name: "obsidian", Status: "ok"}
	complete := true
	if len(details) > 0 {
		sort.Strings(details)
		status.Status = "partial"
		status.Detail = fmt.Sprintf("%d valid task(s), %d invalid task note(s): %s", len(valid), len(details), strings.Join(details, "; "))
		complete = false
	}
	return integration.CollectResult{Observations: deduplicateObservations(observations), Complete: complete, SourceStatus: &status}
}

func discover(vault string) ([]discoveredNote, error) {
	root := taskRoot(vault)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read Obsidian task root %s: %w", root, err)
	}
	items := make([]discoveredNote, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		current, err := readNote(path)
		items = append(items, discoveredNote{note: current, err: err, path: path})
	}
	return items, nil
}

func readNote(path string) (note, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return note{Path: path}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return note{Path: path}, fmt.Errorf("task note must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return note{Path: path}, err
	}
	current, err := parseNote(string(data))
	current.Path = path
	current.Title = strings.TrimSuffix(filepath.Base(path), ".md")
	if err == nil && strings.TrimSpace(current.Title) == "" {
		err = fmt.Errorf("task note filename must contain a title")
	}
	return current, err
}

func parseNote(content string) (note, error) {
	current := note{content: content, fields: map[string]int{}}
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return current, fmt.Errorf("Markdown frontmatter is required")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
		key, value, ok := strings.Cut(lines[i], ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, duplicate := current.fields[key]; duplicate {
			return current, fmt.Errorf("duplicate frontmatter field %q", key)
		}
		current.fields[key] = i
		value = strings.TrimSpace(value)
		switch key {
		case "radar-id":
			current.ID = value
		case "radar-state":
			current.State = value
		case "radar-priority":
			current.Priority = value
		case "radar-created-at":
			current.CreatedAt = value
		case "radar-completed-at":
			current.CompletedAt = value
		}
	}
	if end < 0 {
		return current, fmt.Errorf("Markdown frontmatter is not closed")
	}
	for _, field := range []string{"radar-id", "radar-state", "radar-priority", "radar-created-at", "radar-completed-at"} {
		if _, ok := current.fields[field]; !ok {
			return current, fmt.Errorf("missing required field %s", field)
		}
	}
	if !validID.MatchString(current.ID) {
		return current, fmt.Errorf("invalid radar-id %q", current.ID)
	}
	if current.State != "open" && current.State != "done" {
		return current, fmt.Errorf("unsupported radar-state %q", current.State)
	}
	if current.Priority != "normal" && current.Priority != "urgent" {
		return current, fmt.Errorf("unsupported radar-priority %q", current.Priority)
	}
	if err := validTimestamp("radar-created-at", current.CreatedAt, false); err != nil {
		return current, err
	}
	if err := validTimestamp("radar-completed-at", current.CompletedAt, current.State == "open"); err != nil {
		return current, err
	}
	if current.State == "done" && current.CompletedAt == "" {
		return current, fmt.Errorf("radar-completed-at is required when radar-state is done")
	}
	if current.State == "open" && current.CompletedAt != "" {
		return current, fmt.Errorf("radar-completed-at must be empty when radar-state is open")
	}
	return current, nil
}

func validTimestamp(field, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("%s must be an RFC 3339 timestamp", field)
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must be in UTC", field)
	}
	return nil
}

func observationsFor(vault string, current note) []integration.Observation {
	identity := "obsidian:task:" + current.ID
	uri := noteURI(vault, current.Path)
	metadata := map[string]string{
		"radar_id": current.ID, "note_path": current.Path,
		"state": current.State, "priority": current.Priority, "created_at": current.CreatedAt,
		"completed_at": current.CompletedAt, "authoring": "true",
	}
	signal := integration.SignalLowPriority
	if current.State == "done" {
		signal = integration.SignalDone
	} else if current.Priority == "urgent" {
		signal = integration.SignalImmediate
	}
	return []integration.Observation{{
		Ref: protocol.SourceRef{
			ID: identity, EntityID: identity, Source: "obsidian", SourceLabel: "Obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative,
			Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary,
			Presentation: protocol.SourceRefPresentation{PreferTitle: true, WorkspaceName: current.Title}, Title: current.Title, URL: uri,
			Status: current.State, CanonicalKey: identity, LinkingKeys: linking.Keys(identity), Metadata: metadata,
		},
		Signal: signal, Reason: "Obsidian task is " + current.State,
	}}
}

func previousObservations(tasks []protocol.Task, keep func(protocol.SourceRef) bool) []integration.Observation {
	observations := make([]integration.Observation, 0)
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.Source != "obsidian" || (keep != nil && !keep(ref)) {
				continue
			}
			observations = append(observations, integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: task.Reason})
		}
	}
	return observations
}

func deduplicateObservations(items []integration.Observation) []integration.Observation {
	byID := map[string]integration.Observation{}
	for _, item := range items {
		byID[item.Ref.ID] = item
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]integration.Observation, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result
}

func (s Source) Create(_ context.Context, title string) (integration.AuthoredTaskIdentity, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("task title must not be empty")
	}
	if strings.ContainsAny(title, "\r\n") {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("task title must be one line")
	}
	if strings.ContainsAny(title, `/\\`) || strings.ContainsRune(title, 0) {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("task title must be a valid filename")
	}
	vault, err := s.configuredVault()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	id, err := newUUID()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	path := filepath.Join(taskRoot(vault), title+".md")
	now := time.Now().UTC().Format(time.RFC3339)
	content := fmt.Sprintf("---\nradar-id: %s\nradar-state: open\nradar-priority: normal\nradar-created-at: %s\nradar-completed-at:\n---\n\n## Intent\n\n## Desired outcome\n\n## Context\n\n## Working notes\n\n## Outcome\n", id, now)
	if err := atomicCreate(path, []byte(content), 0o644); err != nil {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("create Obsidian task note: %w", err)
	}
	return integration.AuthoredTaskIdentity{SourceRefID: "obsidian:task:" + id}, nil
}

func (s Source) SetLifecycle(_ context.Context, ref protocol.SourceRef, state string) (integration.AuthoredTaskIdentity, error) {
	if state != "open" && state != "done" {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("unsupported Obsidian lifecycle %q", state)
	}
	updates := map[string]string{"radar-state": state, "radar-completed-at": ""}
	if state == "done" {
		updates["radar-completed-at"] = "__now_if_empty__"
	}
	return s.mutate(ref, updates)
}

func (s Source) SetPriority(_ context.Context, ref protocol.SourceRef, priority string) (integration.AuthoredTaskIdentity, error) {
	if priority != "normal" && priority != "urgent" {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("unsupported Obsidian priority %q", priority)
	}
	return s.mutate(ref, map[string]string{"radar-priority": priority})
}

func (s Source) mutate(ref protocol.SourceRef, updates map[string]string) (integration.AuthoredTaskIdentity, error) {
	if ref.Source != "obsidian" || ref.Kind != "task" || ref.Authority != protocol.SourceRefAuthorityPrimary {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("source ref %q is not an Obsidian-authored task", ref.ID)
	}
	vault, err := s.configuredVault()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	path := strings.TrimSpace(ref.Metadata["note_path"])
	if path == "" {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("Obsidian task ref %q has no note path", ref.ID)
	}
	if !validManagedNotePath(vault, path) {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("Obsidian task note is outside the managed task root: %s", path)
	}
	current, err := readNote(path)
	if err != nil {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("validate Obsidian task note %s: %w", path, err)
	}
	if ref.ID != "obsidian:task:"+current.ID || (ref.Metadata["radar_id"] != "" && ref.Metadata["radar_id"] != current.ID) {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("Obsidian task identity changed at %s", path)
	}
	if updates["radar-completed-at"] == "__now_if_empty__" {
		updates["radar-completed-at"] = current.CompletedAt
		if updates["radar-completed-at"] == "" {
			updates["radar-completed-at"] = time.Now().UTC().Format(time.RFC3339)
		}
	}
	lines := strings.Split(current.content, "\n")
	for field, value := range updates {
		index, ok := current.fields[field]
		if !ok {
			return integration.AuthoredTaskIdentity{}, fmt.Errorf("managed field %s is missing from %s", field, path)
		}
		lines[index] = field + ": " + value
		if value == "" {
			lines[index] = field + ":"
		}
	}
	content := strings.Join(lines, "\n")
	if _, err := parseNote(content); err != nil {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("updated Obsidian task note is invalid: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	if err := atomicWrite(path, []byte(content), info.Mode().Perm()); err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	return integration.AuthoredTaskIdentity{SourceRefID: ref.ID}, nil
}

func atomicCreate(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (Source) Actions(_ context.Context, req integration.ActionRequest) []integration.Action {
	if req.Ref.Source != "obsidian" || req.Ref.Kind != "task" || req.Ref.URL == "" {
		return nil
	}
	return []integration.Action{{PreferredKey: "o", Source: "Obsidian", Label: req.Label, Detail: "Open in Obsidian", ID: OpenAction, Ref: req.Ref}}
}

func (Source) RunAction(ctx context.Context, req integration.RunActionRequest) (integration.ActionResult, error) {
	if req.ActionID != OpenAction || req.Ref.URL == "" {
		return integration.ActionResult{}, fmt.Errorf("unknown Obsidian action: %s", req.ActionID)
	}
	if err := openurl.Open(ctx, req.Ref.URL); err != nil {
		return integration.ActionResult{}, err
	}
	return integration.ActionResult{Message: "Opened task in Obsidian"}, nil
}

func taskRoot(vault string) string { return config.ObsidianTaskRoot(vault) }

func validManagedNotePath(vault, path string) bool {
	relative, err := filepath.Rel(taskRoot(vault), filepath.Clean(path))
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return len(parts) == 1 && parts[0] != "" && parts[0] != "." && parts[0] != ".." && strings.HasSuffix(parts[0], ".md")
}

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func noteURI(vault, notePath string) string {
	relative, err := filepath.Rel(vault, notePath)
	if err != nil {
		relative = notePath
	}
	query := url.Values{}
	query.Set("vault", filepath.Base(vault))
	query.Set("file", filepath.ToSlash(relative))
	return "obsidian://open?" + query.Encode()
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.ActionProvider = Source{}
var _ integration.TaskAuthoringProvider = Source{}
