package handoff

import "testing"

func TestNextRole(t *testing.T) {
	cases := map[string]string{
		"specifier":  "coder",
		"coder":      "refactorer",
		"refactorer": "architect",
		"architect":  "specifier",
	}

	for from, want := range cases {
		got, err := NextRole(from)
		if err != nil {
			t.Errorf("NextRole(%q): %v", from, err)
			continue
		}
		if got != want {
			t.Errorf("NextRole(%q) = %q, want %q", from, got, want)
		}
	}
}

func TestNextRoleUnknown(t *testing.T) {
	for _, role := range []string{"", "foo", "Coder", "../coder"} {
		if _, err := NextRole(role); err == nil {
			t.Errorf("NextRole(%q) returned a destination", role)
		}
	}
}

// The route must be a single cycle covering all four roles.
func TestRouteIsAClosedCycle(t *testing.T) {
	route := Route()

	if len(route) != 4 {
		t.Fatalf("route has %d hops, want 4", len(route))
	}

	seen := map[string]bool{}
	role := "specifier"
	for i := 0; i < 4; i++ {
		if seen[role] {
			t.Fatalf("role %q repeats before the cycle closes", role)
		}
		seen[role] = true

		next, err := NextRole(role)
		if err != nil {
			t.Fatal(err)
		}
		role = next
	}

	if role != "specifier" {
		t.Errorf("route ends at %q, want it to close back to specifier", role)
	}
}
