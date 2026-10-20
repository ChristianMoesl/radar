package obsidian

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
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
	"radar/internal/integration/obsidian/settings"
	"radar/internal/integration/workspace/group"
	"radar/internal/linking"
	"radar/internal/openurl"
	"radar/internal/protocol"
	"radar/internal/taskdb"
)

const OpenAction = "obsidian_open"

var validID = regexp.MustCompile(`^(?:[0-9A-HJKMNP-TV-Z]{26}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})$`)

type Source struct {
	vaultPath string
}

type note struct {
	taskdb.Task
	content        string
	fields         map[string]fieldRange
	frontmatterEnd int
	newline        string
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
	var vault string
	var err error
	if strings.TrimSpace(s.vaultPath) != "" {
		vault, err = config.ObsidianConfig{VaultPath: s.vaultPath}.ValidateAndPrepare()
	} else {
		cfg, e := config.Load()
		if e != nil {
			return "", e
		}
		vault, err = cfg.Obsidian.ValidateAndPrepare()
	}
	if err != nil {
		return "", err
	}
	return vault, nil
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

	validateNoteOwnership(discovered)

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
			if item.note.ID != "" {
				known := false
				for _, task := range req.Previous {
					for _, ref := range task.SourceRefs {
						if ref.ID == "obsidian:task:"+item.note.ID {
							known = true
						}
					}
				}
				if !known {
					valid = append(valid, item.note)
				}
			}
			continue
		}
		valid = append(valid, item.note)
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].ID < valid[j].ID })
	observations := make([]integration.Observation, 0, len(valid))
	for _, current := range valid {
		items := observationsFor(vault, current)
		if invalidIDs[current.ID] {
			items[0].Ref.Metadata["note_error"] = "note unavailable; repair its association before mutation or cleanup"
		}
		observations = append(observations, items...)
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
	tasks, err := taskdb.List(vault)
	if err != nil {
		return nil, err
	}
	items := make([]discoveredNote, 0, len(tasks))
	for _, task := range tasks {
		current, err := readTaskNote(task)
		if task.Trashed && os.IsNotExist(err) {
			continue
		}
		items = append(items, discoveredNote{note: current, err: err, path: current.Path})
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
	if _, err := os.Lstat(path); err != nil {
		return note{Task: taskdb.Task{Path: path}}, err
	}
	db, err := taskdb.Current()
	if err != nil {
		return note{}, err
	}
	defer db.Close()
	var data []byte
	var revision int64
	err = db.QueryRow("SELECT data,revision FROM authored_tasks WHERE path=? AND trashed=0", path).Scan(&data, &revision)
	if err != nil {
		return note{}, fmt.Errorf("note is not associated with a task in SQLite: %s: %w", path, err)
	}
	var task taskdb.Task
	if err := json.Unmarshal(data, &task); err != nil {
		return note{}, err
	}
	task.Revision = revision
	return readTaskNote(task)
}

func readTaskNote(task taskdb.Task) (note, error) {
	current := note{Task: task, fields: map[string]fieldRange{}, newline: "\n"}
	path := task.Path
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !settings.IsArchivedNote(path) && !task.Trashed {
		entries, e := os.ReadDir(filepath.Dir(path))
		if e == nil {
			var names []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
					names = append(names, entry.Name())
				}
			}
			if len(names) == 1 {
				path = filepath.Join(filepath.Dir(path), names[0])
				info, err = os.Lstat(path)
			}
		}
	}
	if err != nil {
		return current, err
	}
	if !info.Mode().IsRegular() {
		return current, fmt.Errorf("task note must be a regular file")
	}
	if parent, e := os.Lstat(filepath.Dir(path)); e != nil || !parent.IsDir() {
		return current, fmt.Errorf("task note parent must be a real directory")
	}
	if !settings.IsArchivedNote(path) {
		entries, e := os.ReadDir(filepath.Dir(path))
		if e != nil {
			return current, e
		}
		count := 0
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				count++
			}
		}
		if count != 1 {
			return current, fmt.Errorf("task directory must contain exactly one Markdown note")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return current, err
	}
	if hasLegacyFields(string(data)) {
		return current, fmt.Errorf("legacy Radar frontmatter requires explicit state migration: %s", path)
	}
	current.content = string(data)
	if path != task.Path || task.Trashed {
		current.Path = path
		current.Trashed = false
		if err := taskdb.Put(&current.Task); err != nil {
			return current, err
		}
	}
	return current, nil
}

func noteVersion(current note) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d\n%s", current.Revision, current.content))))
}

func observationsFor(vault string, current note) []integration.Observation {
	identity := "obsidian:task:" + current.ID
	keys := []string{identity}
	for _, binding := range current.Bindings {
		keys = append(keys, binding.LinkingKey())
	}
	uri := noteURI(vault, current.Path)
	metadata := map[string]string{
		"radar_id": current.ID, "note_path": current.Path, "task_directory": filepath.Dir(current.Path),
		"state": current.State, "priority": current.Priority, "created_at": current.CreatedAt,
		"completed_at": current.CompletedAt,
		"content_hash": noteVersion(current),
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
			Status: current.State, CanonicalKey: identity, LinkingKeys: linking.Keys(keys...), Metadata: metadata,
			WorkspaceAnchorPath: current.Path, Authored: true, Muted: current.Muted, Bindings: current.Bindings, AgentReview: current.AgentReview,
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
	updates := map[string]string{"state": state, "completed_at": "", "completion_baseline": "pending"}
	if state == "done" {
		updates["completed_at"] = "__now_if_empty__"
	}
	// Only explicit Done dismisses the handoff; reopening and priority changes
	// do not implicitly acknowledge an agent's result.
	_, err := s.mutateNote(ref, func(current note) (map[string]string, error) {
		if state == "done" && current.AgentReview {
			updates["agent_review"] = "false"
		}
		return updates, nil
	})
	return integration.AuthoredTaskIdentity{SourceRefID: ref.ID}, err
}

func (s Source) SetPriority(_ context.Context, ref protocol.SourceRef, priority string) (integration.AuthoredTaskIdentity, error) {
	if priority != "normal" && priority != "urgent" {
		return integration.AuthoredTaskIdentity{}, fmt.Errorf("unsupported Obsidian priority %q", priority)
	}
	return s.mutate(ref, map[string]string{"priority": priority})
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
	if updates["completed_at"] == "__now_if_empty__" {
		updates["completed_at"] = current.CompletedAt
		if updates["completed_at"] == "" {
			updates["completed_at"] = time.Now().UTC().Format(time.RFC3339)
		}
	}
	updated, err := applyStateUpdates(current, updates)
	if err != nil {
		return note{}, err
	}
	if updated.State == "open" && settings.IsArchivedNote(path) {
		restored, e := s.restoreNote(root, current)
		err = e
		path = restored.Path
		updated.Revision = restored.Revision
		if err != nil {
			return note{}, err
		}
		updated.Path = path
	}
	if err := taskdb.Put(&updated.Task); err != nil {
		return note{}, err
	}

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
	return []integration.Action{{PreferredKey: "o", Source: "Obsidian", Label: req.Label, Detail: "Open task note", ID: OpenAction, Ref: req.Ref}}
}

func (Source) RunAction(ctx context.Context, req integration.RunActionRequest) (integration.ActionResult, error) {
	if req.ActionID != OpenAction || req.Ref.URL == "" {
		return integration.ActionResult{}, fmt.Errorf("unknown Obsidian action: %s", req.ActionID)
	}
	if err := openurl.Open(ctx, req.Ref.URL); err != nil {
		return integration.ActionResult{}, err
	}
	return integration.ActionResult{Message: "Opened task note"}, nil
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
	// Ordinary directories use the system's Markdown handler, while a real vault
	// retains its Obsidian deep links. Notes and their stable identities are identical.
	if info, err := os.Stat(filepath.Join(vault, ".obsidian")); err != nil || !info.IsDir() {
		return (&url.URL{Scheme: "file", Path: notePath}).String()
	}
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
