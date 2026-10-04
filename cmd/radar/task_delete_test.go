package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestTaskDeleteRequiresExplicitConfirmationAndReturnsDeletionResult(t *testing.T) {
	preview := protocol.TaskDeletionPreview{TaskID: 7, TaskTitle: "Write release notes", SourceRefID: "obsidian:task:one", Path: "/vault/Tasks/Write release notes--12345678", TrashDirectory: "/vault/.trash", Description: "Move the entire private task directory, including accompanying files, to recoverable vault trash.", Revision: "confirmed"}
	result := protocol.TaskDeletionResult{TaskID: 7, SourceRefID: preview.SourceRefID, OriginalPath: preview.Path, TrashPath: "/vault/.trash/radar-123/task"}
	for _, answer := range []string{"", "\n", "n\n", "yes\n", "y", "y\n", "Y\n"} {
		t.Run(strings.ReplaceAll(answer, "\n", "newline"), func(t *testing.T) {
			var output bytes.Buffer
			var requests []protocol.Request
			call := func(request protocol.Request) protocol.Response {
				requests = append(requests, request)
				if request.Method == "task-delete-preview" {
					return protocol.Response{OK: true, TaskDeletionPreview: &preview}
				}
				return protocol.Response{OK: true, TaskDeletionResult: &result}
			}
			got, err := deleteTask(7, strings.NewReader(answer), &output, call)
			if err != nil {
				t.Fatal(err)
			}
			confirmed := answer == "y\n" || answer == "Y\n"
			if confirmed {
				if !reflect.DeepEqual(got, &result) || len(requests) != 2 || requests[1].Method != "task-delete" || !reflect.DeepEqual(requests[1].TaskDeletion, &preview) {
					t.Fatalf("did not submit exact confirmed preview: got=%+v requests=%+v", got, requests)
				}
			} else if got != nil || len(requests) != 1 || !strings.Contains(output.String(), "cancelled") {
				t.Fatal("cancel or EOF submitted deletion")
			}
			if requests[0].Method != "task-delete-preview" || requests[0].TaskID != 7 {
				t.Fatalf("wrong preview request: %+v", requests[0])
			}
			for _, want := range []string{preview.TaskTitle, preview.Path, preview.TrashDirectory, "accompanying files", "Remote items and local resources stay unchanged", "may remain visible", "[y/N]"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("confirmation missing %q: %s", want, output.String())
				}
			}
		})
	}
}

func TestTaskDeleteFailsOnMissingOrRejectedResponses(t *testing.T) {
	for _, stage := range []string{"preview missing", "preview rejected", "delete missing", "delete rejected"} {
		t.Run(stage, func(t *testing.T) {
			calls := 0
			call := func(request protocol.Request) protocol.Response {
				calls++
				if strings.HasPrefix(stage, "delete") && request.Method == "task-delete-preview" {
					return protocol.Response{OK: true, TaskDeletionPreview: &protocol.TaskDeletionPreview{TaskID: 7}}
				}
				return protocol.Response{OK: strings.HasSuffix(stage, "missing"), Error: "workspace must be cleaned up first"}
			}
			var output bytes.Buffer
			if result, err := deleteTask(7, strings.NewReader("y\n"), &output, call); err == nil || result != nil {
				t.Fatalf("invalid response succeeded: %+v, %v", result, err)
			}
			if strings.HasPrefix(stage, "preview") && calls != 1 {
				t.Fatal("submitted deletion after preview failure")
			}
		})
	}
}
