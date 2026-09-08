package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Perl detects high-confidence socket-backed Shell payloads passed with -e.
func Perl(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, perlReverseShellRisk, RiskTypeReverseShell)
}

func perlReverseShellRisk(arguments []string) bool {
	payload, found := shortOptionPayload(arguments, "-e")
	if !found {
		return false
	}
	lower := executableInterpreterText(payload)
	createsSocket := strings.Contains(lower, "use socket") && strings.Contains(lower, "socket(")
	connectsSocket := strings.Contains(lower, "connect(")
	redirectsStreams := strings.Contains(lower, "open(stdin,") && strings.Contains(lower, "open(stdout,") && strings.Contains(lower, "open(stderr,")
	launchesShell := interpreterCallsShell(payload, lower, "exec(")
	return createsSocket && connectsSocket && redirectsStreams && launchesShell
}
