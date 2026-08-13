package handoff

import (
	"strings"
	"testing"
)

func testRoles() Roles {
	return NewRoles([]string{"specifier", "coder", "refactorer", "architect"})
}

// validCommit is a well-formed 10-character abbreviation. It does not need to
// exist: Validate checks shape only, resolution happens in the daemon.
const validCommit = "71ae82cc13"

func validGit() Handoff {
	return Handoff{
		Type: TypeGit, From: "coder", To: []string{"refactorer"},
		Task: "AUTH-42", Commit: validCommit, Priority: 20, Note: "done",
	}
}

func validNote() Handoff {
	return Handoff{Type: TypeNote, From: "architect", To: []string{"specifier"}, Priority: 10, Note: "clarify"}
}

func TestValidateAccepts(t *testing.T) {
	if err := Validate(validGit(), testRoles(), "coder"); err != nil {
		t.Errorf("git handoff rejected: %v", err)
	}
	if err := Validate(validNote(), testRoles(), "architect"); err != nil {
		t.Errorf("note rejected: %v", err)
	}
	if err := Validate(validGit(), testRoles(), ""); err != nil {
		t.Errorf("ownerless validation failed: %v", err)
	}

	multi := validNote()
	multi.To = []string{"specifier", "coder"}
	if err := Validate(multi, testRoles(), "architect"); err != nil {
		t.Errorf("multi-destination note rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Handoff)
		owner  string
		want   string
	}{
		{"unknown type", func(h *Handoff) { h.Type = "explode" }, "coder", "unsupported type"},
		{"missing type", func(h *Handoff) { h.Type = "" }, "coder", "missing type"},
		{"unknown sender", func(h *Handoff) { h.From = "foo" }, "", "sender role \"foo\" is not configured"},
		{"unknown destination", func(h *Handoff) { h.To = []string{"foo"} }, "coder", "destination role \"foo\" is not configured"},
		{"one bad destination", func(h *Handoff) { h.To = []string{"architect", "foo"} }, "coder", "not configured"},
		{"no destination", func(h *Handoff) { h.To = nil }, "coder", "missing destination"},
		{"duplicate destination", func(h *Handoff) { h.To = []string{"architect", "architect"} }, "coder", "listed twice"},
		{"self handoff", func(h *Handoff) { h.To = []string{"coder"} }, "coder", "cannot hand off to itself"},
		{"outbox mismatch", func(h *Handoff) {}, "architect", "does not match outbox"},
		{"missing task", func(h *Handoff) { h.Task = "" }, "coder", "requires a task"},
		{"missing commit", func(h *Handoff) { h.Commit = "" }, "coder", "requires a commit"},
		{"short commit", func(h *Handoff) { h.Commit = "abc123" }, "coder", "exactly 10"},
		{"long commit", func(h *Handoff) { h.Commit = "71ae82cc13ZZ" }, "coder", "exactly 10"},
		{"non-hex commit", func(h *Handoff) { h.Commit = "zzzzzzzzzz" }, "coder", "not hexadecimal"},
		{"missing note", func(h *Handoff) { h.Note = "" }, "coder", "missing note"},
		{"blank note", func(h *Handoff) { h.Note = "   " }, "coder", "missing note"},
		{"priority too low", func(h *Handoff) { h.Priority = -1 }, "coder", "outside"},
		{"priority too high", func(h *Handoff) { h.Priority = 101 }, "coder", "outside"},
		{"task newline", func(h *Handoff) { h.Task = "a\nb" }, "coder", "single line"},
		{"task control char", func(h *Handoff) { h.Task = "a\x07b" }, "coder", "control character"},
	}

	for _, c := range cases {
		h := validGit()
		c.mutate(&h)

		err := Validate(h, testRoles(), c.owner)
		if err == nil {
			t.Errorf("%s: expected rejection", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
}

// The 10-character rule is the SwarmForge parity requirement.
func TestValidateCommitLength(t *testing.T) {
	for _, commit := range []string{"abc123", "71ae82cc1", "71ae82cc133", "71ae82cc13ZZ", ""} {
		h := validGit()
		h.Commit = commit
		if err := Validate(h, testRoles(), "coder"); err == nil {
			t.Errorf("commit %q was accepted", commit)
		}
	}

	// Exactly ten hex characters passes the shape check, in either case.
	for _, commit := range []string{"71ae82cc13", "71AE82CC13", "0123456789", "abcdefABCD"} {
		h := validGit()
		h.Commit = commit
		if err := Validate(h, testRoles(), "coder"); err != nil {
			t.Errorf("commit %q was rejected: %v", commit, err)
		}
	}
}

func TestValidateNoteMustNotCarryGitFields(t *testing.T) {
	h := validNote()
	h.Commit = validCommit
	if err := Validate(h, testRoles(), "architect"); err == nil {
		t.Error("note with a commit was accepted")
	}

	h = validNote()
	h.Task = "AUTH-42"
	if err := Validate(h, testRoles(), "architect"); err == nil {
		t.Error("note with a task was accepted")
	}
}

// A role name from a file must never be usable as a path.
func TestValidateRejectsPathTraversalRoles(t *testing.T) {
	for _, evil := range []string{"../../etc", "..", ".", "coder/../architect", `..\windows`, "/etc"} {
		h := validNote()
		h.To = []string{evil}
		if err := Validate(h, testRoles(), h.From); err == nil {
			t.Errorf("destination %q was accepted", evil)
		}

		h = validNote()
		h.From = evil
		if err := Validate(h, testRoles(), ""); err == nil {
			t.Errorf("sender %q was accepted", evil)
		}
	}
}

func TestRoles(t *testing.T) {
	r := testRoles()

	if !r.Has("coder") {
		t.Error("configured role reported as unknown")
	}
	if r.Has("foo") || r.Has("") {
		t.Error("unconfigured role reported as known")
	}
}
