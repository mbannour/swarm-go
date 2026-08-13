package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// ErrNotInstalled is returned when no tmux binary can be found.
var ErrNotInstalled = errors.New("tmux is not installed or not available in PATH")

// Available reports whether a tmux binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// run executes a non-interactive tmux command on the given socket and returns
// its trimmed stdout. Failures carry tmux's stderr rather than "exit status 1".
func run(socket string, args ...string) (string, error) {
	if !Available() {
		return "", ErrNotInstalled
	}

	full := append([]string{"-S", socket}, args...)
	cmd := exec.Command("tmux", full...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), msg)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// socketUser returns a filesystem-safe identifier for the current user.
func socketUser() string {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" {
		name = "unknown"
	}

	// Windows usernames may be "DOMAIN\user"; keep the path single-segment.
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)

	return name
}

// ensureSocketDir creates the socket's parent directory, private to this user.
func ensureSocketDir(socket string) error {
	return os.MkdirAll(filepath.Dir(socket), 0o700)
}
