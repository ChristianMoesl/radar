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

// LinkingEvidence reports production identity protection, never repair intent.
type LinkingEvidence struct {
	AuthoredIdentities int
	ConnectedRecords   int
	Blocked            bool
}

// LinkingInputs uses production's connected components and the same authored
// identity guard used before merging. Inactive authored history still owns its
// numeric identity; no note contents or free-form provider reasons are read.
func LinkingInputs(records []TaskRecord, refs []SourceRefRecord) map[string]LinkingEvidence {
	owners := recordAuthoredIdentities(persistedState{Records: records, SourceRefs: refs})
	groups := sourceRefLinkGroups(refs)
	blocked := blockedLinkRecords(owners, groups)
	result := make(map[string]LinkingEvidence, len(records))
	for _, record := range records {
		result[record.ID] = LinkingEvidence{AuthoredIdentities: len(owners[record.ID]), Blocked: blocked[record.ID]}
	}
	for _, group := range groups {
		ids := uniqueTaskRecordIDs(group)
		for _, id := range ids {
			e := result[id]
			e.ConnectedRecords = len(ids)
			result[id] = e
		}
	}
	return result
}
