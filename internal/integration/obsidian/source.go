package obsidian

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/obsidian/settings"
	"radar/internal/integration/workspace/group"
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
	ID                 string
	Title              string
	State              string
	Priority           string
	CreatedAt          string
	CompletedAt        string
	CompletionBaseline string
	Path               string
	content            string
	fields             map[string]int
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

func (Source) CanSeedWorkspace(ref protocol.SourceRef) bool {
	return ref.Source == "obsidian" && ref.Kind == "task" && strings.TrimSpace(ref.WorkspaceAnchorPath) != ""
}

func (Source) PrepareWorkspaceSeed(_ context.Context, ref protocol.SourceRef) (integration.WorkspaceSeed, error) {
	if !(Source{}).CanSeedWorkspace(ref) {
		return integration.WorkspaceSeed{}, fmt.Errorf("source ref %q cannot seed an Obsidian workspace", ref.ID)
	}
	if err := settings.ValidateWorkspaceNote(ref.WorkspaceAnchorPath); err != nil {
		return integration.WorkspaceSeed{}, err
	}
	return integration.WorkspaceSeed{
		Name: strings.TrimSpace(ref.Presentation.WorkspaceName), NotePath: strings.TrimSpace(ref.WorkspaceAnchorPath),
	}, nil
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
	if strings.TrimSpace(s.vaultPath) == "" {
		cfg, err := config.Load()
		if err != nil {
			return integration.StatusResult{Status: protocol.SourceStatus{Name: "obsidian", Status: "error", Detail: err.Error()}}
		}
		if strings.TrimSpace(cfg.Obsidian.VaultPath) == "" {
			return integration.OptionalStatus("obsidian", nil, "configure obsidian.vault_path to create tasks and workspaces")
		}
	}
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
	byTitle := map[string][]int{}
	for i := range discovered {
		if discovered[i].err == nil {
			byID[discovered[i].note.ID] = append(byID[discovered[i].note.ID], i)
			byTitle[discovered[i].note.Title] = append(byTitle[discovered[i].note.Title], i)
		}
	}
	for id, indexes := range byID {
		markDuplicates(discovered, indexes, fmt.Sprintf("duplicate radar-id %s", id))
	}
	for title, indexes := range byTitle {
		markDuplicates(discovered, indexes, fmt.Sprintf("duplicate task title %q", title))
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
			notePath := filepath.Clean(ref.Metadata["note_path"])
			return invalidIDs[ref.Metadata["radar_id"]] || invalidPaths[notePath] || invalidPaths[filepath.Dir(notePath)]
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
		if !entry.IsDir() {
			if entry.Name() == settings.ArchiveDirectory {
				items = append(items, discoveredNote{err: fmt.Errorf("archive must be a real directory"), path: filepath.Join(root, entry.Name())})
			}
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if entry.Name() == settings.ArchiveDirectory {
			children, readErr := os.ReadDir(directory)
			if readErr != nil {
				items = append(items, discoveredNote{err: readErr, path: directory})
				continue
			}
			for _, child := range children {
				path := filepath.Join(directory, child.Name())
				if child.IsDir() {
					items = append(items, discoveredNote{err: fmt.Errorf("archive must contain flat Markdown notes"), path: path})
				} else if strings.EqualFold(filepath.Ext(child.Name()), ".md") {
					current, noteErr := readNote(path)
					items = append(items, discoveredNote{note: current, err: noteErr, path: path})
				}
			}
			continue
		}
		children, readErr := os.ReadDir(directory)
		if readErr != nil {
			items = append(items, discoveredNote{err: readErr, path: directory})
			continue
		}
		notes := make([]string, 0, 1)
		for _, child := range children {
			if !child.IsDir() && strings.EqualFold(filepath.Ext(child.Name()), ".md") {
				notes = append(notes, filepath.Join(directory, child.Name()))
			}
		}
		if len(notes) != 1 {
			items = append(items, discoveredNote{err: fmt.Errorf("task directory must contain exactly one Markdown note"), path: directory})
			continue
		}
		path := notes[0]
		current, noteErr := readNote(path)
		directorySuffix := "--" + shortID(current.ID)
		if noteErr == nil && (!strings.HasSuffix(filepath.Base(directory), directorySuffix) || strings.TrimSuffix(filepath.Base(directory), directorySuffix) == "") {
			noteErr = fmt.Errorf("task directory must end with %s", directorySuffix)
		}
		items = append(items, discoveredNote{note: current, err: noteErr, path: path})
	}
	return items, nil
}

func markDuplicates(items []discoveredNote, indexes []int, reason string) {
	if len(indexes) < 2 {
		return
	}
	paths := make([]string, 0, len(indexes))
	for _, index := range indexes {
		paths = append(paths, items[index].path)
	}
	sort.Strings(paths)
	for _, index := range indexes {
		items[index].err = fmt.Errorf("%s in %s", reason, strings.Join(paths, ", "))
	}
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
		// Only top-level keys are managed; nested user metadata and title block
		// scalar contents must not become field indexes.
		if strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t") {
			continue
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
		case "radar-completion-baseline":
			current.CompletionBaseline = value
		}
	}
	if end < 0 {
		return current, fmt.Errorf("Markdown frontmatter is not closed")
	}
	for _, field := range []string{"radar-id", "radar-title", "radar-state", "radar-priority", "radar-created-at", "radar-completed-at"} {
		if _, ok := current.fields[field]; !ok {
			return current, fmt.Errorf("missing required field %s", field)
		}
	}
	var frontmatter map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &frontmatter); err != nil {
		return current, fmt.Errorf("invalid YAML frontmatter")
	}
	title := frontmatter["radar-title"]
	if title.Kind != yaml.ScalarNode || title.Tag != "!!str" || strings.TrimSpace(title.Value) == "" {
		return current, fmt.Errorf("radar-title must be a non-empty YAML string")
	}
	current.Title = strings.TrimSpace(title.Value)
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
	if value := current.CompletionBaseline; value != "" && value != "pending" && !validCompletionBaseline.MatchString(value) {
		return current, fmt.Errorf("invalid radar-completion-baseline %q", value)
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
		"radar_id": current.ID, "note_path": current.Path, "task_directory": filepath.Dir(current.Path),
		"state": current.State, "priority": current.Priority, "created_at": current.CreatedAt,
		"completed_at": current.CompletedAt,
		"content_hash": fmt.Sprintf("%x", sha256.Sum256([]byte(current.content))),
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
			WorkspaceAnchorPath: current.Path, Authored: true,
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

func (s Source) Create(ctx context.Context, title string) (integration.AuthoredTaskIdentity, error) {
	note, err := s.PrepareWorkspaceNote(ctx, title)
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	if err := s.EnsureWorkspaceNote(ctx, note); err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	return integration.AuthoredTaskIdentity{SourceRefID: note.LinkingKey}, nil
}

func (s Source) SetLifecycle(_ context.Context, ref protocol.SourceRef, state string) (integration.AuthoredTaskIdentity, error) {
	if state != "open" && state != "done" {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("unsupported Obsidian lifecycle %q", state)
	}
	updates := map[string]string{"radar-state": state, "radar-completed-at": "", "radar-completion-baseline": "pending"}
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
	_, err := s.mutateNote(ref, func(note) (map[string]string, error) { return updates, nil })
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	return integration.AuthoredTaskIdentity{SourceRefID: ref.ID}, nil
}

func (s Source) mutateNote(ref protocol.SourceRef, update func(note) (map[string]string, error)) (note, error) {
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return note{}, err
	}
	var current note
	err = workspacegroup.WithNoteLock(root, func() error {
		var err error
		changed := false
		current, err = s.mutateNoteLocked(root, ref, func(current note) (map[string]string, error) {
			updates, err := update(current)
			changed = len(updates) > 0
			return updates, err
		})
		if err != nil || !changed {
			return err
		}
		current, err = archiveCompleted(root, current)
		return err
	})
	return current, err
}

func (s Source) mutateNoteLocked(root string, ref protocol.SourceRef, update func(note) (map[string]string, error)) (note, error) {
	current, err := s.noteForRef(ref)
	if err != nil {
		return note{}, err
	}
	path := current.Path
	updates, err := update(current)
	if err != nil {
		return note{}, err
	}
	if len(updates) == 0 {
		return current, nil
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
			if field != "radar-completion-baseline" {
				return note{}, fmt.Errorf("managed field %s is missing from %s", field, path)
			}
			// Optional lifecycle bookkeeping is inserted without touching the body
			// or requiring edits to existing notes.
			index = 1
			for strings.TrimSpace(lines[index]) != "---" {
				index++
			}
			lines = append(lines[:index], append([]string{""}, lines[index:]...)...)
		}
		lines[index] = field + ": " + value
		if value == "" {
			lines[index] = field + ":"
		}
	}
	content := strings.Join(lines, "\n")
	updated, err := parseNote(content)
	if err != nil {
		return note{}, fmt.Errorf("updated Obsidian task note is invalid: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return note{}, err
	}

	if updated.State == "open" && settings.IsArchivedNote(path) {
		path, err = s.restoreNote(root, current)
		if err != nil {
			return note{}, err
		}
		current.Path = path
	}
	if err := atomicWrite(path, []byte(content), info.Mode().Perm()); err != nil {
		return note{}, err
	}
	updated.Path = current.Path
	return updated, nil
}

func (s Source) noteForRef(ref protocol.SourceRef) (note, error) {
	if ref.Source != "obsidian" || ref.Kind != "task" || ref.Authority != protocol.SourceRefAuthorityPrimary {
		return note{}, fmt.Errorf("source ref %q is not an Obsidian-authored task", ref.ID)
	}
	vault, err := s.configuredVault()
	if err != nil {
		return note{}, err
	}
	path := strings.TrimSpace(ref.Metadata["note_path"])
	if path == "" {
		return note{}, fmt.Errorf("Obsidian task ref %q has no note path", ref.ID)
	}
	if !validManagedNotePath(vault, path) {
		return note{}, fmt.Errorf("Obsidian task note is outside the managed task root: %s", path)
	}
	if info, err := os.Lstat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return note{}, fmt.Errorf("task note parent must be a real directory: %s", path)
	}
	current, err := readNote(path)
	if err != nil {
		return note{}, fmt.Errorf("validate Obsidian task note %s: %w", path, err)
	}
	if !settings.IsArchivedNote(path) && !strings.HasSuffix(filepath.Base(filepath.Dir(path)), "--"+shortID(current.ID)) {
		return note{}, fmt.Errorf("Obsidian task note is outside its stable task directory: %s", path)
	}
	if ref.ID != "obsidian:task:"+current.ID || (ref.Metadata["radar_id"] != "" && ref.Metadata["radar_id"] != current.ID) {
		return note{}, fmt.Errorf("Obsidian task identity changed at %s", path)
	}
	return current, nil
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
	return len(parts) == 2 && parts[0] != "" && parts[0] != "." && parts[0] != ".." && parts[1] != "" && strings.EqualFold(filepath.Ext(parts[1]), ".md")
}

func taskDirectoryName(title, id string) string { return title + "--" + shortID(id) }

func shortID(id string) string {
	id = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "-", "")
	if len(id) < 8 {
		return id
	}
	return id[:8]
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
var _ integration.WorkspaceSeedProvider = Source{}
