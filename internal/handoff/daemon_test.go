package handoff

import (
	"context"
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

func newTestDaemon(t *testing.T) (*Daemon, *Store, *recorder) {
	t.Helper()

	s := newTestStore(t)
	n := &recorder{}

	d := NewDaemon(s, []string{"specifier", "coder", "refactorer", "architect"}, n)
	d.Log = io.Discard

	return d, s, n
}

func TestScanDeliversAndNotifies(t *testing.T) {
	d, s, n := newTestDaemon(t)

	if _, err := s.Send(validGit()); err != nil { // coder -> refactorer
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Delivered != 1 || got.Rejected != 0 {
		t.Fatalf("scan = %+v, want 1 delivered", got)
	}

	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Commit != "71ae82cc13" {
		t.Fatalf("refactorer inbox = %+v", inbox)
	}

	if woken := n.seen(); len(woken) != 1 || woken[0] != "refactorer" {
		t.Errorf("woken = %v, want [refactorer]", woken)
	}
}

func TestScanIsQuietWhenNothingToDo(t *testing.T) {
	d, _, n := newTestDaemon(t)

	if got := d.Scan(); got.Delivered != 0 || got.Rejected != 0 {
		t.Errorf("scan = %+v, want zero", got)
	}
	if len(n.seen()) != 0 {
		t.Error("an agent was woken with no deliveries")
	}
}

// The whole chain: specifier -> coder -> refactorer -> architect.
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
		h.From, h.To = hop[0], hop[1]
		if _, err := s.Send(h); err != nil {
			t.Fatal(err)
		}
	}

	if got := d.Scan(); got.Delivered != 4 {
		t.Fatalf("scan = %+v, want 4 delivered", got)
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

	// A valid message alongside the broken one must still be delivered.
	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	got := d.Scan()
	if got.Rejected != 1 || got.Delivered != 1 {
		t.Fatalf("scan = %+v, want 1 rejected and 1 delivered", got)
	}

	// A second scan must find nothing left over and still not panic.
	if again := d.Scan(); again.Delivered != 0 || again.Rejected != 0 {
		t.Errorf("second scan = %+v, want zero", again)
	}
}

func TestScanRejectsInvalidHandoffs(t *testing.T) {
	cases := map[string]string{
		"unknown destination": "type: note\nfrom: coder\nto: foo\npriority: 10\nnote: hi\n",
		"unknown type":        "type: explode\nfrom: coder\nto: architect\npriority: 10\nnote: hi\n",
		"missing commit":      "type: git_handoff\nfrom: coder\nto: architect\ntask: T-1\npriority: 10\nnote: hi\n",
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

		// The reason must be recorded next to the quarantined file.
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

// A handoff whose sender does not own the outbox it sits in must be rejected,
// even though the message itself parses and names configured roles.
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

func TestScanSurvivesNotifierFailure(t *testing.T) {
	d, s, n := newTestDaemon(t)
	n.err = io.ErrUnexpectedEOF

	if _, err := s.Send(validGit()); err != nil {
		t.Fatal(err)
	}

	if got := d.Scan(); got.Delivered != 1 {
		t.Fatalf("scan = %+v, want the delivery to succeed anyway", got)
	}

	// Durability does not depend on the notification.
	inbox, err := s.Inbox("refactorer")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Error("message lost when the notifier failed")
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

	// Wait for the delivery, then shut down.
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
	d := NewDaemon(s, []string{"coder", "refactorer"}, nil)
	d.Log = io.Discard
	d.Interval = time.Hour // never actually ticks

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.Run(ctx); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{
		filepath.Join(s.Root, "coder", "outbox"),
		filepath.Join(s.Root, "refactorer", "inbox"),
		s.RejectedDir(),
	} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
}
