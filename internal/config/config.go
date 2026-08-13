package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/mbannour/swarm-go/internal/roles"
)

// ReceiveMode is how a role consumes incoming work.
type ReceiveMode string

const (
	ReceiveTask  ReceiveMode = "task"
	ReceiveBatch ReceiveMode = "batch"
)

// RoleConfig is a single configured agent window.
type RoleConfig struct {
	Name        string
	Backend     string
	Worktree    string
	ReceiveMode ReceiveMode
}

// Config is a parsed swarm configuration file.
type Config struct {
	Roles []RoleConfig
}

// ValidateFourPack checks that the configuration declares exactly the four
// standard roles, once each, with every field populated.
func (c *Config) ValidateFourPack() error {
	want := roles.FourPack()

	if len(c.Roles) != len(want) {
		return fmt.Errorf("expected %d roles, got %d", len(want), len(c.Roles))
	}

	seen := make(map[string]bool, len(c.Roles))

	for _, r := range c.Roles {
		switch {
		case r.Backend == "":
			return fmt.Errorf("role %q: missing backend", r.Name)
		case r.Worktree == "":
			return fmt.Errorf("role %q: missing worktree", r.Name)
		case r.ReceiveMode == "":
			return fmt.Errorf("role %q: missing receive mode", r.Name)
		case seen[r.Name]:
			return fmt.Errorf("role %q declared more than once", r.Name)
		}

		seen[r.Name] = true
	}

	for _, w := range want {
		if !seen[w.Name] {
			return fmt.Errorf("missing role %q", w.Name)
		}
	}

	return nil
}

// Load reads and parses the swarm configuration at path.
//
// Each non-empty, non-comment line has the form:
//
//	window <name> <backend> <worktree> <receive-mode>
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{}
	scanner := bufio.NewScanner(f)

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		fields := strings.Fields(text)
		if fields[0] != "window" {
			return nil, fmt.Errorf("%s:%d: unknown directive %q", path, line, fields[0])
		}
		if len(fields) != 5 {
			return nil, fmt.Errorf("%s:%d: window needs 4 arguments, got %d", path, line, len(fields)-1)
		}

		mode := ReceiveMode(fields[4])
		if mode != ReceiveTask && mode != ReceiveBatch {
			return nil, fmt.Errorf("%s:%d: unknown receive mode %q", path, line, fields[4])
		}

		cfg.Roles = append(cfg.Roles, RoleConfig{
			Name:        fields[1],
			Backend:     fields[2],
			Worktree:    fields[3],
			ReceiveMode: mode,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return cfg, nil
}
