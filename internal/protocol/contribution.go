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
		signal, reason := ref.Signal, ref.Status
		if signal == "" {
			// A resource with no urgency still represents visible local work.
			// Never inherit a different source's cached attention signal.
			signal = "low_priority"
		}
		if !done {
			if ack := ref.Acknowledgement; ack != nil && task.AcknowledgementCursor != "" &&
				!ack.Blocking && (ack.Cursor == "" || ack.Cursor <= task.AcknowledgementCursor) {
				if ack.FallbackSignal != "" {
					signal, reason = ack.FallbackSignal, ack.FallbackReason
				} else if ack.HideWhenAcknowledged {
					continue
				}
			}
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
