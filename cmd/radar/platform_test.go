package main

import "testing"

func TestSupportedPlatforms(t *testing.T) {
	for _, tc := range []struct {
		os, arch string
		want     bool
	}{
		{"darwin", "arm64", true},
		{"darwin", "amd64", false},
		{"darwin", "386", false},
		{"linux", "amd64", true},
		{"linux", "arm64", true},
	} {
		t.Run(tc.os+"/"+tc.arch, func(t *testing.T) {
			if got := checkPlatform(tc.os, tc.arch) == nil; got != tc.want {
				t.Fatalf("supported=%v, want %v", got, tc.want)
			}
		})
	}
}
