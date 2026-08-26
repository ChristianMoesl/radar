package protocol

import "testing"

func TestSummarizeTasksIncludesLowPriority(t *testing.T) {
	summary := SummarizeTasks([]Task{
		{Attention: "attention"},
		{Attention: "low_priority"},
	})
	if summary.Attention != 1 || summary.LowPriority != 1 {
		t.Fatalf("unexpected summary %#v", summary)
	}
}
