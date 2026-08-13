package handoff

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnmarshalGitHandoff(t *testing.T) {
	in := `id: 9f2c1d7a5b3e4f60a1c2d3e4f5061728
type: git_handoff
from: coder
to: refactorer
task: AUTH-42
commit: 71ae82cc13
canonical_commit: 71ae82cc13a9f0e4d8c7b6a5049382716f5e4d3c
priority: 20
created_at: 2026-08-13T21:05:01.123456789Z
note: Implementation complete; tests pass.
`

	h, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}

	if h.ID != "9f2c1d7a5b3e4f60a1c2d3e4f5061728" || h.Type != TypeGit {
		t.Errorf("id/type wrong: %+v", h)
	}
	if !reflect.DeepEqual(h.To, []string{"refactorer"}) {
		t.Errorf("To = %v", h.To)
	}
	if h.Commit != "71ae82cc13" || h.CanonicalCommit != "71ae82cc13a9f0e4d8c7b6a5049382716f5e4d3c" {
		t.Errorf("commits wrong: %+v", h)
	}
	if h.CreatedAt.IsZero() || h.CreatedAt.Nanosecond() != 123456789 {
		t.Errorf("CreatedAt = %v", h.CreatedAt)
	}
}

func TestUnmarshalMultipleDestinations(t *testing.T) {
	in := "type: note\nfrom: coder\nto: refactorer, architect ,specifier\npriority: 5\nnote: hi\n"

	h, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"refactorer", "architect", "specifier"}
	if !reflect.DeepEqual(h.To, want) {
		t.Errorf("To = %v, want %v", h.To, want)
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

	if h.Type != TypeNote || h.From != "architect" || h.Priority != 10 {
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
		"bad timestamp": "type: note\ncreated_at: yesterday\n",
	}

	for name, in := range cases {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("%s: expected a parse error", name)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	stamp := time.Date(2026, 8, 13, 21, 5, 1, 123456789, time.UTC)

	cases := []Handoff{
		{
			ID: "abc123", Type: TypeGit, From: "coder", To: []string{"refactorer"},
			Task: "AUTH-42", Commit: "71ae82cc13",
			CanonicalCommit: "71ae82cc13a9f0e4d8c7b6a5049382716f5e4d3c",
			Priority:        20, CreatedAt: stamp, DeliveredAt: stamp.Add(time.Second), Note: "done",
		},
		{
			ID: "def456", Type: TypeNote, From: "architect", To: []string{"specifier", "coder"},
			Priority: 0, CreatedAt: stamp, Note: "clarify retries",
		},
		{
			ID: "ghi789", Type: TypeNote, From: "specifier", To: []string{"coder"},
			Priority: 100, CreatedAt: stamp, Note: "line one\nline two",
		},
	}

	for _, want := range cases {
		got, err := Unmarshal([]byte(Marshal(want)))
		if err != nil {
			t.Fatalf("%+v: %v", want, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip changed the message:\ngot  %+v\nwant %+v", got, want)
		}
	}
}

func TestMarshalFieldOrder(t *testing.T) {
	out := Marshal(Handoff{
		ID: "id1", Type: TypeGit, From: "coder", To: []string{"refactorer"},
		Task: "T-1", Commit: "71ae82cc13", CanonicalCommit: "71ae82cc13ff",
		Priority: 20, CreatedAt: time.Now(), Note: "n",
	})

	want := []string{"id:", "type:", "from:", "to:", "task:", "commit:", "canonical_commit:", "priority:", "created_at:", "note:"}

	pos := -1
	for _, key := range want {
		i := strings.Index(out, "\n"+key)
		if i < 0 && strings.HasPrefix(out, key) {
			i = 0
		}
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
	out := Marshal(Handoff{Type: TypeNote, From: "a", To: []string{"b"}, Priority: 1, Note: "n"})

	for _, key := range []string{"task:", "commit:", "canonical_commit:", "created_at:", "delivered_at:", "id:"} {
		if strings.Contains(out, key) {
			t.Errorf("empty field %q was emitted:\n%s", key, out)
		}
	}
}

func TestNewIDIsUniqueAndHex(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 1000; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != idBytes*2 {
			t.Fatalf("id %q has length %d", id, len(id))
		}
		if strings.ContainsAny(id, `/\.-`) {
			t.Fatalf("id %q is not a safe file-name component", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q after %d draws", id, i)
		}
		seen[id] = true
	}
}
