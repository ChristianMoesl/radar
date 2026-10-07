package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Notice struct {
	Checked time.Time `json:"checked"`
	Current string    `json:"current"`
	Version string    `json:"version"`
	Error   string    `json:"error"`
}

// Cached notices are informational only. The interactive flow fetches and
// authenticates everything again; modifying the cache cannot authorize an update.
func CheckNotice(ctx context.Context, current string) Notice {
	n := Notice{Current: current}
	if runtime.GOOS != "darwin" {
		return n
	}
	executable, err := os.Executable()
	if err != nil {
		return n
	}
	if _, err := Inspect(executable); err != nil {
		return n
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return n
	}
	file := filepath.Join(cache, "radar/release-notice.json")
	if data, err := os.ReadFile(file); err == nil {
		var old Notice
		if json.Unmarshal(data, &old) == nil && old.Current == current && time.Since(old.Checked) >= 0 && time.Since(old.Checked) < time.Hour && (old.Version == "" || Stable(old.Version)) {
			return old
		}
	}
	n.Checked = time.Now()
	m, err := NewClient().Latest(ctx, current)
	if err != nil {
		n.Error = "Update check unavailable; press u for details"
	} else if m != nil {
		n.Version = m.Version
	}
	_ = writeJSON(file, n)
	return n
}
