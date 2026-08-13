package handoff

import (
	"strings"
	"testing"
)

func TestUnmarshalGitHandoff(t *testing.T) {
	in := `type: git_handoff
from: coder
to: refactorer
task: AUTH-42
commit: 71ae82cc13
priority: 20
note: Implementation complete; tests pass.
`

	h, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}

	want := Handoff{
		Type: TypeGit, From: "coder", To: "refactorer",
		Task: "AUTH-42", Commit: "71ae82cc13", Priority: 20,
		Note: "Implementation complete; tests pass.",
	}
	if h != want {
		t.Errorf("got  %+v\nwant %+v", h, want)
	}
}

func TestUnmarshalNote(t *testing.T) {
	in := `# a comment

type: note
from: architect
to: specifier
priority: 10
note: Please clarify expected retry behavior.
`

	h, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}

	if h.Type != TypeNote || h.From != "architect" || h.To != "specifier" || h.Priority != 10 {
		t.Errorf("unexpected handoff: %+v", h)
	}
	if h.Task != "" || h.Commit != "" {
		t.Errorf("note should have no task/commit: %+v", h)
	}
}

func TestUnmarshalMultiLineNote(t *testing.T) {
	in := "type: note\nfrom: coder\nto: architect\npriority: 5\nnote: first line\nsecond line\nthird line\n"

	h, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if h.Note != "first line\nsecond line\nthird line" {
		t.Errorf("Note = %q", h.Note)
	}
}

func TestUnmarshalSyntaxErrors(t *testing.T) {
	cases := map[string]string{
		"no colon":      "type git_handoff\n",
		"unknown field": "type: note\nnonsense: x\n",
		"duplicate":     "type: note\ntype: note\n",
		"bad priority":  "type: note\npriority: soon\n",
		"empty key":     ": value\n",
	}

	for name, in := range cases {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("%s: expected a parse error", name)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []Handoff{
		{Type: TypeGit, From: "coder", To: "refactorer", Task: "AUTH-42", Commit: "71ae82cc13", Priority: 20, Note: "done"},
		{Type: TypeNote, From: "architect", To: "specifier", Priority: 0, Note: "clarify retries"},
		{Type: TypeNote, From: "specifier", To: "coder", Priority: 100, Note: "line one\nline two"},
	}

	for _, want := range cases {
		got, err := Unmarshal([]byte(Marshal(want)))
		if err != nil {
			t.Fatalf("%+v: %v", want, err)
		}
		if got != want {
			t.Errorf("round trip changed the message:\ngot  %+v\nwant %+v", got, want)
		}
	}
}

func TestMarshalFieldOrder(t *testing.T) {
	out := Marshal(Handoff{
		Type: TypeGit, From: "coder", To: "refactorer",
		Task: "T-1", Commit: "abcd", Priority: 20, Note: "n",
	})

	want := []string{"type:", "from:", "to:", "task:", "commit:", "priority:", "note:"}

	pos := -1
	for _, key := range want {
		i := strings.Index(out, key)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", key, out)
		}
		if i < pos {
			t.Errorf("%q is out of order in:\n%s", key, out)
		}
		pos = i
	}
}

func TestMarshalOmitsEmptyOptionalFields(t *testing.T) {
	out := Marshal(Handoff{Type: TypeNote, From: "a", To: "b", Priority: 1, Note: "n"})

	if strings.Contains(out, "task:") || strings.Contains(out, "commit:") {
		t.Errorf("empty fields were emitted:\n%s", out)
	}
}
