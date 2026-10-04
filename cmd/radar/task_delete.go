package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"radar/internal/protocol"
)

func deleteTask(taskID int, input io.Reader, output io.Writer, call func(protocol.Request) protocol.Response) (*protocol.TaskDeletionResult, error) {
	response := call(protocol.Request{Method: "task-delete-preview", TaskID: taskID})
	if !response.OK {
		return nil, fmt.Errorf("%s", response.Error)
	}
	preview := response.TaskDeletionPreview
	if preview == nil {
		return nil, fmt.Errorf("task deletion preview response was empty")
	}
	fmt.Fprintf(output, "Delete authored task %q?\n%s\nMove: %s\nTrash: %s\nRemote items and local resources stay unchanged; linked work may remain visible.\nDelete task? [y/N] ", preview.TaskTitle, preview.Description, preview.Path, preview.TrashDirectory)
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil || strings.ToLower(strings.TrimSpace(answer)) != "y" {
		fmt.Fprintln(output, "Task deletion cancelled")
		return nil, nil
	}
	response = call(protocol.Request{Method: "task-delete", TaskDeletion: preview})
	if !response.OK {
		return nil, fmt.Errorf("%s", response.Error)
	}
	if response.TaskDeletionResult == nil {
		return nil, fmt.Errorf("task deletion response was empty")
	}
	return response.TaskDeletionResult, nil
}
