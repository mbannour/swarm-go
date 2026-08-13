// Package prompt loads the shared constitution and per-role instructions from
// disk and assembles the effective prompt for a role. It knows nothing about
// tmux or any particular agent backend.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir is the repository-relative home of the prompt sources.
const Dir = "prompts"

// PromptSet is the raw prompt material for one role.
type PromptSet struct {
	Role         string
	Constitution string
	Instructions string
}

// ConstitutionPath returns the repository-relative path of the shared prompt.
func ConstitutionPath() string {
	return Dir + "/constitution.prompt"
}

// RolePath returns the repository-relative path of a role's prompt.
func RolePath(role string) string {
	return Dir + "/roles/" + role + ".prompt"
}

// validRole rejects names that could escape the prompts directory.
func validRole(role string) error {
	switch {
	case role == "":
		return fmt.Errorf("empty role name")
	case role == "." || role == "..":
		return fmt.Errorf("invalid role name %q", role)
	case strings.ContainsAny(role, `/\`):
		return fmt.Errorf("role name %q must not contain a path separator", role)
	}
	return nil
}

// LoadForRole reads the constitution and the role prompt beneath root.
func LoadForRole(root, role string) (PromptSet, error) {
	if err := validRole(role); err != nil {
		return PromptSet{}, err
	}

	constitution, err := readPrompt(root, ConstitutionPath())
	if err != nil {
		return PromptSet{}, err
	}

	instructions, err := readPrompt(root, RolePath(role))
	if err != nil {
		return PromptSet{}, err
	}

	return PromptSet{Role: role, Constitution: constitution, Instructions: instructions}, nil
}

// readPrompt reads one prompt file, reporting the relative path on failure.
func readPrompt(root, rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s does not exist", rel)
		}
		return "", fmt.Errorf("read %s: %w", rel, err)
	}

	body := strings.TrimSpace(string(data))
	if body == "" {
		return "", fmt.Errorf("%s is empty", rel)
	}

	return body, nil
}
