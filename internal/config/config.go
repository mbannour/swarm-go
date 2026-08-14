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

// ApprovalPolicy is how much a role's agent may do without a human.
type ApprovalPolicy string

const (
	// ApprovalInteractive lets the agent ask for approval. Safe, but it stalls
	// an unattended swarm at the first command.
	ApprovalInteractive ApprovalPolicy = "interactive"
	// ApprovalAutonomous lets the agent run what it needs inside its own
	// worktree without asking. Intended for unattended operation.
	ApprovalAutonomous ApprovalPolicy = "autonomous"
	// ApprovalRestricted runs unattended but with the tightest sandbox the
	// backend supports.
	ApprovalRestricted ApprovalPolicy = "restricted"
)

// ApprovalPolicies lists every supported policy.
func ApprovalPolicies() []ApprovalPolicy {
	return []ApprovalPolicy{ApprovalInteractive, ApprovalAutonomous, ApprovalRestricted}
}

// RoleConfig is a single configured agent window.
type RoleConfig struct {
	Name        string
	Backend     string
	Worktree    string
	ReceiveMode ReceiveMode
	// Approval defaults to interactive: autonomy is opted into, never assumed.
	Approval ApprovalPolicy
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
		if len(fields) < 5 || len(fields) > 6 {
			return nil, fmt.Errorf(
				"%s:%d: window needs 4 or 5 arguments (role backend worktree mode [approval]), got %d",
				path, line, len(fields)-1)
		}

		mode := ReceiveMode(fields[4])
		if mode != ReceiveTask && mode != ReceiveBatch {
			return nil, fmt.Errorf("%s:%d: unknown receive mode %q", path, line, fields[4])
		}

		// The approval policy is optional and defaults to interactive, so
		// existing four-field configurations keep working unchanged.
		approval := ApprovalInteractive
		if len(fields) == 6 {
			approval = ApprovalPolicy(fields[5])
			known := false
			for _, p := range ApprovalPolicies() {
				if approval == p {
					known = true
				}
			}
			if !known {
				return nil, fmt.Errorf("%s:%d: unknown approval policy %q", path, line, fields[5])
			}
		}

		cfg.Roles = append(cfg.Roles, RoleConfig{
			Name:        fields[1],
			Backend:     fields[2],
			Worktree:    fields[3],
			ReceiveMode: mode,
			Approval:    approval,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return cfg, nil
}
