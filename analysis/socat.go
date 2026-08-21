package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Socat detects network addresses connected to a local Shell.
func Socat(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, socatExecRisk, RiskTypeReverseShell)
}

func socatExecRisk(arguments []string) bool {
	network := false
	shell := false
	for _, argument := range arguments {
		upper := strings.ToUpper(argument)
		if strings.HasPrefix(upper, "EXEC:") || strings.HasPrefix(upper, "SYSTEM:") {
			shell = shell || containsShellExecutable(argument)
			continue
		}
		for _, prefix := range []string{
			"TCP:", "TCP4:", "TCP6:",
			"TCP-CONNECT:", "TCP4-CONNECT:", "TCP6-CONNECT:",
			"TCP-LISTEN:", "TCP4-LISTEN:", "TCP6-LISTEN:",
			"UDP:", "UDP4:", "UDP6:",
			"UDP-CONNECT:", "UDP4-CONNECT:", "UDP6-CONNECT:",
			"UDP-LISTEN:", "UDP4-LISTEN:", "UDP6-LISTEN:",
			"OPENSSL:", "OPENSSL-LISTEN:",
			"SOCKS:", "SOCKS4:", "SOCKS4A:", "SOCKS5:",
		} {
			if strings.HasPrefix(upper, prefix) {
				network = true
				break
			}
		}
	}
	return network && shell
}
