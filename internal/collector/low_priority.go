package collector

import (
	"radar/internal/integration"
	"radar/internal/protocol"
)

// Missing/partial collection is not proof that a surfaced Low action resolved.
// Retain only its previous actionable evidence, never completeness or a newly
// fabricated action. Normal filtering and all fresh safety checks still apply.
func retainLowPriorityActions(previous []protocol.Task, source string, result *integration.CollectResult) {
	if result.Complete {
		return
	}
	for _, task := range previous {
		for _, ref := range task.SourceRefs {
			if ref.Source != source || !protocol.LowPriorityActionOutstanding(task, ref) {
				continue
			}
			stillActionable := false
			for _, observation := range result.Observations {
				if observation.Ref.ID == ref.ID {
					fresh := observation.Ref
					fresh.Signal = string(observation.Signal)
					stillActionable = stillActionable || protocol.LowPriorityActionOutstanding(task, fresh)
				}
			}
			if stillActionable {
				continue
			}
			kept := make([]integration.Observation, 0, len(result.Observations)+1)
			for _, observation := range result.Observations {
				if observation.Ref.ID != ref.ID {
					kept = append(kept, observation)
				}
			}
			result.Observations = append(kept, integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: ref.Status})
		}
	}
}
