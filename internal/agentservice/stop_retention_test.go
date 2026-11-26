package agentservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeStopPlanRetentionIsImmutablePrivateAndConservative(t *testing.T) {
	var zero StopPlan
	if !zero.ExpiresAt().IsZero() || zero.RetainedBytes() != 0 {
		t.Fatal("zero capture supplied retention evidence")
	}
	reg, manager := stopPlanFixture(t)
	reg.Entrypoint = strings.Repeat("private", 2048)
	reg.Collection = "github"
	dir, _ := instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p, err := manager.PrepareStop(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
	if err != nil {
		t.Fatal(err)
	}
	deadline, bytes := p.ExpiresAt(), p.RetainedBytes()
	raw, _ := json.Marshal(reg)
	if !deadline.After(start) || deadline.After(time.Now().Add(30*time.Second)) || bytes < 64*1024+4*len(reg.Entrypoint) || bytes < len(raw) {
		t.Fatal("native retention undercounted private fields or extended expiry", bytes)
	}
	encoded, _ := json.Marshal(p)
	if string(encoded) != "{}" {
		t.Fatal("private retention metadata serialized")
	}
	if err := p.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.ExpiresAt().Equal(deadline) || p.RetainedBytes() != bytes {
		t.Fatal("execution renewed retention metadata")
	}
	if _, err := os.Stat(filepath.Join(dir, "registration.json")); err != nil {
		t.Fatal("retention/execution removed registration")
	}
}
