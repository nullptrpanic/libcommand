package builtin

func init() {
	registerBuiltin("true", executeTrue)
}
