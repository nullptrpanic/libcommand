package builtin

func init() {
	registerCommand("local", executeDeclaration)
}
