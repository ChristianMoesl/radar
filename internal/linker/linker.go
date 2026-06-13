package linker

import "radar.nvim/internal/protocol"

type Input struct {
	Tasks      []protocol.Task
	SourceRefs []protocol.SourceRef
}

func Link(input Input) []protocol.Task {
	items := cloneTasks(input.Tasks)
	items = linkSourceRefsByTicket(items, input.SourceRefs)
	items = linkSourceRefsByPath(items, input.SourceRefs)
	items = append(items, standaloneSourceRefTasks(items, input.SourceRefs)...)
	return items
}

func cloneTasks(items []protocol.Task) []protocol.Task {
	cloned := make([]protocol.Task, len(items))
	copy(cloned, items)
	return cloned
}

func appendSourceRef(sourceRefs []protocol.SourceRef, sourceRef protocol.SourceRef) []protocol.SourceRef {
	for _, existing := range sourceRefs {
		if existing.ID == sourceRef.ID {
			return sourceRefs
		}
	}
	return append(sourceRefs, sourceRef)
}
