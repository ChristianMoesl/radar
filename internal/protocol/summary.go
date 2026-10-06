package protocol

func SummarizeTasks(tasks []Task) Summary {
	var summary Summary
	for _, task := range tasks {
		switch task.DisplayGroup() {
		case "ignored":
			summary.Ignored++
		case "immediate":
			summary.Immediate++
		case "attention":
			summary.Attention++
		case "in_progress":
			summary.InProgress++
		case "done":
			summary.Done++
		case "low_priority":
			summary.LowPriority++
		}
	}
	return summary
}
