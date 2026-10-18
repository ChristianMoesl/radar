package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKVMPreflightRequiresDeviceAndAPI(t *testing.T) {
	if err := checkKVMAt(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "/dev/kvm") {
		t.Fatalf("missing device: %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-kvm-device")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkKVMAt(file); err == nil || !strings.Contains(err.Error(), "KVM API 12") {
		t.Fatalf("invalid API: %v", err)
	}
}
