package handoff

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The wire format is one `key: value` per line, in a fixed order so files stay
// easy to read and diff:
//
//	id: 9f2c1d7a5b3e4f60a1c2d3e4f5061728
//	type: git_handoff
//	from: coder
//	to: refactorer,architect
//	task: AUTH-42
//	commit: 71ae82cc13
//	canonical_commit: 71ae82cc13a9f0e4d8c7b6a5049382716f5e4d3c
//	priority: 20
//	created_at: 2026-08-13T21:05:01.123456789Z
//	delivered_at: 2026-08-13T21:05:01.456789012Z
//	note: Implementation complete; tests pass.
//
// `to` is a comma-separated list of roles. `note` must come last: every
// remaining line belongs to it, so notes may span multiple lines. Blank lines
// and `#` comments before `note` are ignored.

// Marshal renders a handoff in the wire format.
func Marshal(h Handoff) string {
	var b strings.Builder

	writeIf := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", key, value)
		}
	}

	writeIf("id", h.ID)
	writeIf("source_handoff_id", h.SourceID)
	fmt.Fprintf(&b, "type: %s\n", h.Type)
	fmt.Fprintf(&b, "from: %s\n", h.From)
	fmt.Fprintf(&b, "to: %s\n", strings.Join(h.To, ","))
	writeIf("task", h.Task)
	writeIf("commit", h.Commit)
	writeIf("canonical_commit", h.CanonicalCommit)
	fmt.Fprintf(&b, "priority: %d\n", h.Priority)
	writeIf("created_at", formatTime(h.CreatedAt))
	writeIf("delivered_at", formatTime(h.DeliveredAt))
	writeIf("note", h.Note)

	return b.String()
}

// formatTime renders a timestamp, or "" for the zero value.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// Unmarshal parses the wire format. It reports syntax errors only; semantic
// rules (known roles, required fields) live in Validate.
func Unmarshal(data []byte) (Handoff, error) {
	var h Handoff

	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var noteLines []string
	inNote := false

	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()

		// Once note has started, every remaining line is note content.
		if inNote {
			noteLines = append(noteLines, text)
			continue
		}

		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, value, ok := splitField(trimmed)
		if !ok {
			return Handoff{}, fmt.Errorf("line %d: expected \"key: value\", got %q", line, trimmed)
		}
		if seen[key] {
			return Handoff{}, fmt.Errorf("line %d: duplicate field %q", line, key)
		}
		seen[key] = true

		switch key {
		case "id":
			h.ID = value
		case "source_handoff_id":
			h.SourceID = value
		case "type":
			h.Type = Type(value)
		case "from":
			h.From = value
		case "to":
			h.To = splitRoles(value)
		case "task":
			h.Task = value
		case "commit":
			h.Commit = value
		case "canonical_commit":
			h.CanonicalCommit = value
		case "priority":
			n, err := strconv.Atoi(value)
			if err != nil {
				return Handoff{}, fmt.Errorf("line %d: priority %q is not a number", line, value)
			}
			h.Priority = n
		case "created_at":
			t, err := parseTime(value)
			if err != nil {
				return Handoff{}, fmt.Errorf("line %d: created_at: %w", line, err)
			}
			h.CreatedAt = t
		case "delivered_at":
			t, err := parseTime(value)
			if err != nil {
				return Handoff{}, fmt.Errorf("line %d: delivered_at: %w", line, err)
			}
			h.DeliveredAt = t
		case "note":
			noteLines = append(noteLines, value)
			inNote = true
		default:
			return Handoff{}, fmt.Errorf("line %d: unknown field %q", line, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return Handoff{}, err
	}

	h.Note = strings.TrimRight(strings.Join(noteLines, "\n"), "\n")

	return h, nil
}

// splitRoles parses a comma-separated destination list, dropping empties.
func splitRoles(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseTime accepts RFC3339, with or without sub-second precision.
func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC3339 timestamp", value)
	}
	return t.UTC(), nil
}

// splitField splits "key: value" on the first colon.
func splitField(line string) (key, value string, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}

	key = strings.ToLower(strings.TrimSpace(line[:i]))
	value = strings.TrimSpace(line[i+1:])

	if key == "" {
		return "", "", false
	}

	return key, value, true
}
