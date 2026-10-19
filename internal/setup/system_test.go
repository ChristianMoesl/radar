package setup

import (
	"strings"
	"testing"
)

func TestCommandOutputIsBounded(t *testing.T) {
	var b commandOutput
	input := []byte(strings.Repeat("x", (1<<20)+20))
	if n, err := b.Write(input); err != nil || n != len(input) {
		t.Fatal(n, err)
	}
	if b.Len() != 1<<20 || !b.truncated {
		t.Fatal("output not bounded")
	}
	if n, err := b.Write(input); err != nil || n != len(input) || b.Len() != 1<<20 {
		t.Fatal("repeated overflow")
	}
}

// Opt-in host test: creates/removes one disposable SBX sandbox, pulls the public
// image if needed and mounts only private fixture directories. No Radar user
// config, credentials or network policy are modified.
