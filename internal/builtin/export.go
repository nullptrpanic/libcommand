package builtin

func init() {
	registerCommand("export", executeDeclaration)
}
