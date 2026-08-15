package builtin

func init() {
	registerExternalCandidate("sh", executeShell)
}
