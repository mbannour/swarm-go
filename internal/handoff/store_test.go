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

func TestEnsureDirsCreatesEveryBox(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnsureDirs([]string{"coder"}); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}

	for _, role := range []string{"specifier", "coder", "refactorer", "architect"} {
		for _, box := range roleBoxes {
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

func TestBoxRejectsUnconfiguredRolesAndBoxes(t *testing.T) {
	s := newTestStore(t)

	for _, evil := range []string{"foo", "..", "../../etc", "coder/../architect", ""} {
		if _, err := s.Box(evil, BoxInbox); err == nil {
			t.Errorf("Box(%q) was allowed", evil)
		}
	}
	for _, box := range []string{"..", "../outbox", "nonsense", ""} {
		if _, err := s.Box("coder", box); err == nil {
			t.Errorf("Box(coder, %q) was allowed", box)
		}
	}
}

func TestSendGeneratesMetadata(t *testing.T) {
	s := newTestStore(t)
	frozen := time.Date(2026, 8, 13, 21, 5, 1, 0, time.UTC)
	s.Now = func() time.Time { return frozen }

	// A sender trying to dictate lifecycle metadata must be overridden.
	h := validGit()
	h.ID = "sender-chosen-id"
	h.CreatedAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	h.CanonicalCommit = "deadbeef"

	entry, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}

	if entry.ID == "sender-chosen-id" || entry.ID == "" {
		t.Errorf("ID was not generated: %q", entry.ID)
	}
	if !entry.CreatedAt.Equal(frozen) {
		t.Errorf("CreatedAt = %v, want %v", entry.CreatedAt, frozen)
	}
	if entry.CanonicalCommit != "" {
		t.Errorf("CanonicalCommit was accepted from the sender: %q", entry.CanonicalCommit)
	}
	if !entry.DeliveredAt.IsZero() {
		t.Error("DeliveredAt was set before delivery")
	}
}

func TestSendUniqueEvenWithFrozenClock(t *testing.T) {
	s := newTestStore(t)
	s.Now = func() time.Time { return time.Date(2026, 8, 13, 21, 5, 1, 0, time.UTC) }

	first, err := s.Send(validGit())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Send(validGit())
	if err != nil {
		t.Fatal(err)
	}

	if first.Path == second.Path {
		t.Fatal("second send overwrote the first")
	}
	if first.ID == second.ID {
		t.Fatal("two handoffs share an id")
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
	bad.Commit = "abc123" // not ten characters

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
	if _, err := os.Stat(filepath.Join(s.Root, "intruder")); !os.IsNotExist(err) {
		t.Error("a directory was created for an unconfigured role")
	}
}

func TestListOrdersByPriorityThenAge(t *testing.T) {
	s := newTestStore(t)

	stamp := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	send := func(priority int, note string) {
		stamp = stamp.Add(time.Second)
		s.Now = func() time.Time { return stamp }

		h := validNote()
		h.From, h.To, h.Priority, h.Note = "coder", []string{"architect"}, priority, note
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

	want := []string{"high-first", "high-second", "low-first", "low-second"}
	for i, w := range want {
		if entries[i].Note != w {
			got := make([]string, len(entries))
			for j, e := range entries {
				got[j] = e.Note
			}
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestDeliverWritesCopyPerDestination(t *testing.T) {
	s := newTestStore(t)

	h := validNote()
	h.From, h.To = "architect", []string{"specifier", "coder"}
	entry, err := s.Send(h)
	if err != nil {
		t.Fatal(err)
	}

	for _, to := range entry.To {
		if _, already, err := s.Deliver(entry.Handoff, to); err != nil || already {
			t.Fatalf("Deliver(%s) = (%v, %v)", to, already, err)
		}
	}

	for _, to := range entry.To {
		inbox, err := s.Inbox(to)
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) != 1 {
			t.Fatalf("%s inbox has %d entries", to, len(inbox))
		}
		// Each copy is addressed to exactly one role.
		if len(inbox[0].To) != 1 || inbox[0].To[0] != to {
			t.Errorf("%s copy is addressed to %v", to, inbox[0].To)
		}
		if inbox[0].DeliveredAt.IsZero() {
			t.Errorf("%s copy has no delivered_at", to)
		}
		if inbox[0].ID != entry.ID {
			t.Errorf("%s copy has id %q, want %q", to, inbox[0].ID, entry.ID)
		}
	}
}

func TestDeliverIsIdempotent(t *testing.T) {
	s := newTestStore(t)

	entry, err := s.Send(validGit())
	if err != nil {
		t.Fatal(err)
	}

	if _, already, err := s.Deliver(entry.Handoff, "refactorer"); err != nil || already {
		t.Fatalf("first Deliver = (%v, %v)", already, err)
	}

	// A repeated attempt must report "already" and not add a second copy.
	if _, already, err := s.Deliver(entry.Handoff, "refactorer"); err != nil || !already {
		t.Fatalf("second Deliver = (%v, %v), want already=true", already, err)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox has %d copies, want 1", len(inbox))
	}
}

// Idempotency must also hold once the recipient has moved the work on.
func TestDeliverIdempotentAcrossLifecycle(t *testing.T) {
	s := newTestStore(t)

	entry, err := s.Send(validGit())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Deliver(entry.Handoff, "refactorer"); err != nil {
		t.Fatal(err)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}

	for _, box := range []string{BoxCurrent, BoxCompleted} {
		if _, err := s.MoveTo(inbox[0].Path, "refactorer", box); err != nil {
			t.Fatal(err)
		}

		_, already, err := s.Deliver(entry.Handoff, "refactorer")
		if err != nil || !already {
			t.Fatalf("with work in %s: Deliver = (%v, %v), want already=true", box, already, err)
		}

		current, err := s.List("refactorer", box)
		if err != nil {
			t.Fatal(err)
		}
		inbox = current
	}
}

func TestRejectAndFailAreDistinct(t *testing.T) {
	s := newTestStore(t)

	outbox, err := s.OutboxDir("coder")
	if err != nil {
		t.Fatal(err)
	}

	broken := filepath.Join(outbox, "broken.handoff")
	if err := os.WriteFile(broken, []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	rejected, err := s.Reject(broken, "file is not a valid handoff")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(rejected) != s.RejectedDir() {
		t.Errorf("rejected file went to %s", filepath.Dir(rejected))
	}

	unresolvable := filepath.Join(outbox, "unresolvable.handoff")
	if err := os.WriteFile(unresolvable, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	failedPath, err := s.Fail("coder", unresolvable, "commit does not resolve")
	if err != nil {
		t.Fatal(err)
	}
	failedDir, _ := s.Box("coder", BoxFailed)
	if filepath.Dir(failedPath) != failedDir {
		t.Errorf("failed file went to %s, want %s", filepath.Dir(failedPath), failedDir)
	}

	// Both record a reason.
	for _, p := range []string{rejected, failedPath} {
		data, err := os.ReadFile(p + ".reason")
		if err != nil {
			t.Fatalf("no reason beside %s: %v", p, err)
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			t.Errorf("empty reason beside %s", p)
		}
	}
}

func TestBatchMarker(t *testing.T) {
	s := newTestStore(t)

	id, err := s.BatchID("refactorer")
	if err != nil || id != "" {
		t.Fatalf("BatchID on a clean role = (%q, %v)", id, err)
	}

	if err := s.SetBatchID("refactorer", "batch-1"); err != nil {
		t.Fatal(err)
	}
	if id, err := s.BatchID("refactorer"); err != nil || id != "batch-1" {
		t.Fatalf("BatchID = (%q, %v)", id, err)
	}

	// The marker must not show up as a handoff.
	entries, err := s.Current("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("batch marker leaked into listings: %+v", entries)
	}

	if err := s.ClearBatchID("refactorer"); err != nil {
		t.Fatal(err)
	}
	if id, err := s.BatchID("refactorer"); err != nil || id != "" {
		t.Fatalf("after clear: BatchID = (%q, %v)", id, err)
	}
	// Clearing twice is not an error.
	if err := s.ClearBatchID("refactorer"); err != nil {
		t.Fatal(err)
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
	for _, name := range []string{"README.txt", ".hidden.handoff", "notes.md", ".batch"} {
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

func TestSendLeavesNoTempFiles(t *testing.T) {
	s := newTestStore(t)

	entry, err := s.Send(validGit())
	if err != nil {
		t.Fatal(err)
	}

	items, err := os.ReadDir(filepath.Dir(entry.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", item.Name())
		}
	}
}
