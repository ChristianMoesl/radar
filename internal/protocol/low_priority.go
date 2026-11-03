package protocol

import "slices"

// CaptureLowPriorityBaseline records only currently known source occurrences.
// It does not acknowledge anything or change the source's raw signal. Providers
// supply exact action identities when a source can have successive requests.
func CaptureLowPriorityBaseline(task Task) map[string][]string {
	baseline := map[string][]string{}
	if priority, previous := priorityPreference(task); priority == "low" {
		// A missing/quiet source is not proof that a previously deferred
		// occurrence became new. Keep that knowledge until Normal/Urgent.
		for id, keys := range previous {
			baseline[id] = slices.Clone(keys)
		}
	}
	for _, ref := range task.SourceRefs {
		if ref.Role != SourceRefRoleAuthoritative || ref.ID == "" {
			continue
		}
		if _, known := baseline[ref.ID]; !known {
			baseline[ref.ID] = nil
		}
		signal, _, visible := lowPriorityContribution(task, ref)
		if visible && contributionRank(signal) >= contributionRank("attention") {
			keys := append(baseline[ref.ID], actionableSignalKeys(task, ref)...)
			slices.Sort(keys)
			baseline[ref.ID] = slices.Compact(keys)
		}
	}
	return baseline
}

func actionableSignalKeys(task Task, ref SourceRef) []string {
	var keys []string
	// An author's reserved next-handoff identity is not an outstanding review
	// merely because the user selected Urgent. Clearing urgency with Low must
	// not pre-defer a later result. Pending and saved holds share the real key.
	if !ref.Authored || ref.AgentReview || task.AgentReviewPending {
		for _, key := range ref.ActionableSignals {
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	if len(keys) == 0 {
		if ack := ref.Acknowledgement; ack != nil && ack.Cursor != "" {
			keys = append(keys, "ack:"+ack.Cursor)
		} else if ref.ID != "" && (ref.Signal == "attention" || ref.Signal == "immediate") {
			// A previously unknown source identity is itself new evidence. This
			// fallback cannot distinguish a later request on the same source;
			// those providers must publish explicit occurrence identities.
			keys = append(keys, "source:"+ref.ID)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func priorityPreference(task Task) (string, map[string][]string) {
	for _, ref := range task.SourceRefs {
		if ref.Role == SourceRefRoleAuthoritative && ref.Authored && ref.Authority == SourceRefAuthorityPrimary {
			return ref.Metadata["priority"], ref.LowPriorityBaseline
		}
	}
	return "", nil
}

func newActionableSignal(task Task, ref SourceRef, baseline map[string][]string) bool {
	for _, key := range actionableSignalKeys(task, ref) {
		if !slices.Contains(baseline[ref.ID], key) {
			return true
		}
	}
	return false
}

// LowPriorityActionOutstanding identifies last-known action evidence worth
// retaining when collection is incomplete. It does not bypass source filters,
// grant freshness/completeness, or authorize lifecycle/resource operations.
func LowPriorityActionOutstanding(task Task, ref SourceRef) bool {
	priority, baseline := priorityPreference(task)
	if priority != "low" || task.Attention == "done" || ref.Role != SourceRefRoleAuthoritative || ref.Presentation.Hidden {
		return false
	}
	signal, _, visible := lowPriorityContribution(task, ref)
	return visible && contributionRank(signal) >= contributionRank("attention") && newActionableSignal(task, ref, baseline)
}

// Waiting is normally a badge/veto, not a category change. Only the explicit
// Low policy promotes a new provider-owned prompt. Busy/idle alone never do.
func lowPriorityContribution(task Task, ref SourceRef) (signal, reason string, visible bool) {
	signal, reason, visible = effectiveContribution(task, ref)
	if visible && task.Attention != "done" && ref.Activity == ActivityWaiting && len(ref.ActionableSignals) > 0 && contributionRank(signal) < contributionRank("attention") {
		signal, reason = "attention", "Agent waiting for interaction"
	}
	return
}
