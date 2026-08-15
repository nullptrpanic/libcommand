package builtin

func init() {
	registerCommand("typeset", executeDeclaration)
}
