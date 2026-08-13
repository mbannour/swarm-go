package handoff

import "fmt"

// The four-pack route. This is the single place the normal flow is written
// down; nothing else in the codebase decides where a role's work goes next.
//
//	specifier -> coder -> refactorer -> architect -> specifier
var fourPackRoute = []struct{ from, to string }{
	{"specifier", "coder"},
	{"coder", "refactorer"},
	{"refactorer", "architect"},
	{"architect", "specifier"},
}

// NextRole returns the role a given role normally hands work to.
func NextRole(role string) (string, error) {
	for _, hop := range fourPackRoute {
		if hop.from == role {
			return hop.to, nil
		}
	}
	return "", fmt.Errorf("no downstream role is defined for %q", role)
}

// Route returns the whole route as from/to pairs, in flow order.
func Route() [][2]string {
	out := make([][2]string, 0, len(fourPackRoute))
	for _, hop := range fourPackRoute {
		out = append(out, [2]string{hop.from, hop.to})
	}
	return out
}
