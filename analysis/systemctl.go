package analysis

import (
	"context"

	"github.com/nullptrpanic/libcommand"
)

// Systemctl detects systemctl power-control actions.
func Systemctl(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, systemctlPowerAction, "system power control")
}

func systemctlPowerAction(arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	switch arguments[0] {
	case "poweroff", "reboot", "halt", "kexec":
		return true
	default:
		return false
	}
}
