package prompt

import (
	"fmt"
	"strings"
)

// RuntimeContext is the concise, machine-derived context handed to an agent
// alongside its static instructions.
//
// It carries no secrets and no environment values: only what an agent needs to
// find its own worktree and drive its own lifecycle.
type RuntimeContext struct {
	Role        string
	RepoRoot    string
	Worktree    string
	Branch      string
	ReceiveMode string
	NextRole    string // where this role's work goes next
	SwarmBin    string // absolute path to a stable swarm executable
}

// Assemble builds the effective prompt:
//
//	constitution  → runtime protocol → role instructions → runtime context
//
// The first three come from files under prompts/; the last is generated.
func Assemble(set PromptSet, ctx RuntimeContext) string {
	var b strings.Builder

	b.WriteString("# Swarm constitution\n\n")
	b.WriteString(set.Constitution)
	b.WriteString("\n\n")

	b.WriteString("# Swarm runtime protocol\n\n")
	b.WriteString(set.Runtime)
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("# Role: %s\n\n", set.Role))
	b.WriteString(set.Instructions)
	b.WriteString("\n\n")

	b.WriteString("# Runtime context\n\n")
	b.WriteString("These values are generated for this session. Use them verbatim.\n\n")
	b.WriteString("```\n")

	for _, kv := range [][2]string{
		{"ROLE", ctx.Role},
		{"REPOSITORY_ROOT", ctx.RepoRoot},
		{"WORKTREE", ctx.Worktree},
		{"BRANCH", ctx.Branch},
		{"RECEIVE_MODE", ctx.ReceiveMode},
		{"NEXT_ROLE", ctx.NextRole},
		{"SWARM_BIN", ctx.SwarmBin},
	} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
		}
	}

	b.WriteString("```\n\n")

	if ctx.SwarmBin != "" && ctx.Role != "" {
		b.WriteString("Your lifecycle commands, ready to run:\n\n")
		b.WriteString("```bash\n")
		fmt.Fprintf(&b, "SWARM_BIN=%s\n\n", shellQuote(ctx.SwarmBin))
		fmt.Fprintf(&b, "\"$SWARM_BIN\" handoff ready %s      # get or resume work\n", ctx.Role)
		fmt.Fprintf(&b, "\"$SWARM_BIN\" handoff current %s    # re-read it, changing nothing\n", ctx.Role)
		fmt.Fprintf(&b, "\"$SWARM_BIN\" handoff status %s     # has my downstream handoff been sent?\n", ctx.Role)
		fmt.Fprintf(&b, "\"$SWARM_BIN\" handoff next --from %s --type note --priority 10 --note \"...\"\n", ctx.Role)
		fmt.Fprintf(&b, "\"$SWARM_BIN\" handoff done %s       # only after the handoff succeeded\n", ctx.Role)
		b.WriteString("```\n\n")
	}

	if ctx.ReceiveMode == "batch" {
		b.WriteString("Your receive mode is `batch`: `ready` gives you every message sharing " +
			"the highest priority, and `done` completes all of them together.\n\n")
	}

	if ctx.NextRole != "" {
		fmt.Fprintf(&b, "Your completed work goes to **%s**; `handoff next` routes there automatically.\n", ctx.NextRole)
	}

	return b.String()
}

// shellQuote wraps a value so a shell treats it as one literal word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
