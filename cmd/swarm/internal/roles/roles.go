package roles

type Role struct {
	Name string
}

func FourPack() []Role {
	return []Role{
		{Name: "specifier"},
		{Name: "coder"},
		{Name: "refactorer"},
		{Name: "architect"},
	}
}
