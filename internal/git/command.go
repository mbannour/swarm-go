package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// run executes git in dir and returns its trimmed stdout.
//
// On failure the error carries git's stderr instead of a bare "exit status 128".
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// Available reports whether a git binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// RepoRoot returns the top level of the git repository containing dir.
func RepoRoot(dir string) (string, error) {
	root, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	return root, nil
}

// HasCommits reports whether the repository at root has at least one commit.
func HasCommits(root string) bool {
	_, err := run(root, "rev-parse", "--verify", "HEAD")
	return err == nil
}

// branchExists reports whether a local branch of that name exists.
func branchExists(root, branch string) bool {
	_, err := run(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// listWorktreePaths returns the absolute paths git currently tracks as worktrees.
func listWorktreePaths(root string) ([]string, error) {
	out, err := run(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
		}
	}

	return paths, nil
}
