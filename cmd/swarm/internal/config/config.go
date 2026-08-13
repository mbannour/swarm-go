package config

import "fmt"

type ReceiveMode string

const (
	ReceiveTask  ReceiveMode = "task"
	ReceiveBatch ReceiveMode = "batch"
)

type RoleConfig struct {
	Name        string
	Backend     string
	Worktree    string
	ReceiveMode ReceiveMode
}

type Config struct {
	Roles []RoleConfig
}

func (c Config) ValidateFourPack() error {
	required := map[string]bool{
		"specifier":  false,
		"coder":      false,
		"refactorer": false,
		"architect":  false,
	}

	for _, role := range c.Roles {
		if _, exists := required[role.Name]; exists {
			required[role.Name] = true
		}
	}

	for role, found := range required {
		if !found {
			return fmt.Errorf(
				"four-pack requires role %q",
				role,
			)
		}
	}

	return nil
}
