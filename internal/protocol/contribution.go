package protocol

import "strings"

// ContributionAction controls presentation only. Source signals remain the
// authoritative facts for linking, completion, reopening and cleanup.
type ContributionAction string

const (
	ContributionKeep         ContributionAction = "keep"
	ContributionMute         ContributionAction = "mute"
	ContributionDeprioritize ContributionAction = "deprioritize"
)

type ContributionPolicy func(SourceRef) ContributionAction

// ProjectAttention applies acknowledgement and policy independently to each
// source before choosing the strongest remaining signal. It never changes refs.
// It can be reapplied to a projection because source facts remain untouched.
func ProjectAttention(task Task, policy ContributionPolicy) (Task, bool) {
	if len(task.SourceRefs) == 0 {
		return task, true
	}
	done := task.Attention == "done"
	priority, baseline := priorityPreference(task)
	task.Priority = priority
	visible := false
	bestRank := -1
	bestSignal, bestReason, bestID := "", "", ""
	for _, ref := range task.SourceRefs {
		if ref.Role != SourceRefRoleAuthoritative {
			continue
		}
		action := ContributionKeep
		if policy != nil {
			action = policy(ref)
		}
		if action == ContributionMute || ref.Presentation.Hidden {
			continue
		}
		signal, reason, contributionVisible := effectiveContribution(task, ref)
		if priority == "low" {
			signal, reason, contributionVisible = lowPriorityContribution(task, ref)
		}
		if !contributionVisible {
			continue
		}
		visible = true
		if done {
			continue
		}
		if signal == "done" {
			continue // A completed contributor cannot finish another active source.
		}
		// The aggregate reason may belong to a suppressed contributor, even
		// when it has the same signal. Derive fallback text from this ref only.
		if reason == "" {
			reason = ref.Source + " " + ref.Kind
		}
		if action == ContributionDeprioritize {
			signal = "low_priority"
			if !strings.HasPrefix(reason, "low priority: ") {
				reason = "low priority: " + reason
			}
		}
		// Apply whole-task Low only after acknowledgement and source filtering.
		// An actionable new occurrence can break through without changing the
		// preference. Ordinary activity and already-deferred actions cannot.
		if priority == "low" && signal != "low_priority" &&
			(contributionRank(signal) < contributionRank("attention") || !newActionableSignal(task, ref, baseline)) {
			signal, reason = "low_priority", "Manual low priority"
		}
		rank := contributionRank(signal)
		if rank > bestRank {
			bestRank, bestSignal, bestReason, bestID = rank, signal, reason, ref.ID
		}
	}
	if !visible {
		return task, false
	}
	if !done {
		if bestSignal == "" {
			bestSignal, bestReason = "low_priority", "tracked work"
		}
		task.Attention, task.Reason = bestSignal, bestReason
		task.AttentionSourceRefID = bestID
	}
	return task, true
}

func effectiveContribution(task Task, ref SourceRef) (signal, reason string, visible bool) {
	signal, reason = ref.Signal, ref.Status
	done := task.Attention == "done"
	if !done && ref.Authored && (ref.AgentReview || task.AgentReviewPending) && signal != "immediate" {
		if task.Activity == ActivityIdle {
			signal, reason = "attention", "Agent finished — review result"
		} else {
			signal, reason = "in_progress", "Agent working — completion held for review"
		}
	}
	if signal == "" {
		// A quiet resource never inherits another source's cached signal.
		signal = "low_priority"
	}
	if !done {
		if ack := ref.Acknowledgement; ack != nil && task.AcknowledgementCursor != "" &&
			!ack.Blocking && (ack.Cursor == "" || ack.Cursor <= task.AcknowledgementCursor) {
			if ack.FallbackSignal != "" {
				signal, reason = ack.FallbackSignal, ack.FallbackReason
			} else if ack.HideWhenAcknowledged {
				return "", "", false
			}
		}
	}
	return signal, reason, true
}

func contributionRank(signal string) int {
	switch signal {
	case "immediate":
		return 4
	case "attention":
		return 3
	case "in_progress":
		return 2
	case "low_priority":
		return 1
	default:
		return 0
	}
}
