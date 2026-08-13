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

// Sub-directories that are not role-owned.
const (
	rejectedDir = "rejected"
	archiveDir  = "archive"
)

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

// roleDir resolves a role-owned directory, refusing anything unconfigured.
// This is the only place a role name becomes a path component.
func (s *Store) roleDir(role, box string) (string, error) {
	if !s.Roles.Has(role) {
		return "", fmt.Errorf("role %q is not configured", role)
	}
	// Belt and braces: a configured role must still be a single path segment.
	if role == "" || role == "." || role == ".." || strings.ContainsAny(role, `/\`) {
		return "", fmt.Errorf("invalid role name %q", role)
	}
	return filepath.Join(s.Root, role, box), nil
}

// InboxDir is the absolute inbox path of a role.
func (s *Store) InboxDir(role string) (string, error) { return s.roleDir(role, "inbox") }

// OutboxDir is the absolute outbox path of a role.
func (s *Store) OutboxDir(role string) (string, error) { return s.roleDir(role, "outbox") }

// RejectedDir is where invalid messages are quarantined.
func (s *Store) RejectedDir() string { return filepath.Join(s.Root, rejectedDir) }

// ArchiveDir is where acknowledged messages are kept.
func (s *Store) ArchiveDir() string { return filepath.Join(s.Root, archiveDir) }

// EnsureDirs creates the whole tree. It is idempotent.
func (s *Store) EnsureDirs(roles []string) error {
	dirs := []string{s.RejectedDir(), s.ArchiveDir()}

	for _, role := range roles {
		in, err := s.InboxDir(role)
		if err != nil {
			return err
		}
		out, err := s.OutboxDir(role)
		if err != nil {
			return err
		}
		dirs = append(dirs, in, out)
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	return nil
}

// FileName builds a unique, sortable name for a handoff.
func FileName(now time.Time, from, to string) string {
	return fmt.Sprintf("%s-%s-to-%s%s", now.UTC().Format(timeFormat), from, to, FileExt)
}

// Send writes a handoff into the sender's outbox atomically: the content is
// written to a temporary file in the same directory, synced, then renamed into
// place, so a scanning daemon never observes a partial file.
func (s *Store) Send(h Handoff) (string, error) {
	if err := Validate(h, s.Roles, h.From); err != nil {
		return "", err
	}

	dir, err := s.OutboxDir(h.From)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, err := s.uniqueName(dir, h.From, h.To)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)

	if err := writeFileAtomic(path, []byte(Marshal(h))); err != nil {
		return "", err
	}

	return path, nil
}

// uniqueName picks a file name that does not already exist in dir.
func (s *Store) uniqueName(dir, from, to string) (string, error) {
	base := FileName(s.Now(), from, to)

	name := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name, nil
		} else if err != nil {
			return "", err
		}
		if i > 1000 {
			return "", fmt.Errorf("cannot find a free file name in %s", dir)
		}
		name = strings.TrimSuffix(base, FileExt) + fmt.Sprintf("-%d", i) + FileExt
	}
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

	sortEntries(entries)
	sort.Strings(bad)

	return entries, bad, nil
}

// sortEntries orders by priority descending, then by name (which is a
// timestamp, so ties resolve oldest-first).
func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Priority != entries[j].Priority {
			return entries[i].Priority > entries[j].Priority
		}
		return entries[i].Name < entries[j].Name
	})
}

// Inbox lists a role's pending messages.
func (s *Store) Inbox(role string) ([]Entry, error) {
	dir, err := s.InboxDir(role)
	if err != nil {
		return nil, err
	}
	entries, _, err := s.list(dir)
	return entries, err
}

// Outbox lists a role's undelivered messages.
func (s *Store) Outbox(role string) ([]Entry, error) {
	dir, err := s.OutboxDir(role)
	if err != nil {
		return nil, err
	}
	entries, _, err := s.list(dir)
	return entries, err
}

// Deliver moves a message from an outbox into the destination inbox with a
// single rename, which is atomic on the same filesystem. Delivery falls back to
// copy+remove only if the rename crosses a filesystem boundary.
func (s *Store) Deliver(e Entry) (string, error) {
	dir, err := s.InboxDir(e.To)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, err := s.uniqueNameFor(dir, e.Name)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)

	if err := os.Rename(e.Path, dst); err == nil {
		return dst, nil
	}

	// Cross-device: write atomically into the inbox, then drop the source.
	data, err := os.ReadFile(e.Path)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(dst, data); err != nil {
		return "", err
	}
	if err := os.Remove(e.Path); err != nil {
		return "", err
	}

	return dst, nil
}

// uniqueNameFor keeps an existing file name unless it is already taken.
func (s *Store) uniqueNameFor(dir, name string) (string, error) {
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

// Reject quarantines a file and records why, next to it as <name>.reason.
func (s *Store) Reject(path, reason string) (string, error) {
	dir := s.RejectedDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	name, err := s.uniqueNameFor(dir, filepath.Base(path))
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)

	if err := os.Rename(path, dst); err != nil {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		if writeErr := writeFileAtomic(dst, data); writeErr != nil {
			return "", writeErr
		}
		if rmErr := os.Remove(path); rmErr != nil {
			return "", rmErr
		}
	}

	if err := writeFileAtomic(dst+".reason", []byte(reason+"\n")); err != nil {
		return "", err
	}

	return dst, nil
}

// Ack moves a processed message out of a role's inbox into the archive.
func (s *Store) Ack(role, name string) (string, error) {
	dir, err := s.InboxDir(role)
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

	dstName, err := s.uniqueNameFor(s.ArchiveDir(), name)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(s.ArchiveDir(), dstName)

	if err := os.Rename(src, dst); err != nil {
		return "", err
	}

	return dst, nil
}
