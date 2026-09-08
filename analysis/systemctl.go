package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Systemctl detects systemctl power-control actions.
func Systemctl(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, systemctlPowerAction, RiskTypeDestructiveOperation)
}

func systemctlPowerAction(arguments []string) bool {
	command := ""
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--help" || argument == "--version" || argument == "-h" {
			return false
		}
		if argument == "--" {
			if command == "" && index+1 < len(arguments) {
				command = arguments[index+1]
			}
			break
		}
		option, _, attached := strings.Cut(argument, "=")
		switch option {
		case "-H", "--host", "-M", "--machine", "-t", "--type", "-p", "--property", "--state", "--root", "--image", "--job-mode", "--signal", "--kill-whom", "--output", "--lines", "--preset-mode", "--when":
			if !attached {
				index++
			}
		default:
			if !strings.HasPrefix(argument, "-") && command == "" {
				command = argument
			}
		}
	}
	switch command {
	case "poweroff", "reboot", "halt", "kexec":
		return true
	default:
		return false
	}
}
