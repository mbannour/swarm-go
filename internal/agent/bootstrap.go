package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Readiness is whether a backend can actually start working for a role.
//
// "The process exists" is not readiness: an agent sitting at a trust prompt is
// running and useless.
type Readiness string

const (
	ReadinessReady           Readiness = "ready"
	ReadinessBlockedTrust    Readiness = "blocked-trust"
	ReadinessBlockedApproval Readiness = "blocked-approval"
	ReadinessNotAuthed       Readiness = "not-authenticated"
	ReadinessMissing         Readiness = "missing"
	ReadinessUnknown         Readiness = "unknown"
)

// Bootstrapper is a backend that needs preparing before unattended use.
type Bootstrapper interface {
	// Ready reports whether the backend can run unattended for a repository
	// under a policy, and why not when it cannot.
	Ready(repoRoot string, policy Approval) (Readiness, string)
	// Bootstrap performs the explicit preparation. It must only ever do things
	// the backend officially supports, never simulate a human answering a
	// security prompt.
	Bootstrap(repoRoot string, policy Approval) (changed bool, err error)
}

// codexConfigPath is where the Codex CLI keeps its configuration.
func codexConfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex", "config.toml")
	}
	return ""
}

// Ready reports whether Codex can run unattended in a repository.
//
// Two gates matter, both observed in a real run:
//
//   - authentication: without it Codex cannot do anything;
//   - workspace trust: the first launch in an untrusted directory shows
//     "Do you trust this directory?" and waits, before the prompt is even read.
//     Codex records trust per project in ~/.codex/config.toml, keyed by the
//     repository root.
func (Codex) Ready(repoRoot string, policy Approval) (Readiness, string) {
	if !Available(Codex{}) {
		return ReadinessMissing, "codex executable not found in PATH"
	}

	caps := Codex{}.Capabilities()
	if !caps.Supports(policy) {
		return ReadinessBlockedApproval, fmt.Sprintf("codex does not support approval policy %q", policy)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		if _, statErr := os.Stat(filepath.Join(home, ".codex", "auth.json")); statErr != nil {
			return ReadinessNotAuthed, "codex has no stored credentials; run `codex login`"
		}
	}

	// Interactive use is allowed to hit the trust prompt: a human is there.
	if policy == ApprovalInteractive || policy == "" {
		return ReadinessReady, ""
	}

	trusted, err := codexTrusts(repoRoot)
	if err != nil {
		return ReadinessUnknown, err.Error()
	}
	if !trusted {
		return ReadinessBlockedTrust, fmt.Sprintf(
			"codex has not been told to trust %s; it will stop at a trust prompt.\n"+
				"run `swarm bootstrap` to record it, or open codex there once yourself",
			repoRoot)
	}

	return ReadinessReady, ""
}

// Bootstrap records the repository as a trusted Codex project.
//
// This writes the same `[projects."<path>"] trust_level = "trusted"` entry that
// answering Codex's own prompt produces. It is deliberately an explicit,
// operator-invoked command rather than something `swarm start` does quietly:
// trusting a workspace is a security decision.
func (Codex) Bootstrap(repoRoot string, policy Approval) (bool, error) {
	path := codexConfigPath()
	if path == "" {
		return false, fmt.Errorf("cannot locate the codex configuration directory")
	}

	trusted, err := codexTrusts(repoRoot)
	if err != nil {
		return false, err
	}
	if trusted {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	entry := fmt.Sprintf("\n[projects.%q]\ntrust_level = \"trusted\"\n", filepath.Clean(repoRoot))

	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	if err := os.WriteFile(path, []byte(body+entry), 0o600); err != nil {
		return false, err
	}

	return true, nil
}

// codexTrusts reports whether the Codex configuration already trusts a path.
func codexTrusts(repoRoot string) (bool, error) {
	path := codexConfigPath()
	if path == "" {
		return false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// The entry looks like: [projects."/abs/path"] followed by trust_level.
	want := fmt.Sprintf("[projects.%q]", filepath.Clean(repoRoot))

	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != want {
			continue
		}
		// Trust level is recorded in the lines that follow, before the next
		// section header.
		for _, next := range lines[i+1:] {
			trimmed := strings.TrimSpace(next)
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "trust_level") && strings.Contains(trimmed, "trusted") {
				return true, nil
			}
		}
	}

	return false, nil
}

// BootstrapperFor returns the bootstrapper for a backend, if it needs one.
func BootstrapperFor(name string) (Bootstrapper, bool) {
	backend, err := Lookup(name)
	if err != nil {
		return nil, false
	}
	b, ok := backend.(Bootstrapper)
	return b, ok
}
