package linker

import (
	"regexp"
	"strings"

	"radar.nvim/internal/protocol"
)

var ticketPattern = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]+-[0-9]+`)

func linkSourceRefsByTicket(items []protocol.Task, sourceRefs []protocol.SourceRef) []protocol.Task {
	for i := range items {
		itemKeys := ticketKeysForTask(items[i])
		for _, sourceRef := range sourceRefs {
			if matchesAnyString(itemKeys, ticketKeysForSourceRef(sourceRef)) {
				items[i].SourceRefs = appendSourceRef(items[i].SourceRefs, sourceRef)
			}
		}
	}
	return items
}

func ticketKeysForTask(task protocol.Task) []string {
	values := []string{task.Title, task.Repo, task.URL}
	for _, sourceRef := range task.SourceRefs {
		values = append(values, valuesForSourceRef(sourceRef)...)
	}
	return extractTicketKeys(values...)
}

func ticketKeysForSourceRef(sourceRef protocol.SourceRef) []string {
	return extractTicketKeys(valuesForSourceRef(sourceRef)...)
}

func valuesForSourceRef(sourceRef protocol.SourceRef) []string {
	values := []string{sourceRef.ID, sourceRef.Title, sourceRef.Branch, sourceRef.Path, sourceRef.Repo, sourceRef.URL}
	for _, value := range sourceRef.Metadata {
		values = append(values, value)
	}
	return values
}

func extractTicketKeys(values ...string) []string {
	keys := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		for _, match := range ticketPattern.FindAllString(value, -1) {
			key := strings.ToUpper(match)
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func ticketKeysMatch(left, right protocol.SourceRef) bool {
	return matchesAnyString(ticketKeysForSourceRef(left), ticketKeysForSourceRef(right))
}
