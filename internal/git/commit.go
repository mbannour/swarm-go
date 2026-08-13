package git

import (
	"fmt"
	"strings"
)

// AbbrevLen is the exact length of a commit abbreviation carried by a handoff.
// SwarmForge uses ten hexadecimal characters; anything else is rejected before
// Git is ever consulted.
const AbbrevLen = 10

// CommitResolver turns a commit abbreviation into a canonical SHA.
//
// Implementations must resolve against a repository chosen by the application,
// never one named by the message being validated.
type CommitResolver interface {
	ResolveCommit(abbrev string) (string, error)
}

// Repo resolves commits in one repository.
type Repo struct {
	Root string // absolute path to the repository top level
}

// NewRepo binds a resolver to a repository root.
func NewRepo(root string) *Repo { return &Repo{Root: root} }

// ValidAbbrev checks the shape of an abbreviation without touching Git.
//
// Hex digits are case-insensitive in Git, so mixed case is accepted here and
// normalised to lower case by ResolveCommit.
func ValidAbbrev(abbrev string) error {
	if len(abbrev) != AbbrevLen {
		return fmt.Errorf("commit %q must be exactly %d hexadecimal characters", abbrev, AbbrevLen)
	}

	for _, r := range abbrev {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return fmt.Errorf("commit %q is not hexadecimal", abbrev)
		}
	}

	return nil
}

// ResolveCommit verifies that abbrev names exactly one commit in this
// repository and returns its full canonical SHA.
//
// It runs, with each argument passed separately to exec.Command — never
// through a shell:
//
//	git rev-parse --verify --end-of-options <abbrev>^{commit}
//
// The `^{commit}` peel makes Git reject an object that exists but is not a
// commit (a blob or tree), and --verify makes an ambiguous or unknown name an
// error rather than a passthrough.
func (r *Repo) ResolveCommit(abbrev string) (string, error) {
	if err := ValidAbbrev(abbrev); err != nil {
		return "", err
	}

	normalised := strings.ToLower(abbrev)

	// --end-of-options stops Git from reading a value that begins with a dash
	// as a flag. ValidAbbrev already excludes that, but the guard is free.
	out, err := run(r.Root, "rev-parse", "--verify", "--end-of-options", normalised+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("commit %s does not resolve to a commit in this repository", abbrev)
	}

	sha := strings.TrimSpace(out)
	if len(sha) != 40 && len(sha) != 64 { // sha1 or sha256 repositories
		return "", fmt.Errorf("unexpected object name %q for commit %s", sha, abbrev)
	}
	if !strings.HasPrefix(sha, normalised) {
		return "", fmt.Errorf("commit %s resolved to unrelated object %s", abbrev, sha)
	}

	return sha, nil
}
