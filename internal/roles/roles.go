package roles

// Role describes a single agent role in a swarm.
type Role struct {
	Name string
}

// FourPack returns the default four-role swarm layout.
func FourPack() []Role {
	return []Role{
		{Name: "specifier"},
		{Name: "coder"},
		{Name: "refactorer"},
		{Name: "architect"},
	}
}
