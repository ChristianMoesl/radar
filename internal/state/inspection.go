package state

// LifecycleInputs selects the very same active authoritative work items used by
// updateRecordLifecycles. It explains inputs, not a second lifecycle decision or
// a reconstruction of historical manual intent.
func LifecycleInputs(record TaskRecord, refs []SourceRefRecord) map[string]bool {
	selected := lifecycleWorkItems(activeSourceRefRecordsForRecord(record.ID, refs))
	ids := make(map[string]bool, len(selected))
	for _, ref := range selected {
		ids[ref.ID] = true
	}
	return ids
}
