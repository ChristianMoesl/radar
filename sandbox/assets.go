// Package sandbox supplies the user-owned starter kit installed by radar setup.
package sandbox

import _ "embed"

// KitSpec is also the source for the published kit. CI pins its image separately.
//
//go:embed kit/spec.yaml
var KitSpec string

// StartupReadme is installed non-executable; an empty hook directory is a no-op.
//
//go:embed startup-README.md
var StartupReadme string
