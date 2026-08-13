package handoff

import (
	"fmt"
	"strings"

	"github.com/mbannour/swarm-go/internal/git"
)

// Roles is the set of configured role names. Role names are only ever taken
// from configuration — never from a handoff file — so a message can never name
// a path component the operator did not configure.
type Roles map[string]bool

// NewRoles builds a role set from configured names.
func NewRoles(names []string) Roles {
	r := make(Roles, len(names))
	for _, n := range names {
		r[n] = true
	}
	return r
}

// Has reports whether a role is configured.
func (r Roles) Has(name string) bool { return r[name] }

// Validate checks a parsed handoff against the configured roles.
//
// This is the "is the message itself well formed" check: everything it rejects
// is a permanent fault of the message, and the daemon quarantines such files
// under rejected/. Whether the commit actually exists is a separate question,
// answered by ResolveCommit — see daemon.go.
//
// owner is the role whose outbox the file was found in; pass "" when the
// message did not come from an outbox (for example, `handoff send`).
func Validate(h Handoff, roles Roles, owner string) error {
	switch h.Type {
	case TypeGit, TypeNote:
	case "":
		return fmt.Errorf("missing type")
	default:
		return fmt.Errorf("unsupported type %q", h.Type)
	}

	if h.From == "" {
		return fmt.Errorf("missing sender")
	}
	if !roles.Has(h.From) {
		return fmt.Errorf("sender role %q is not configured", h.From)
	}

	if len(h.To) == 0 {
		return fmt.Errorf("missing destination")
	}

	seen := map[string]bool{}
	for _, to := range h.To {
		if !roles.Has(to) {
			return fmt.Errorf("destination role %q is not configured", to)
		}
		if to == h.From {
			return fmt.Errorf("role %q cannot hand off to itself", h.From)
		}
		if seen[to] {
			return fmt.Errorf("destination role %q is listed twice", to)
		}
		seen[to] = true
	}

	if owner != "" && h.From != owner {
		return fmt.Errorf("sender %q does not match outbox of role %q", h.From, owner)
	}

	if h.Priority < MinPriority || h.Priority > MaxPriority {
		return fmt.Errorf("priority %d is outside %d..%d", h.Priority, MinPriority, MaxPriority)
	}

	if strings.TrimSpace(h.Note) == "" {
		return fmt.Errorf("missing note")
	}

	if h.Type == TypeGit {
		if h.Task == "" {
			return fmt.Errorf("git_handoff requires a task")
		}
		if h.Commit == "" {
			return fmt.Errorf("git_handoff requires a commit")
		}
		// Shape only. Existence is checked against the repository later.
		if err := git.ValidAbbrev(h.Commit); err != nil {
			return err
		}
	} else if h.Commit != "" || h.Task != "" {
		// Notes carry no Git identity; silently ignoring the fields would
		// hide a mistake in the sender.
		return fmt.Errorf("%s must not carry task or commit fields", TypeNote)
	}

	if h.Task != "" {
		if err := checkInline("task", h.Task); err != nil {
			return err
		}
	}

	return nil
}

// checkInline rejects values that would break the one-line wire format or
// carry control characters into a terminal.
func checkInline(field, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must be a single line", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	if len(value) > 200 {
		return fmt.Errorf("%s is longer than 200 characters", field)
	}
	return nil
}
