package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BinaryEnv lets an operator name the orchestrator binary explicitly.
const BinaryEnv = "SWARM_BIN"

// ResolveBinary returns an absolute path to a swarm executable that agents can
// keep invoking for the lifetime of their session.
//
// The subtlety: `go run ./cmd/swarm` compiles to a temporary binary that is
// deleted the moment the parent process exits, so baking that path into a
// prompt would hand every agent a command that stops working seconds later.
// Such a path is refused with instructions to build a stable one.
//
// Resolution order:
//  1. $SWARM_BIN, if set (must exist and be executable)
//  2. ./bin/swarm inside the repository, if present
//  3. the running executable, if it is not a temporary build
func ResolveBinary(repoRoot string) (string, error) {
	if env := strings.TrimSpace(os.Getenv(BinaryEnv)); env != "" {
		path, err := filepath.Abs(env)
		if err != nil {
			return "", err
		}
		if err := checkExecutable(path); err != nil {
			return "", fmt.Errorf("%s=%s: %w", BinaryEnv, env, err)
		}
		return path, nil
	}

	candidate := filepath.Join(repoRoot, "bin", "swarm")
	if err := checkExecutable(candidate); err == nil {
		return candidate, nil
	}

	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine the swarm executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	if IsTemporaryBinary(self) {
		return "", fmt.Errorf(
			"swarm is running from a temporary build (%s), which agents cannot call after this process exits\n\n"+
				"build a stable binary first:\n"+
				"  go build -o ./bin/swarm ./cmd/swarm\n"+
				"  ./bin/swarm agents start\n\n"+
				"or point %s at an existing one",
			self, BinaryEnv,
		)
	}

	return self, nil
}

// IsTemporaryBinary reports whether path looks like a `go run` scratch build.
func IsTemporaryBinary(path string) bool {
	clean := filepath.Clean(path)

	if strings.Contains(clean, string(filepath.Separator)+"go-build") {
		return true
	}

	tmp := os.TempDir()
	if tmp != "" {
		if rel, err := filepath.Rel(tmp, clean); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}

	return false
}

// checkExecutable verifies that path is a regular, executable file.
func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}
