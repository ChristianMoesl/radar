// Package sessionlease admits tmux navigation and cleanup by both reusable
// session name and current server/session incarnation, not caller selectors.
package sessionlease

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"radar/internal/workspacelease"
)

type Runner interface {
	Run(context.Context, string, string, ...string) (string, error)
}

type Request struct {
	Selector     string
	Name         string // Reusable name to reserve, including while absent.
	Expected     string // Collected tmux:session:<pid>:<created>:<id>, when known.
	AllowMissing bool
}

type Identity struct {
	ServerPID string
	Created   string
	ID        string
	Name      string
}

func (i Identity) Key() string {
	return "tmux:session:" + i.ServerPID + ":" + i.Created + ":" + i.ID
}

// Set is owned by one admitting operation. Close after its last native effect
// or foreground process callback; repeated Close calls are harmless. Admission
// deduplicates keys, never the caller's provider targets.
type Set struct {
	exclusive bool
	held      map[string]bool
	releases  []func()
	once      sync.Once
	closed    bool
}

func New(exclusive bool) *Set {
	return &Set{exclusive: exclusive, held: make(map[string]bool)}
}

func (s *Set) Close() {
	s.once.Do(func() {
		s.closed = true
		for i := len(s.releases) - 1; i >= 0; i-- {
			s.releases[i]()
		}
	})
}

func (s *Set) acquire(key string) error {
	if s.held[key] {
		return nil
	}
	release, err := workspacelease.AcquireTmux(key, s.exclusive)
	if err != nil {
		return err
	}
	s.held[key] = true
	s.releases = append(s.releases, release)
	return nil
}

var (
	incarnation = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*:\$[0-9]+$`)
	serverPID   = regexp.MustCompile(`^[1-9][0-9]*$`)
	sessionID   = regexp.MustCompile(`^\$[0-9]+$`)
)

// Admit resolves selectors, acquires matching names/incarnation and rechecks
// resolution under admission. The caller must dispatch by the returned ID,
// never by a prefix/glob/name that can select another runtime later. On any
// failure, unwind the entire operation's partially admitted set.
func (s *Set) Admit(ctx context.Context, runner Runner, req Request) (result Identity, err error) {
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	if s.closed {
		return result, fmt.Errorf("tmux admission is closed")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Selector) == "" || strings.Contains(req.Selector, ":") || (req.Name != "" && strings.TrimSpace(req.Name) == "") || strings.IndexFunc(req.Selector+req.Name, unicode.IsControl) >= 0 {
		return result, fmt.Errorf("valid tmux session selector is required")
	}
	if req.Expected != "" && (!strings.HasPrefix(req.Expected, "tmux:session:") || !incarnation.MatchString(strings.TrimPrefix(req.Expected, "tmux:session:"))) {
		return result, fmt.Errorf("invalid expected tmux runtime identity")
	}
	if req.AllowMissing && (req.Name == "" || (req.Selector != "="+req.Name && !sessionID.MatchString(req.Selector))) {
		return result, fmt.Errorf("absent tmux admission requires an exact name or collected session ID")
	}
	initial, err := resolve(ctx, runner, req.Selector)
	if err != nil {
		return result, err
	}
	if req.Name != "" {
		if err := s.acquire("name:" + req.Name); err != nil {
			return result, err
		}
	}
	if initial.ID == "" {
		if !req.AllowMissing || strings.TrimSpace(req.Name) == "" {
			return result, fmt.Errorf("tmux session no longer exists; refresh and retry")
		}
		// A missing numeric ID must not silently authorize cleanup of a new
		// incarnation now using the old collected name.
		current, err := resolve(ctx, runner, req.Selector)
		if err != nil {
			return result, err
		}
		if current.ID == "" && req.Selector != "="+req.Name {
			current, err = resolve(ctx, runner, "="+req.Name)
			if err != nil {
				return result, err
			}
			if current.ID != "" {
				return result, fmt.Errorf("tmux runtime identity changed; refresh and retry")
			}
		}
		if current.ID == "" {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			return Identity{Name: req.Name}, nil
		}
		initial = current
	}
	if req.Expected != "" && initial.Key() != req.Expected {
		return result, fmt.Errorf("tmux runtime identity changed; refresh and retry")
	}
	for _, key := range []string{"name:" + initial.Name, "runtime:" + initial.Key()} {
		if err := s.acquire(key); err != nil {
			return result, err
		}
	}
	current, err := resolve(ctx, runner, req.Selector)
	if err != nil {
		return result, err
	}
	if current != initial {
		return result, fmt.Errorf("tmux session changed during admission; refresh and retry")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return initial, nil
}

func resolve(ctx context.Context, runner Runner, selector string) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	// display-message takes a pane target. A trailing colon supplies explicit
	// session context. Use only numeric fields here: literal control separators
	// are not portable, and arbitrary names must not split an identity tuple.
	output, err := runner.Run(ctx, "", "tmux", "display-message", "-p", "-t", selector+":", "#{pid}:#{session_created}:#{session_id}")
	if err != nil {
		detail := strings.ToLower(output + " " + err.Error())
		if strings.Contains(detail, "no server running") || (strings.Contains(detail, "error connecting to") && strings.Contains(detail, "no such file or directory")) {
			return Identity{}, nil
		}
		return Identity{}, err
	}
	output = strings.TrimSpace(output)
	fields := strings.Split(output, ":")
	// tmux may successfully print only the server PID for a missing target.
	if len(fields) == 3 && fields[1] == "" && fields[2] == "" && serverPID.MatchString(fields[0]) {
		return Identity{}, nil
	}
	if !incarnation.MatchString(output) {
		return Identity{}, fmt.Errorf("incomplete tmux runtime identity; refresh and retry")
	}
	name, err := runner.Run(ctx, "", "tmux", "display-message", "-p", "-t", fields[2]+":", "#{session_name}")
	if err != nil {
		return Identity{}, err
	}
	name = strings.TrimSuffix(name, "\n")
	if strings.TrimSpace(name) == "" || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Identity{}, fmt.Errorf("incomplete tmux session name; refresh and retry")
	}
	return Identity{ServerPID: fields[0], Created: fields[1], ID: fields[2], Name: name}, nil
}
