package builtin

func init() {
	registerExternalCandidate("bash", executeShell)
}
