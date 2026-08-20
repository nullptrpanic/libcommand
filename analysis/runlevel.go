package analysis

func powerRunlevel(arguments []string) bool {
	return len(arguments) != 0 && (arguments[0] == "0" || arguments[0] == "6")
}
