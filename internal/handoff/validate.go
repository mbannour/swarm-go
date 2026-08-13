package handoff

import (
	"fmt"
	"strings"
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

	if h.To == "" {
		return fmt.Errorf("missing destination")
	}
	if !roles.Has(h.To) {
		return fmt.Errorf("destination role %q is not configured", h.To)
	}
	if h.To == h.From {
		return fmt.Errorf("role %q cannot hand off to itself", h.From)
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
		if err := checkInline("task", h.Task); err != nil {
			return err
		}
		if h.Commit == "" {
			return fmt.Errorf("git_handoff requires a commit")
		}
		if err := checkCommit(h.Commit); err != nil {
			return err
		}
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

// checkCommit requires something that looks like a Git object name. Handoff
// values are never passed to a shell, but keeping this strict makes a
// malformed message obvious at delivery time rather than later.
func checkCommit(commit string) error {
	if len(commit) < 4 || len(commit) > 64 {
		return fmt.Errorf("commit %q must be 4..64 characters", commit)
	}
	for _, r := range commit {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return fmt.Errorf("commit %q is not a hexadecimal object name", commit)
		}
	}
	return nil
}
