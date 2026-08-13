package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	cfg := &Config{}

	scanner := bufio.NewScanner(file)

	lineNumber := 0

	for scanner.Scan() {
		lineNumber++

		line := strings.TrimSpace(scanner.Text())

		// Ignore empty lines.
		if line == "" {
			continue
		}

		// Ignore comments.
		if strings.HasPrefix(line, "#") {
			continue
		}

		role, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf(
				"config line %d: %w",
				lineNumber,
				err,
			)
		}

		cfg.Roles = append(cfg.Roles, role)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return cfg, nil
}

func parseLine(line string) (RoleConfig, error) {
	fields := strings.Fields(line)

	if len(fields) != 5 {
		return RoleConfig{}, fmt.Errorf(
			"expected 5 fields, got %d",
			len(fields),
		)
	}

	if fields[0] != "window" {
		return RoleConfig{}, fmt.Errorf(
			"unknown directive %q",
			fields[0],
		)
	}

	mode := ReceiveMode(fields[4])

	switch mode {
	case ReceiveTask, ReceiveBatch:
		// valid
	default:
		return RoleConfig{}, fmt.Errorf(
			"invalid receive mode %q",
			fields[4],
		)
	}

	return RoleConfig{
		Name:        fields[1],
		Backend:     fields[2],
		Worktree:    fields[3],
		ReceiveMode: mode,
	}, nil
}
