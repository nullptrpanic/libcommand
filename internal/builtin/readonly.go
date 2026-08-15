package builtin

func init() {
	registerCommand("readonly", executeDeclaration)
}
