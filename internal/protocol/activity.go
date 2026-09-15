package protocol

import "fmt"

// Activity is transient runtime state, independent of attention and lifecycle.
// The zero value is idle, so refs without an activity producer contribute nothing.
type Activity string

const (
	ActivityIdle    Activity = ""
	ActivityBusy    Activity = "busy"
	ActivityWaiting Activity = "waiting"
)

func ParseActivity(value string) (Activity, error) {
	switch value {
	case "idle":
		return ActivityIdle, nil
	case "busy":
		return ActivityBusy, nil
	case "waiting":
		return ActivityWaiting, nil
	default:
		return ActivityIdle, fmt.Errorf("invalid activity %q: expected idle, busy, or waiting", value)
	}
}

func (a Activity) Valid() bool {
	return a == ActivityIdle || a == ActivityBusy || a == ActivityWaiting
}

func (a Activity) String() string {
	if a == ActivityIdle {
		return "idle"
	}
	return string(a)
}

// MergeActivity reduces current facts, not historical transitions. Waiting wins
// over processing so one busy producer cannot hide another's pending prompt.
func MergeActivity(left, right Activity) Activity {
	if left == ActivityWaiting || right == ActivityWaiting {
		return ActivityWaiting
	}
	if left == ActivityBusy || right == ActivityBusy {
		return ActivityBusy
	}
	return ActivityIdle
}
