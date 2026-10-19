package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOutputHelper(t *testing.T) {
	switch os.Getenv("RADAR_OUTPUT_HELPER") {
	case "large":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 21<<20))
		os.Exit(0)
	case "private":
		_, _ = os.Stdout.WriteString("private credential")
		_, _ = os.Stderr.WriteString("private credential")
		os.Exit(3)
	case "terminal":
		f, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
			os.Exit(4)
		}
		_, _ = os.Stdout.WriteString("no controlling terminal")
		os.Exit(0)
	}
}
func TestProviderOutputIsBoundedAndRedactsDiagnostics(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"large", "private", "terminal"} {
		out, err := Output(context.Background(), binary, []string{"-test.run=^TestOutputHelper$"}, append(os.Environ(), "RADAR_OUTPUT_HELPER="+mode))
		if mode == "terminal" {
			if err != nil || string(out) != "no controlling terminal" {
				t.Fatalf("output=%q err=%v", out, err)
			}
			continue
		}
		if err == nil || len(out) != 0 || strings.Contains(err.Error(), "private credential") {
			t.Fatalf("mode=%s output=%d err=%v", mode, len(out), err)
		}
	}
}
func TestProviderOutputHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Output(ctx, "/bin/sh", []string{"-c", "sleep 30"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
