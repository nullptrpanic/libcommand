package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Python detects high-confidence socket-backed Shell payloads passed with -c.
func Python(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, pythonReverseShellRisk, RiskTypeReverseShell)
}

func pythonReverseShellRisk(arguments []string) bool {
	payload, found := shortOptionPayload(arguments, "-c")
	if !found {
		return false
	}
	lower := strings.ToLower(payload)
	createsSocket := strings.Contains(lower, "socket.socket(") || strings.Contains(lower, "socket.create_connection(")
	connectsSocket := strings.Contains(lower, ".connect(") || strings.Contains(lower, "create_connection(")
	redirectsDescriptors := strings.Contains(lower, "dup2(")
	launchesShell := strings.Contains(lower, "pty.spawn(") && containsShellExecutable(lower)
	return createsSocket && connectsSocket && redirectsDescriptors && launchesShell
}
