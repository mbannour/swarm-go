package main

import (
	"strings"
	"testing"

	"github.com/mbannour/swarm-go/internal/tmux"
)

func TestFindRef(t *testing.T) {
	refs := []tmux.RoleRef{
		{Name: "specifier", WorkingDir: "/repo/.swarm/worktrees/wt-specifier"},
		{Name: "coder", WorkingDir: "/repo/.swarm/worktrees/wt-coder"},
	}

	got, err := findRef(refs, "coder")
	if err != nil {
		t.Fatalf("findRef: %v", err)
	}
	if got.WorkingDir != "/repo/.swarm/worktrees/wt-coder" {
		t.Errorf("resolved to %+v", got)
	}

	_, err = findRef(refs, "foo")
	if err == nil || !strings.Contains(err.Error(), `unknown role "foo"`) {
		t.Errorf("findRef(foo) = %v, want unknown role error", err)
	}
}
