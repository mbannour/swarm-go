package prompt

import (
	"fmt"
	"strings"
)

// RuntimeContext is the concise, machine-derived context handed to an agent
// alongside its static instructions.
type RuntimeContext struct {
	Role        string
	RepoRoot    string
	Worktree    string
	Branch      string
	ReceiveMode string
}

// Assemble builds the effective prompt: shared constitution, then the role
// instructions, then runtime context.
func Assemble(set PromptSet, ctx RuntimeContext) string {
	var b strings.Builder

	b.WriteString("# Swarm constitution\n\n")
	b.WriteString(set.Constitution)
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("# Role: %s\n\n", set.Role))
	b.WriteString(set.Instructions)
	b.WriteString("\n\n")

	b.WriteString("# Runtime context\n\n")
	for _, kv := range [][2]string{
		{"role", ctx.Role},
		{"repository", ctx.RepoRoot},
		{"worktree", ctx.Worktree},
		{"branch", ctx.Branch},
		{"receive mode", ctx.ReceiveMode},
	} {
		if kv[1] != "" {
			b.WriteString(fmt.Sprintf("- %s: %s\n", kv[0], kv[1]))
		}
	}

	return b.String()
}
