package builtin

func init() {
	registerRestoreAlways(".", executeSource)
}
