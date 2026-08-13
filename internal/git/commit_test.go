package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidAbbrev(t *testing.T) {
	good := []string{"71ae82cc13", "0123456789", "abcdefabcd", "71AE82CC13"}
	for _, c := range good {
		if err := ValidAbbrev(c); err != nil {
			t.Errorf("ValidAbbrev(%q) = %v, want nil", c, err)
		}
	}

	bad := map[string]string{
		"too short":  "abc123",
		"nine":       "71ae82cc1",
		"eleven":     "71ae82cc133",
		"non-hex":    "71ae82cc13ZZ",
		"letters":    "zzzzzzzzzz",
		"empty":      "",
		"full sha":   "71ae82cc13a9f0e4d8c7b6a5049382716f5e4d3c",
		"leading -":  "-1ae82cc13",
		"whitespace": "71ae82cc1 ",
	}
	for name, c := range bad {
		if err := ValidAbbrev(c); err == nil {
			t.Errorf("%s: ValidAbbrev(%q) accepted", name, c)
		}
	}
}

// repoSeq makes every test repository's history unique. Without it two repos
// built from identical content, messages and timestamps produce identical
// commit SHAs — which is correct Git behavior, but useless for testing that
// resolution is repository-scoped.
var repoSeq int

// testRepo builds a throwaway repository with two commits and returns its root
// plus the two full SHAs.
func testRepo(t *testing.T) (root, first, second string) {
	t.Helper()

	repoSeq++
	seed := fmt.Sprintf("repo-%d-%s\n", repoSeq, t.Name())

	if !Available() {
		t.Skip("git not available")
	}

	root = t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "swarm@example.com"},
		{"config", "user.name", "swarm"},
	} {
		if _, err := run(root, args...); err != nil {
			t.Skipf("git unusable: %v", err)
		}
	}

	commit := func(name, body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := run(root, "add", name); err != nil {
			t.Fatal(err)
		}
		if _, err := run(root, "commit", "-m", "add "+name); err != nil {
			t.Fatal(err)
		}
		sha, err := run(root, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}

	first = commit("one.txt", "one\n"+seed)
	second = commit("two.txt", "two\n"+seed)

	return root, first, second
}

func TestResolveCommit(t *testing.T) {
	root, first, second := testRepo(t)
	repo := NewRepo(root)

	for _, want := range []string{first, second} {
		abbrev := want[:AbbrevLen]

		got, err := repo.ResolveCommit(abbrev)
		if err != nil {
			t.Fatalf("ResolveCommit(%q): %v", abbrev, err)
		}
		if got != want {
			t.Errorf("ResolveCommit(%q) = %s, want canonical %s", abbrev, got, want)
		}
		if len(got) < 40 {
			t.Errorf("returned SHA %q is not canonical", got)
		}
	}
}

func TestResolveCommitIsCaseInsensitive(t *testing.T) {
	root, first, _ := testRepo(t)
	repo := NewRepo(root)

	abbrev := first[:AbbrevLen]
	upper := strings.ToUpper(abbrev)

	got, err := repo.ResolveCommit(upper)
	if err != nil {
		t.Fatalf("ResolveCommit(%q): %v", upper, err)
	}
	if got != first {
		t.Errorf("uppercase abbreviation resolved to %s, want %s", got, first)
	}
}

func TestResolveCommitRejectsNonexistent(t *testing.T) {
	root, first, _ := testRepo(t)
	repo := NewRepo(root)

	// Flip a hex digit so the abbreviation is well formed but almost certainly
	// absent from this two-commit repository.
	flipped := []byte(first[:AbbrevLen])
	if flipped[0] == 'f' {
		flipped[0] = '0'
	} else {
		flipped[0] = 'f'
	}

	if _, err := repo.ResolveCommit(string(flipped)); err == nil {
		t.Fatalf("nonexistent commit %q was accepted", flipped)
	}
}

func TestResolveCommitRejectsBadShape(t *testing.T) {
	root, _, _ := testRepo(t)
	repo := NewRepo(root)

	for _, abbrev := range []string{"abc123", "71ae82cc13ZZ", "", "HEAD", "--all"} {
		if _, err := repo.ResolveCommit(abbrev); err == nil {
			t.Errorf("ResolveCommit(%q) was accepted", abbrev)
		}
	}
}

// A blob is a real object but not a commit: `^{commit}` must reject it.
func TestResolveCommitRejectsNonCommitObject(t *testing.T) {
	root, _, _ := testRepo(t)
	repo := NewRepo(root)

	blob, err := run(root, "rev-parse", "HEAD:one.txt")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ResolveCommit(blob[:AbbrevLen]); err == nil {
		t.Errorf("blob %s was accepted as a commit", blob[:AbbrevLen])
	}
}

// A commit in another repository must not resolve here.
func TestResolveCommitIsRepositoryScoped(t *testing.T) {
	rootA, firstA, _ := testRepo(t)
	rootB, _, _ := testRepo(t)

	if _, err := NewRepo(rootB).ResolveCommit(firstA[:AbbrevLen]); err == nil {
		t.Error("a commit from another repository resolved")
	}
	if _, err := NewRepo(rootA).ResolveCommit(firstA[:AbbrevLen]); err != nil {
		t.Errorf("own commit failed to resolve: %v", err)
	}
}

// An agent runs inside a linked worktree, but every managed resource belongs to
// the main working tree — so RepoRoot must look through the worktree.
func TestRepoRootFromLinkedWorktree(t *testing.T) {
	root, _, _ := testRepo(t)

	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}

	wt, created, err := m.Create("coder", "wt-coder")
	if err != nil || !created {
		t.Fatalf("Create = (%v, %v)", created, err)
	}

	got, err := RepoRoot(wt.AbsPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolve(got) != resolve(root) {
		t.Errorf("RepoRoot(%s) = %s, want the main working tree %s", wt.AbsPath, got, root)
	}
}
