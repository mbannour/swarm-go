package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
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

// RepoRoot returns the top level of the main working tree of the repository
// containing dir.
//
// The subtlety: agents run inside linked worktrees, and `rev-parse
// --show-toplevel` would answer with the *worktree* there. Every managed
// resource — .swarm/handoffs, .swarm/runtime, the tmux socket — belongs to the
// main working tree, so a role running `swarm handoff ready` from
// .swarm/worktrees/wt-coder must still resolve to the project itself.
// --git-common-dir points at the shared .git directory from anywhere, so its
// parent is the main working tree.
func RepoRoot(dir string) (string, error) {
	common, err := run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Older Git without --path-format: fall back to resolving by hand.
		common, err = run(dir, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", fmt.Errorf("not inside a git repository: %w", err)
		}
		if !filepath.IsAbs(common) {
			abs, absErr := filepath.Abs(filepath.Join(dir, common))
			if absErr != nil {
				return "", absErr
			}
			common = abs
		}
	}

	root := filepath.Dir(filepath.Clean(common))

	// A bare repository has no working tree to manage.
	if filepath.Base(common) != ".git" {
		top, topErr := run(dir, "rev-parse", "--show-toplevel")
		if topErr != nil {
			return "", fmt.Errorf("not inside a git repository: %w", topErr)
		}
		return top, nil
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
