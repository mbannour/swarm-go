package handoff

import (
	"strings"
	"testing"
)

func testRoles() Roles {
	return NewRoles([]string{"specifier", "coder", "refactorer", "architect"})
}

func validGit() Handoff {
	return Handoff{
		Type: TypeGit, From: "coder", To: "refactorer",
		Task: "AUTH-42", Commit: "71ae82cc13", Priority: 20, Note: "done",
	}
}

func validNote() Handoff {
	return Handoff{Type: TypeNote, From: "architect", To: "specifier", Priority: 10, Note: "clarify"}
}

func TestValidateAccepts(t *testing.T) {
	if err := Validate(validGit(), testRoles(), "coder"); err != nil {
		t.Errorf("git handoff rejected: %v", err)
	}
	if err := Validate(validNote(), testRoles(), "architect"); err != nil {
		t.Errorf("note rejected: %v", err)
	}
	// No owner means "not from an outbox" and must still pass.
	if err := Validate(validGit(), testRoles(), ""); err != nil {
		t.Errorf("ownerless validation failed: %v", err)
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
		{"unknown destination", func(h *Handoff) { h.To = "foo" }, "coder", "destination role \"foo\" is not configured"},
		{"self handoff", func(h *Handoff) { h.To = "coder" }, "coder", "cannot hand off to itself"},
		{"outbox mismatch", func(h *Handoff) {}, "architect", "does not match outbox"},
		{"missing task", func(h *Handoff) { h.Task = "" }, "coder", "requires a task"},
		{"missing commit", func(h *Handoff) { h.Commit = "" }, "coder", "requires a commit"},
		{"non-hex commit", func(h *Handoff) { h.Commit = "zzzz" }, "coder", "hexadecimal"},
		{"short commit", func(h *Handoff) { h.Commit = "ab" }, "coder", "4..64"},
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

// A role name from a file must never be usable as a path.
func TestValidateRejectsPathTraversalRoles(t *testing.T) {
	for _, evil := range []string{"../../etc", "..", ".", "coder/../architect", `..\windows`, "/etc"} {
		h := validNote()
		h.To = evil
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
