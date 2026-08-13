package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is the repository-relative home of all handoff state.
const Dir = ".swarm/handoffs"

// Role-owned boxes.
const (
	BoxInbox     = "inbox"     // delivered, not yet accepted
	BoxOutbox    = "outbox"    // queued for the daemon
	BoxSent      = "sent"      // delivered to every destination
	BoxFailed    = "failed"    // valid request that could not be completed
	BoxCurrent   = "current"   // accepted, being worked on
	BoxCompleted = "completed" // finished
)

// roleBoxes is every per-role directory.
var roleBoxes = []string{BoxInbox, BoxOutbox, BoxSent, BoxFailed, BoxCurrent, BoxCompleted}

// Shared directories that belong to no role.
const (
	rejectedDir = "rejected" // the message itself is invalid
	archiveDir  = "archive"  // acknowledged via the legacy ack command
)

// batchMarker records the batch id of an active batch. The leading dot keeps
// it out of handoff listings.
const batchMarker = ".batch"

// Store owns every filesystem operation on the handoff tree. Only configured
// roles can name a directory here.
type Store struct {
	Root  string // absolute path of .swarm/handoffs
	Roles Roles
	Now   Clock
}

// NewStore returns a store rooted at repoRoot/.swarm/handoffs.
func NewStore(repoRoot string, roles Roles) *Store {
	return &Store{
		Root:  filepath.Join(repoRoot, filepath.FromSlash(Dir)),
		Roles: roles,
		Now:   time.Now,
	}
}

// Box resolves a role-owned directory, refusing anything unconfigured. This is
// the only place a role name becomes a path component.
func (s *Store) Box(role, box string) (string, error) {
	if !s.Roles.Has(role) {
		return "", fmt.Errorf("role %q is not configured", role)
	}
	// Belt and braces: a configured role must still be a single path segment.
	if role == "" || role == "." || role == ".." || strings.ContainsAny(role, `/\`) {
		return "", fmt.Errorf("invalid role name %q", role)
	}

	valid := false
	for _, b := range roleBoxes {
		if b == box {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("unknown box %q", box)
	}

	return filepath.Join(s.Root, role, box), nil
}

// InboxDir is the absolute inbox path of a role.
func (s *Store) InboxDir(role string) (string, error) { return s.Box(role, BoxInbox) }

// OutboxDir is the absolute outbox path of a role.
func (s *Store) OutboxDir(role string) (string, error) { return s.Box(role, BoxOutbox) }

// RejectedDir is where invalid messages are quarantined.
func (s *Store) RejectedDir() string { return filepath.Join(s.Root, rejectedDir) }

// ArchiveDir is where acknowledged messages are kept.
func (s *Store) ArchiveDir() string { return filepath.Join(s.Root, archiveDir) }

// EnsureDirs creates the whole tree. It is idempotent.
func (s *Store) EnsureDirs(roles []string) error {
	dirs := []string{s.RejectedDir(), s.ArchiveDir()}

	for _, role := range roles {
		for _, box := range roleBoxes {
			dir, err := s.Box(role, box)
			if err != nil {
				return err
			}
			dirs = append(dirs, dir)
		}
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	return nil
}

// OutboxName is the file name of a queued logical handoff.
func OutboxName(created time.Time, id, from string) string {
	return fmt.Sprintf("%s-%s-%s%s", created.UTC().Format(timeFormat), id, from, FileExt)
}

// DeliveryName is the file name of one destination's copy. It embeds the
// handoff id, which is what makes a repeated delivery detectable.
func DeliveryName(created time.Time, id, from, to string) string {
	return fmt.Sprintf("%s-%s-%s-to-%s%s", created.UTC().Format(timeFormat), id, from, to, FileExt)
}

// Send stamps a handoff with generated metadata and writes it into the
// sender's outbox atomically: content is written to a temporary file in the
// same directory, synced, then renamed, so a scanning daemon never observes a
// partial file.
func (s *Store) Send(h Handoff) (Entry, error) {
	// Lifecycle metadata is generated here, never accepted from the caller.
	id, err := NewID()
	if err != nil {
		return Entry{}, err
	}
	h.ID = id
	h.CreatedAt = s.Now().UTC()
	h.DeliveredAt = time.Time{}
	h.CanonicalCommit = ""

	if err := Validate(h, s.Roles, h.From); err != nil {
		return Entry{}, err
	}

	dir, err := s.OutboxDir(h.From)
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Entry{}, err
	}

	name := OutboxName(h.CreatedAt, h.ID, h.From)
	path := filepath.Join(dir, name)

	if err := writeFileAtomic(path, []byte(Marshal(h))); err != nil {
		return Entry{}, err
	}

	return Entry{Handoff: h, Name: name, Path: path}, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	defer func() {
		// Harmless when the rename already succeeded.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}

	return os.Rename(tmpName, path)
}

// list reads every handoff file in a directory, sorted by priority descending
// and then by file name. Unparsable files are returned in bad.
func (s *Store) list(dir string) (entries []Entry, bad []string, err error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	for _, item := range items {
		name := item.Name()
		if item.IsDir() || !strings.HasSuffix(name, FileExt) || strings.HasPrefix(name, ".") {
			continue
		}

		path := filepath.Join(dir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			bad = append(bad, name)
			continue
		}

		h, parseErr := Unmarshal(data)
		if parseErr != nil {
			bad = append(bad, name)
			continue
		}

		entries = append(entries, Entry{Handoff: h, Name: name, Path: path})
	}

	SortEntries(entries)
	sort.Strings(bad)

	return entries, bad, nil
}

// SortEntries orders by priority descending, then by name. Names begin with a
// creation timestamp, so ties resolve oldest-first, and the trailing id makes
// the order total even for identical timestamps.
func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Priority != entries[j].Priority {
			return entries[i].Priority > entries[j].Priority
		}
		return entries[i].Name < entries[j].Name
	})
}

// List returns the parsable entries of one of a role's boxes.
func (s *Store) List(role, box string) ([]Entry, error) {
	dir, err := s.Box(role, box)
	if err != nil {
		return nil, err
	}
	entries, _, err := s.list(dir)
	return entries, err
}

// Inbox lists a role's delivered, unaccepted messages.
func (s *Store) Inbox(role string) ([]Entry, error) { return s.List(role, BoxInbox) }

// Outbox lists a role's undelivered messages.
func (s *Store) Outbox(role string) ([]Entry, error) { return s.List(role, BoxOutbox) }

// Current lists a role's accepted, in-progress work.
func (s *Store) Current(role string) ([]Entry, error) { return s.List(role, BoxCurrent) }

// HasDelivery reports whether a handoff id is already present anywhere in the
// destination's receive-side lifecycle. This is what makes delivery idempotent:
// a repeated attempt after a crash or retry finds the earlier copy instead of
// creating a second one.
func (s *Store) HasDelivery(role, id string) (bool, error) {
	if id == "" {
		return false, nil
	}

	for _, box := range []string{BoxInbox, BoxCurrent, BoxCompleted} {
		dir, err := s.Box(role, box)
		if err != nil {
			return false, err
		}

		items, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}

		for _, item := range items {
			if strings.Contains(item.Name(), "-"+id+"-") {
				return true, nil
			}
		}
	}

	return false, nil
}

// Deliver writes one destination's copy into its inbox, stamping delivered_at
// and any canonical commit. It is idempotent: if the id is already present in
// that role's inbox, current or completed, nothing is written.
//
// The source file is not consumed here — the daemon decides where the logical
// handoff goes once every destination has been attempted.
func (s *Store) Deliver(h Handoff, to string) (path string, already bool, err error) {
	dir, err := s.Box(to, BoxInbox)
	if err != nil {
		return "", false, err
	}

	exists, err := s.HasDelivery(to, h.ID)
	if err != nil {
		return "", false, err
	}
	if exists {
		return "", true, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}

	// Each destination gets its own copy with its own lifecycle.
	copyForDest := h
	copyForDest.To = []string{to}
	copyForDest.DeliveredAt = s.Now().UTC()

	name := DeliveryName(h.CreatedAt, h.ID, h.From, to)
	dst := filepath.Join(dir, name)

	if err := writeFileAtomic(dst, []byte(Marshal(copyForDest))); err != nil {
		return "", false, err
	}

	return dst, false, nil
}

// MoveTo moves a file into one of a role's boxes, keeping its name.
func (s *Store) MoveTo(path, role, box string) (string, error) {
	dir, err := s.Box(role, box)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, err := uniqueName(dir, filepath.Base(path))
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)

	if err := moveFile(path, dst); err != nil {
		return "", err
	}

	return dst, nil
}

// moveFile renames when possible and falls back to atomic copy + remove when
// the move crosses a filesystem boundary.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(dst, data); err != nil {
		return err
	}

	return os.Remove(src)
}

// uniqueName keeps a file name unless it is already taken.
func uniqueName(dir, name string) (string, error) {
	base := filepath.Base(name)

	candidate := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
		if i > 1000 {
			return "", fmt.Errorf("cannot find a free file name in %s", dir)
		}
		candidate = strings.TrimSuffix(base, FileExt) + fmt.Sprintf("-%d", i) + FileExt
	}
}

// Reject quarantines an invalid message and records why, next to it as
// <name>.reason. Rejection means the message itself is at fault.
func (s *Store) Reject(path, reason string) (string, error) {
	return s.quarantine(s.RejectedDir(), path, reason)
}

// Fail moves a valid outbound request that could not be completed into the
// sender's failed box, with the reason beside it.
func (s *Store) Fail(role, path, reason string) (string, error) {
	dir, err := s.Box(role, BoxFailed)
	if err != nil {
		return "", err
	}
	return s.quarantine(dir, path, reason)
}

// quarantine moves a file into dir and writes its reason file.
func (s *Store) quarantine(dir, path, reason string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, err := uniqueName(dir, filepath.Base(path))
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)

	if err := moveFile(path, dst); err != nil {
		return "", err
	}

	stamped := fmt.Sprintf("%s\n%s\n", s.Now().UTC().Format(time.RFC3339), reason)
	if err := writeFileAtomic(dst+".reason", []byte(stamped)); err != nil {
		return "", err
	}

	return dst, nil
}

// BatchID returns the id of a role's active batch, or "" when there is none.
func (s *Store) BatchID(role string) (string, error) {
	dir, err := s.Box(role, BoxCurrent)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(filepath.Join(dir, batchMarker))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// SetBatchID records the active batch id.
func (s *Store) SetBatchID(role, id string) error {
	dir, err := s.Box(role, BoxCurrent)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, batchMarker), []byte(id+"\n"))
}

// ClearBatchID forgets the active batch id.
func (s *Store) ClearBatchID(role string) error {
	dir, err := s.Box(role, BoxCurrent)
	if err != nil {
		return err
	}

	err = os.Remove(filepath.Join(dir, batchMarker))
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// Ack moves a message out of a role's inbox into the archive.
//
// Deprecated: this is the Step 6 lifecycle. Prefer ready/done, which move work
// through current/ and completed/. Kept so existing scripts keep working.
func (s *Store) Ack(role, name string) (string, error) {
	dir, err := s.Box(role, BoxInbox)
	if err != nil {
		return "", err
	}

	// The caller supplies this name; never let it walk out of the inbox.
	if name != filepath.Base(name) || name == "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid handoff name %q", name)
	}
	if !strings.HasSuffix(name, FileExt) {
		return "", fmt.Errorf("%q is not a handoff file", name)
	}

	src := filepath.Join(dir, name)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("no handoff %q in %s inbox", name, role)
	}

	if err := os.MkdirAll(s.ArchiveDir(), 0o755); err != nil {
		return "", err
	}

	dstName, err := uniqueName(s.ArchiveDir(), name)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(s.ArchiveDir(), dstName)

	if err := moveFile(src, dst); err != nil {
		return "", err
	}

	return dst, nil
}
