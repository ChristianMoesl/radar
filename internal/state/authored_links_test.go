package state

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/taskdb"
)

func authoredLinkRef(id string, keys ...string) protocol.SourceRef {
	return protocol.SourceRef{ID: "obsidian:task:" + id, Source: "obsidian", Kind: "task", Authored: true,
		Role: protocol.SourceRefRoleAuthoritative, Authority: protocol.SourceRefAuthorityPrimary,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, CanonicalKey: "obsidian:task:" + id,
		LinkingKeys: keys, Signal: "done", AgentReview: true}
}

func linkPartitions(state persistedState) []string {
	var result []string
	for _, record := range state.Records {
		ids := sourceRefIDsForRecord(record.ID, state.SourceRefs)
		slices.Sort(ids)
		result = append(result, strings.Join(ids, ","))
	}
	slices.Sort(result)
	return result
}

func eachTaskOrder(tasks []protocol.Task, run func([]protocol.Task)) {
	var permute func(int)
	permute = func(i int) {
		if i == len(tasks) {
			run(tasks)
			return
		}
		for j := i; j < len(tasks); j++ {
			tasks[i], tasks[j] = tasks[j], tasks[i]
			permute(i + 1)
			tasks[i], tasks[j] = tasks[j], tasks[i]
		}
	}
	permute(0)
}

func TestAuthoredLinkConflictsAreIndependentOfSourceOrder(t *testing.T) {
	for _, key := range []string{"mark:ABC-7", "branch:acme/app:shared", "workspace:/work/shared"} {
		t.Run(key, func(t *testing.T) {
			a, b := authoredLinkRef("one", key), authoredLinkRef("two", key)
			remote := testJiraIssueRef("jira:issue:ABC-7", "Shared issue")
			remote.LinkingKeys = []string{key}
			var want []string
			eachTaskOrder([]protocol.Task{makeTask("done", "One", a), makeTask("done", "Two", b), makeTask("in_progress", "Shared", remote)}, func(tasks []protocol.Task) {
				got := reconcileState(persistedState{}, tasks, time.Now().UTC())
				if err := validateAuthoredLinks(got); err != nil || len(got.Records) != 3 {
					t.Fatalf("ambiguous contributor was assigned: %v %+v", err, got.Records)
				}
				partitions := linkPartitions(got)
				if want == nil {
					want = partitions
				} else if !reflect.DeepEqual(want, partitions) {
					t.Fatalf("order changed ownership: %v != %v", partitions, want)
				}
			})
		})
	}
}

func TestAuthoredTransitiveBridgeAndExistingGroupedContributors(t *testing.T) {
	a, b := authoredLinkRef("one", "left"), authoredLinkRef("two", "right")
	x, y := testJiraIssueRef("jira:issue:ABC-1", "Left"), testJiraIssueRef("jira:issue:ABC-2", "Right")
	x.LinkingKeys, y.LinkingKeys = []string{"left", "bridge"}, []string{"bridge", "right"}
	tasks := []protocol.Task{makeTask("done", "One", a), makeTask("done", "Two", b), makeTask("in_progress", "Left", x), makeTask("in_progress", "Right", y)}
	eachTaskOrder(tasks, func(observed []protocol.Task) {
		got := reconcileState(persistedState{}, observed, time.Now().UTC())
		if err := validateAuthoredLinks(got); err != nil || len(got.Records) != 4 {
			t.Fatalf("transitive bridge crossed note identity: %+v (%v)", linkPartitions(got), err)
		}
	})
	// A prior neutral record can itself bridge refs which no longer share a key.
	prior := reconcileState(persistedState{}, tasks[2:], time.Now().UTC())
	if len(prior.Records) != 1 {
		t.Fatal("neutral bridge fixture did not group")
	}
	x.LinkingKeys, y.LinkingKeys = []string{"left"}, []string{"right"}
	fresh := []protocol.Task{tasks[0], tasks[1], makeTask("in_progress", "Left", x), makeTask("in_progress", "Right", y)}
	eachTaskOrder(fresh, func(observed []protocol.Task) {
		data, _ := json.Marshal(prior)
		var copy persistedState
		_ = json.Unmarshal(data, &copy)
		got := reconcileState(copy, observed, time.Now().UTC())
		if err := validateAuthoredLinks(got); err != nil || len(got.Records) != 3 {
			t.Fatalf("existing group attached to an arbitrary note: %+v (%v)", linkPartitions(got), err)
		}
	})
}

func TestAuthoredIdentityDedupAndSingleNoteLinking(t *testing.T) {
	note := authoredLinkRef("one", "mark:ABC-7")
	remote := testJiraIssueRef("jira:issue:ABC-7", "Issue")
	remote.LinkingKeys = []string{"mark:ABC-7"}
	eachTaskOrder([]protocol.Task{makeTask("done", "One", note), makeTask("done", "Repeated", note), makeTask("in_progress", "Issue", remote)}, func(tasks []protocol.Task) {
		got := reconcileState(persistedState{}, tasks, time.Now().UTC())
		if len(got.Records) != 1 || len(got.SourceRefs) != 2 || validateAuthoredLinks(got) != nil {
			t.Fatalf("single-identity linking failed: %+v", got)
		}
	})
}

func TestAssignmentAndRecordMergeRetainAuthoredBoundaryAndBindingOwner(t *testing.T) {
	a, b := authoredLinkRef("one", "shared"), authoredLinkRef("two", "shared")
	remote := testJiraIssueRef("jira:issue:ABC-7", "Issue")
	remote.LinkingKeys = []string{"shared"}
	a.Bindings = []protocol.SourceBinding{remote.Binding()}
	prior := reconcileState(persistedState{}, []protocol.Task{makeTask("done", "One", a), makeTask("in_progress", "Issue", remote)}, time.Now().UTC())
	id := prior.Records[0].NumericID
	prior.Records[0].Ack.Cursor = "2026-10-01T00:00:00Z"
	prior.Records[0].AgentReviewPending = true
	wrongTarget := makeTask("done", "Two", b)
	wrongTarget.TargetTaskID = id
	got := reconcileStateForSources(prior, []protocol.Task{wrongTarget}, time.Now().UTC(), map[string]bool{"obsidian": true})
	if len(got.Records) != 2 || validateAuthoredLinks(got) != nil {
		t.Fatal("targeted assignment crossed authored boundary")
	}
	for _, record := range got.Records {
		if record.NumericID == id && (record.Ack.Cursor != "2026-10-01T00:00:00Z" || !record.AgentReviewPending) {
			t.Fatal("existing history changed")
		}
	}
	for _, ref := range got.SourceRefs {
		if ref.ID == remote.ID && ref.TaskRecordID != prior.Records[0].ID {
			t.Fatal("shared association transferred its owner")
		}
		if ref.ID == a.ID && !reflect.DeepEqual(ref.Snapshot.Bindings, a.Bindings) {
			t.Fatal("authored binding intent changed")
		}
	}
	before, _ := json.Marshal(got)
	got = mergeRelatedRecords(got, []string{got.Records[0].ID, got.Records[1].ID})
	after, _ := json.Marshal(got)
	if !bytes.Equal(before, after) {
		t.Fatal("record merge bypassed authored boundary")
	}
}

func TestInvalidAuthoredProjectionFailsClosedWithoutLosingRepairEvidence(t *testing.T) {
	a, b := authoredLinkRef("one"), authoredLinkRef("two")
	invalid := persistedState{Version: stateVersion, NextTaskID: 7, Records: []TaskRecord{{
		ID: "task:7", NumericID: 7, CanonicalKey: a.ID, Ack: TaskAckState{Cursor: "saved"}, AgentReviewPending: true,
		Snapshot: protocol.Task{SourceRefs: []protocol.SourceRef{a, b}},
	}}, SourceRefs: []SourceRefRecord{{ID: a.ID, TaskRecordID: "task:7", Active: true, Snapshot: a}, {ID: b.ID, TaskRecordID: "task:7", Active: false, Snapshot: b}}}
	data, _ := json.Marshal(invalid)
	conflicts, err := InspectLinkConflicts(data)
	if err != nil || len(conflicts) != 1 || len(conflicts[0].AuthoredIDs) != 2 || len(conflicts[0].Decisions) != 3 {
		t.Fatalf("missing actionable repair plan: %+v %v", conflicts, err)
	}
	got := reconcileState(invalid, []protocol.Task{makeTask("done", "One", a)}, time.Now().UTC())
	unchanged, _ := json.Marshal(got)
	if !bytes.Equal(data, unchanged) {
		t.Fatal("invalid aggregate was silently rewritten")
	}
	path := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("RADAR_STATE", path)
	if err := taskdb.WriteProjection(path, data); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "state-export") || !strings.Contains(err.Error(), "task:7") {
		t.Fatalf("missing fail-closed repair diagnostic: %v", err)
	}
	retained, err := taskdb.ReadProjection(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded persistedState
	_ = json.Unmarshal(retained, &loaded)
	if !reflect.DeepEqual(loaded, invalid) {
		t.Fatal("startup lost retained identity, history or review state")
	}
}

func TestSharedContributorRefreshCannotTransferEstablishedAuthoredOwnership(t *testing.T) {
	a, b := authoredLinkRef("one", "left"), authoredLinkRef("two", "right")
	x, y := testJiraIssueRef("jira:issue:ABC-1", "Left"), testJiraIssueRef("jira:issue:ABC-2", "Right")
	x.LinkingKeys, y.LinkingKeys = []string{"left"}, []string{"right"}
	a.Bindings, b.Bindings = []protocol.SourceBinding{x.Binding()}, []protocol.SourceBinding{y.Binding()}
	prior := reconcileState(persistedState{}, []protocol.Task{makeTask("done", "One", a), makeTask("done", "Two", b), makeTask("in_progress", "Left", x), makeTask("in_progress", "Right", y)}, time.Now().UTC())
	if len(prior.Records) != 2 {
		t.Fatal("ownership fixture failed")
	}
	want := linkPartitions(prior)
	x.LinkingKeys, y.LinkingKeys = []string{"mark:ABC-7"}, []string{"mark:ABC-7"}
	tasks := []protocol.Task{makeTask("in_progress", "Left", x), makeTask("in_progress", "Right", y)}
	// Even a fresh target suggestion cannot overwrite stable source ownership.
	tasks[0].TargetTaskID = prior.Records[1].NumericID
	eachTaskOrder(tasks, func(observed []protocol.Task) {
		data, _ := json.Marshal(prior)
		var copy persistedState
		_ = json.Unmarshal(data, &copy)
		got := reconcileStateForSources(copy, observed, time.Now().UTC(), map[string]bool{"jira": true})
		if validateAuthoredLinks(got) != nil || !reflect.DeepEqual(want, linkPartitions(got)) {
			t.Fatalf("shared canonical key transferred contributor ownership: %v != %v", linkPartitions(got), want)
		}
	})
}

func TestHistoricalBranchesCannotChooseAnArbitraryAuthoredOwner(t *testing.T) {
	a, b := authoredLinkRef("one"), authoredLinkRef("two")
	prior := persistedState{Version: stateVersion, NextTaskID: 3, Records: []TaskRecord{
		{ID: "task:1", NumericID: 1, Snapshot: protocol.Task{SourceRefs: []protocol.SourceRef{a}}},
		{ID: "task:2", NumericID: 2, Snapshot: protocol.Task{SourceRefs: []protocol.SourceRef{b}}},
		{ID: "task:3", NumericID: 3},
	}}
	add := func(record string, active bool, ref protocol.SourceRef) {
		prior.SourceRefs = append(prior.SourceRefs, SourceRefRecord{ID: ref.ID, TaskRecordID: record, Active: active, Snapshot: ref})
	}
	for i, note := range []protocol.SourceRef{a, b} {
		record := prior.Records[i].ID
		add(record, true, note)
		current := testGitWorktreeRef("git:current:"+note.ID, "/work/"+note.ID, "acme/app", "current-"+note.ID)
		add(record, true, current)
		historical := current
		historical.ID += ":old"
		historical.LinkingKeys = []string{"branch:" + note.ID}
		add(record, false, historical)
	}
	remote := testJiraIssueRef("jira:issue:ABC-7", "Bridge")
	remote.LinkingKeys = []string{"branch:" + a.ID, "branch:" + b.ID}
	add("task:3", true, remote)
	want := linkPartitions(prior)
	got := relinkState(prior)
	if len(got.Records) != 3 || !reflect.DeepEqual(want, linkPartitions(got)) {
		t.Fatalf("historical bridge chose a note owner: %v", linkPartitions(got))
	}
}
