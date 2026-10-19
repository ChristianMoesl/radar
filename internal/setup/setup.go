// Package setup defines the foreground-only interaction and deferred file plans
// shared by onboarding and integration setup providers. It does not run setup.
package setup

import (
	"context"
	"errors"

	"github.com/charmbracelet/lipgloss"
)

var ErrAborted = errors.New("setup cancelled — no Radar configuration was written")
var ErrExtensionDeclined = errors.New("extension installation declined")

type Choice struct{ Value, Label string }
type Question struct {
	Title, Hint, Initial string
	Secret               bool
	Choices              []Choice
	Validate             func(string) error
}

type UI interface {
	Input(Question) (string, error)
	Confirm(string, bool) (bool, error)
	Print(string, ...any)
	Style() lipgloss.Style
}

type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

type System interface {
	LookPath(string) bool
	Platform() string
	Output(context.Context, Command) (string, error)
	Run(context.Context, Command) error
}

// File contains only review-safe content. Credential values must never enter it.
type File struct{ Path, Purpose, Action, Details string }

// Plan defers persistent changes until the combined review is accepted. All
// CheckUnchanged callbacks run under the setup lock before any plan is applied.
// Providers must also revalidate at Apply to protect against external writers.
type Plan struct {
	Files          []File
	Directories    []string
	Summary        string
	CheckUnchanged func() error
	Apply          func() error
	AfterSave      func(context.Context)
}
