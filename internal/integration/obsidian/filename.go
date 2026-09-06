package obsidian

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// taskFilename keeps names readable while avoiding filesystem and Obsidian link
// characters. Leave room for the directory ID, .md, and atomic-write temp suffixes
// within the usual 255-byte filesystem component limit.
func taskFilename(title string) string {
	const maxBytes = 200
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*[]#^`, r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(title))
	trimEdge := func(r rune) bool { return r == '.' || unicode.IsSpace(r) }
	name = strings.TrimFunc(name, trimEdge)
	if name == "" {
		name = "Untitled"
	}
	var result strings.Builder
	for _, r := range name {
		if result.Len()+len(string(r)) > maxBytes {
			break
		}
		result.WriteRune(r)
	}
	name = strings.TrimRightFunc(result.String(), trimEdge)
	// Windows reserves device names even when followed by a file extension.
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		name = "_" + name
		if len(name) > maxBytes {
			_, size := utf8.DecodeLastRuneInString(name)
			name = strings.TrimRightFunc(name[:len(name)-size], trimEdge)
		}
	}
	return name
}
