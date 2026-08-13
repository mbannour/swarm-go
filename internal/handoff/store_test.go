package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	s := NewStore(t.TempDir(), testRoles())
	if err := s.EnsureDirs([]string{"specifier", "coder", "refactorer", "architect"}); err != nil {
		t.Fatal(err)
	}

	return s
}

func TestEnsureDirsIsIdempotent(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnsureDirs([]string{"coder"}); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}

	for _, role := range []string{"specifier", "coder", "refactorer", "architect"} {
		for _, box := range []string{"inbox", "outbox"} {
			dir := filepath.Join(s.Root, role, box)
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Errorf("%s missing: %v", dir, err)
			}
		}
	}
	if _, err := os.Stat(s.RejectedDir()); err != nil {
		t.Errorf("rejected dir missing: %v", err)
	}
}

func TestDirLookupRejectsUnconfiguredRoles(t *testing.T) {
	s := newTestStore(t)

	for _, evil := range []string{"foo", "..", "../../etc", "coder/../architect", ""} {
		if _, err := s.InboxDir(evil); err == nil {
			t.Errorf("InboxDir(%q) was allowed", evil)
		}
		if _, err := s.OutboxDir(evil); err == nil {
			t.Errorf("OutboxDir(%q) was allowed", evil)
		}
	}
}

func TestFileNameUniqueness(t *testing.T) {
	base := time.Date(2026, 8, 13, 21, 5, 1, 123456789, time.UTC)

	name := FileName(base, "coder", "refactorer")
	if !strings.HasSuffix(name, ".handoff") {
		t.Errorf("name %q lacks the extension", name)
	}
	if !strings.Contains(name, "coder-to-refactorer") {
		t.Errorf("name %q lacks the route", name)
	}

	// Sub-second resolution: two handoffs 1ns apart get distinct names.
	if a, b := FileName(base, "a", "b"), FileName(base.Add(time.Nanosecond), "a", "b"); a == b {
		t.Errorf("names collide at nanosecond resolution: %s", a)
	}

	// Names sort chronologically, which is what the daemon relies on.
	early := FileName(base, "a", "b")
	late := FileName(base.Add(time.Second), "a", "b")
	if !(early < late) {
		t.Errorf("names do not sort chronologically: %s !< %s", early, late)
	}
}

// Two sends in the same instant must not overwrite each other.
func TestSendWithFrozenClockDoesNotCollide(t *testing.T) {
	s := newTestStore(t)
	s.Now = func() time.Time { return time.Date(2026, 8, 13, 21, 5, 1, 0, time.UTC) }

	h := validNote()
	h.From, h.To = "coder", "architect"

	first, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("second send overwrote the first")
	}

	entries, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("outbox has %d entries, want 2", len(entries))
	}
}

func TestSendValidatesBeforeWriting(t *testing.T) {
	s := newTestStore(t)

	bad := validGit()
	bad.Commit = ""

	if _, err := s.Send(bad); err == nil {
		t.Fatal("invalid handoff was written")
	}

	entries, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("outbox is not empty: %+v", entries)
	}
}

func TestSendRejectsUnconfiguredSender(t *testing.T) {
	s := newTestStore(t)

	h := validNote()
	h.From = "intruder"

	if _, err := s.Send(h); err == nil {
		t.Fatal("handoff from an unconfigured role was accepted")
	}
	// Nothing may have been created for the bogus role.
	if _, err := os.Stat(filepath.Join(s.Root, "intruder")); !os.IsNotExist(err) {
		t.Error("a directory was created for an unconfigured role")
	}
}

func TestSendWritesParsableFile(t *testing.T) {
	s := newTestStore(t)

	want := validGit()
	path, err := s.Send(want)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("file round trip changed the message:\ngot  %+v\nwant %+v", got, want)
	}

	// No temp files may be left behind.
	items, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", item.Name())
		}
	}
}

func TestListOrdersByPriorityThenAge(t *testing.T) {
	s := newTestStore(t)

	stamp := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	send := func(priority int, note string) {
		stamp = stamp.Add(time.Second)
		s.Now = func() time.Time { return stamp }

		h := validNote()
		h.From, h.To, h.Priority, h.Note = "coder", "architect", priority, note
		if _, err := s.Send(h); err != nil {
			t.Fatal(err)
		}
	}

	send(10, "low-first")
	send(90, "high-first")
	send(10, "low-second")
	send(90, "high-second")

	entries, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Note)
	}

	want := []string{"high-first", "high-second", "low-first", "low-second"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestDeliverMovesFile(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	entries, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}

	dst, err := s.Deliver(entries[0])
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(entries[0].Path); !os.IsNotExist(err) {
		t.Error("source file survived delivery")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("delivered file missing: %v", err)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Task != "AUTH-42" {
		t.Errorf("refactorer inbox = %+v", inbox)
	}

	out, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("coder outbox still has %d entries", len(out))
	}
}

func TestReject(t *testing.T) {
	s := newTestStore(t)

	outbox, err := s.OutboxDir("coder")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outbox, "broken.handoff")
	if err := os.WriteFile(path, []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := s.Reject(path, `destination role "foo" is not configured`)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("rejected file left in the outbox")
	}
	if filepath.Dir(dst) != s.RejectedDir() {
		t.Errorf("rejected file went to %s", filepath.Dir(dst))
	}

	reason, err := os.ReadFile(dst + ".reason")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reason), "not configured") {
		t.Errorf("reason file = %q", reason)
	}
}

func TestAck(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.Outbox("coder")
	if _, err := s.Deliver(entries[0]); err != nil {
		t.Fatal(err)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}

	dst, err := s.Ack("refactorer", inbox[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dst) != s.ArchiveDir() {
		t.Errorf("archived to %s", filepath.Dir(dst))
	}

	after, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("inbox still holds %d entries", len(after))
	}
}

func TestAckRejectsPathTraversal(t *testing.T) {
	s := newTestStore(t)

	for _, evil := range []string{"../../../etc/passwd", "../outbox/x.handoff", "/etc/passwd", "", "sub/dir.handoff"} {
		if _, err := s.Ack("coder", evil); err == nil {
			t.Errorf("Ack accepted %q", evil)
		}
	}
}

func TestListIgnoresNonHandoffFiles(t *testing.T) {
	s := newTestStore(t)

	outbox, _ := s.OutboxDir("coder")
	for _, name := range []string{"README.txt", ".hidden.handoff", "notes.md"} {
		if err := os.WriteFile(filepath.Join(outbox, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, bad, err := s.list(outbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || len(bad) != 0 {
		t.Errorf("entries=%v bad=%v, want both empty", entries, bad)
	}
}
