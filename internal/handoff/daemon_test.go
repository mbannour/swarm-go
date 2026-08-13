package handoff

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a Notifier that remembers who was woken, so delivery can be
// tested without a live tmux server.
type recorder struct {
	mu    sync.Mutex
	woken []string
	err   error
}

func (r *recorder) Notify(role string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.woken = append(r.woken, role)
	return r.err
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.woken...)
}

// fakeResolver stands in for Git: it resolves anything it was told about.
type fakeResolver struct {
	known map[string]string
	err   error
}

func (f fakeResolver) ResolveCommit(abbrev string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if sha, ok := f.known[strings.ToLower(abbrev)]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("commit %s does not resolve to a commit in this repository", abbrev)
}

const canonicalOfValidCommit = "71ae82cc13a9f0e4d8c7b6a5049382716f5e4d3c"

func newTestDaemon(t *testing.T) (*Daemon, *Store, *recorder) {
	t.Helper()

	s := newTestStore(t)
	n := &recorder{}

	resolver := fakeResolver{known: map[string]string{validCommit: canonicalOfValidCommit}}

	d := NewDaemon(s, []string{"specifier", "coder", "refactorer", "architect"}, n, resolver)
	d.Log = io.Discard

	return d, s, n
}

func TestScanDeliversAndCanonicalisesCommit(t *testing.T) {
	d, s, n := newTestDaemon(t)

	if _, err := s.Send(validGit()); err != nil { // coder -> refactorer
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Delivered != 1 || got.Sent != 1 || got.Rejected != 0 || got.Failed != 0 {
		t.Fatalf("scan = %+v, want one delivery and one sent", got)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("refactorer inbox = %+v", inbox)
	}
	if inbox[0].CanonicalCommit != canonicalOfValidCommit {
		t.Errorf("canonical commit = %q, want %q", inbox[0].CanonicalCommit, canonicalOfValidCommit)
	}
	if inbox[0].Commit != validCommit {
		t.Errorf("abbreviation was lost: %q", inbox[0].Commit)
	}
	if inbox[0].DeliveredAt.IsZero() {
		t.Error("delivered_at was not stamped")
	}

	// The sender keeps a record instead of losing the message.
	sent, err := s.List("coder", BoxSent)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Errorf("sent box holds %d items, want 1", len(sent))
	}
	out, err := s.Outbox("coder")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("outbox still holds %d items", len(out))
	}

	if woken := n.seen(); len(woken) != 1 || woken[0] != "refactorer" {
		t.Errorf("woken = %v, want [refactorer]", woken)
	}
}

// An unresolvable commit is a failed request, not a malformed message.
func TestScanFailsUnresolvableCommit(t *testing.T) {
	d, s, n := newTestDaemon(t)

	h := validGit()
	h.Commit = "0123456789" // well formed, unknown to the resolver
	if _, err := s.Send(h); err != nil {
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Failed != 1 || got.Delivered != 0 || got.Rejected != 0 {
		t.Fatalf("scan = %+v, want one failure", got)
	}

	failed, err := s.List("coder", BoxFailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 {
		t.Fatalf("failed box holds %d items", len(failed))
	}

	reason, err := os.ReadFile(failed[0].Path + ".reason")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reason), "does not resolve") {
		t.Errorf("reason = %q", reason)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Error("an unresolvable handoff was delivered")
	}
	if len(n.seen()) != 0 {
		t.Error("an agent was woken for a failed handoff")
	}
}

func TestScanDeliversToEveryDestination(t *testing.T) {
	d, s, n := newTestDaemon(t)

	h := validNote()
	h.From, h.To = "architect", []string{"specifier", "coder"}
	if _, err := s.Send(h); err != nil {
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Delivered != 2 || got.Sent != 1 {
		t.Fatalf("scan = %+v, want two deliveries and one sent", got)
	}

	for _, role := range []string{"specifier", "coder"} {
		inbox, err := s.Inbox(role)
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) != 1 {
			t.Errorf("%s inbox holds %d items", role, len(inbox))
		}
	}

	woken := n.seen()
	if len(woken) != 2 {
		t.Errorf("woken = %v, want both destinations", woken)
	}
}

// One destination failing must not undo the other's delivery.
func TestScanRecordsPartialDelivery(t *testing.T) {
	d, s, _ := newTestDaemon(t)

	h := validNote()
	h.From, h.To = "architect", []string{"specifier", "coder"}
	if _, err := s.Send(h); err != nil {
		t.Fatal(err)
	}

	// Make the coder inbox impossible to write to by putting a file where the
	// directory belongs.
	coderInbox, err := s.Box("coder", BoxInbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(coderInbox); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coderInbox, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Delivered != 1 || got.Failed != 1 {
		t.Fatalf("scan = %+v, want one delivery and one recorded failure", got)
	}

	// The successful delivery survives.
	inbox, err := s.Inbox("specifier")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Error("the successful delivery was rolled back")
	}

	failed, err := s.List("architect", BoxFailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 {
		t.Fatalf("failed box holds %d items", len(failed))
	}
	reason, err := os.ReadFile(failed[0].Path + ".reason")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reason), "delivered to 1 of 2") {
		t.Errorf("reason does not record the partial outcome: %q", reason)
	}
}

func TestScanIsQuietWhenNothingToDo(t *testing.T) {
	d, _, n := newTestDaemon(t)

	if got := d.Scan(); got.Delivered != 0 || got.Rejected != 0 || got.Failed != 0 {
		t.Errorf("scan = %+v, want zero", got)
	}
	if len(n.seen()) != 0 {
		t.Error("an agent was woken with no deliveries")
	}
}

func TestScanDeliversAroundTheRing(t *testing.T) {
	d, s, n := newTestDaemon(t)

	ring := [][2]string{
		{"specifier", "coder"},
		{"coder", "refactorer"},
		{"refactorer", "architect"},
		{"architect", "specifier"},
	}

	for _, hop := range ring {
		h := validNote()
		h.From, h.To = hop[0], []string{hop[1]}
		if _, err := s.Send(h); err != nil {
			t.Fatal(err)
		}
	}

	if got := d.Scan(); got.Delivered != 4 || got.Sent != 4 {
		t.Fatalf("scan = %+v, want four deliveries", got)
	}

	for _, hop := range ring {
		inbox, err := s.Inbox(hop[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) != 1 || inbox[0].From != hop[0] {
			t.Errorf("%s inbox = %+v, want one message from %s", hop[1], inbox, hop[0])
		}
	}

	if woken := n.seen(); len(woken) != 4 {
		t.Errorf("woke %v, want all four roles", woken)
	}
}

func TestScanRejectsMalformedFileAndKeepsGoing(t *testing.T) {
	d, s, _ := newTestDaemon(t)

	outbox, err := s.OutboxDir("coder")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outbox, "garbage.handoff"), []byte("this is not a handoff"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Rejected != 1 || got.Delivered != 1 {
		t.Fatalf("scan = %+v, want 1 rejected and 1 delivered", got)
	}

	if again := d.Scan(); again.Delivered != 0 || again.Rejected != 0 || again.Failed != 0 {
		t.Errorf("second scan = %+v, want zero", again)
	}
}

func TestScanRejectsInvalidHandoffs(t *testing.T) {
	cases := map[string]string{
		"unknown destination": "type: note\nfrom: coder\nto: foo\npriority: 10\nnote: hi\n",
		"unknown type":        "type: explode\nfrom: coder\nto: architect\npriority: 10\nnote: hi\n",
		"missing commit":      "type: git_handoff\nfrom: coder\nto: architect\ntask: T-1\npriority: 10\nnote: hi\n",
		"short commit":        "type: git_handoff\nfrom: coder\nto: architect\ntask: T-1\ncommit: abc123\npriority: 10\nnote: hi\n",
		"sender mismatch":     "type: note\nfrom: architect\nto: specifier\npriority: 10\nnote: hi\n",
		"bad priority":        "type: note\nfrom: coder\nto: architect\npriority: 999\nnote: hi\n",
		"missing note":        "type: note\nfrom: coder\nto: architect\npriority: 10\n",
	}

	for name, body := range cases {
		d, s, n := newTestDaemon(t)

		outbox, err := s.OutboxDir("coder")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outbox, "x.handoff"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		got := d.Scan()
		if got.Rejected != 1 || got.Delivered != 0 {
			t.Errorf("%s: scan = %+v, want 1 rejected", name, got)
		}
		if len(n.seen()) != 0 {
			t.Errorf("%s: an agent was woken for a rejected message", name)
		}

		items, err := os.ReadDir(s.RejectedDir())
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, item := range items {
			if strings.HasSuffix(item.Name(), ".reason") {
				data, err := os.ReadFile(filepath.Join(s.RejectedDir(), item.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if len(strings.TrimSpace(string(data))) > 0 {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no reason recorded", name)
		}
	}
}

func TestScanRejectsOutboxMismatch(t *testing.T) {
	d, s, _ := newTestDaemon(t)

	outbox, err := s.OutboxDir("coder")
	if err != nil {
		t.Fatal(err)
	}
	body := "type: note\nfrom: specifier\nto: architect\npriority: 10\nnote: forged\n"
	if err := os.WriteFile(filepath.Join(outbox, "forged.handoff"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := d.Scan(); got.Rejected != 1 || got.Delivered != 0 {
		t.Fatalf("scan = %+v, want 1 rejected", got)
	}

	inbox, err := s.Inbox("architect")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Error("a forged handoff was delivered")
	}
}

// Re-scanning the same logical handoff must not create a second inbox copy.
func TestScanDoesNotDuplicateOnRepeatedDelivery(t *testing.T) {
	d, s, _ := newTestDaemon(t)

	entry, err := s.Send(validGit())
	if err != nil {
		t.Fatal(err)
	}

	if got := d.Scan(); got.Delivered != 1 {
		t.Fatalf("first scan = %+v", got)
	}

	// Simulate a crash between delivery and retiring the source: put the
	// outbox file back and scan again.
	sent, err := s.List("coder", BoxSent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTo(sent[0].Path, "coder", BoxOutbox); err != nil {
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Duplicate != 1 || got.Delivered != 0 {
		t.Fatalf("second scan = %+v, want the delivery recognised as duplicate", got)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox holds %d copies, want 1", len(inbox))
	}
	if inbox[0].ID != entry.ID {
		t.Errorf("inbox id = %q, want %q", inbox[0].ID, entry.ID)
	}
}

func TestScanSurvivesNotifierFailure(t *testing.T) {
	d, s, n := newTestDaemon(t)
	n.err = io.ErrUnexpectedEOF

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	if got := d.Scan(); got.Delivered != 1 || got.Sent != 1 {
		t.Fatalf("scan = %+v, want the delivery to succeed anyway", got)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Error("message lost when the notifier failed")
	}
}

func TestScanWithoutResolverFailsGitHandoffs(t *testing.T) {
	s := newTestStore(t)
	d := NewDaemon(s, []string{"coder", "refactorer"}, nil, nil)
	d.Log = io.Discard

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	if got := d.Scan(); got.Failed != 1 || got.Delivered != 0 {
		t.Fatalf("scan = %+v, want the git handoff to fail safely", got)
	}
}

// Notes still flow when no resolver is configured.
func TestScanWithoutResolverDeliversNotes(t *testing.T) {
	s := newTestStore(t)
	d := NewDaemon(s, []string{"architect", "specifier"}, nil, nil)
	d.Log = io.Discard

	if _, err := s.Send(validNote()); err != nil {
		t.Fatal(err)
	}

	if got := d.Scan(); got.Delivered != 1 {
		t.Fatalf("scan = %+v, want the note delivered", got)
	}
}

func TestDaemonRunStopsOnContextCancel(t *testing.T) {
	d, s, _ := newTestDaemon(t)
	d.Interval = 5 * time.Millisecond

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		inbox, err := s.Inbox("refactorer")
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("daemon did not deliver within the timeout")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop after cancellation")
	}
}

func TestDaemonRunCreatesDirectories(t *testing.T) {
	s := NewStore(t.TempDir(), testRoles())
	d := NewDaemon(s, []string{"coder", "refactorer"}, nil, nil)
	d.Log = io.Discard
	d.Interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.Run(ctx); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{
		filepath.Join(s.Root, "coder", BoxOutbox),
		filepath.Join(s.Root, "coder", BoxSent),
		filepath.Join(s.Root, "coder", BoxFailed),
		filepath.Join(s.Root, "refactorer", BoxCurrent),
		filepath.Join(s.Root, "refactorer", BoxCompleted),
		s.RejectedDir(),
	} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
}
