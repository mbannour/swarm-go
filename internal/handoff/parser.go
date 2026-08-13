package handoff

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// The wire format is one `key: value` per line, in a fixed order so that files
// stay easy to read and diff:
//
//	type: git_handoff
//	from: coder
//	to: refactorer
//	task: AUTH-42
//	commit: 71ae82cc13
//	priority: 20
//	note: Implementation complete; tests pass.
//
// `note` must come last: every remaining line belongs to it, so notes may span
// multiple lines. Blank lines and `#` comments before `note` are ignored.

// fieldOrder is the order in which Marshal emits keys.
var fieldOrder = []string{"type", "from", "to", "task", "commit", "priority", "note"}

// Marshal renders a handoff in the wire format.
func Marshal(h Handoff) string {
	var b strings.Builder

	for _, key := range fieldOrder {
		switch key {
		case "type":
			fmt.Fprintf(&b, "type: %s\n", h.Type)
		case "from":
			fmt.Fprintf(&b, "from: %s\n", h.From)
		case "to":
			fmt.Fprintf(&b, "to: %s\n", h.To)
		case "task":
			if h.Task != "" {
				fmt.Fprintf(&b, "task: %s\n", h.Task)
			}
		case "commit":
			if h.Commit != "" {
				fmt.Fprintf(&b, "commit: %s\n", h.Commit)
			}
		case "priority":
			fmt.Fprintf(&b, "priority: %d\n", h.Priority)
		case "note":
			if h.Note != "" {
				fmt.Fprintf(&b, "note: %s\n", h.Note)
			}
		}
	}

	return b.String()
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
		case "type":
			h.Type = Type(value)
		case "from":
			h.From = value
		case "to":
			h.To = value
		case "task":
			h.Task = value
		case "commit":
			h.Commit = value
		case "priority":
			n, err := strconv.Atoi(value)
			if err != nil {
				return Handoff{}, fmt.Errorf("line %d: priority %q is not a number", line, value)
			}
			h.Priority = n
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
