package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

func netcatCommand(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	args, concrete := concreteArguments(invocation.Args)
	if concrete && netcatTransfersInput(args) && observeNetcat(ctx, shell, invocation) {
		return commandResult(ctx, shell, invocation, RiskTypeReverseShell)
	}
	return argumentRiskResult(ctx, shell, invocation, netcatExecRisk, RiskTypeReverseShell)
}

func netcatExecRisk(arguments []string) bool {
	risk := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--":
			return risk
		case argument == "-h" || argument == "--help" || argument == "--version":
			return false
		case strings.HasPrefix(argument, "--sh-exec="):
			risk = risk || len(argument) > len("--sh-exec=")
		case argument == "--sh-exec":
			if index+1 < len(arguments) {
				index++
				risk = risk || arguments[index] != ""
			}
		case argument == "-e" || argument == "-c" || argument == "--exec":
			if index+1 < len(arguments) {
				index++
				risk = risk || containsShellExecutable(arguments[index])
			}
		case strings.HasPrefix(argument, "--exec="):
			risk = risk || containsShellExecutable(strings.TrimPrefix(argument, "--exec="))
		}
	}
	return risk
}

func netcatTransfersInput(arguments []string) bool {
	operands := 0
	options := true
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if options && argument == "--" {
			options = false
			continue
		}
		if !options || !strings.HasPrefix(argument, "-") || argument == "-" {
			operands++
			continue
		}
		option, _, attached := strings.Cut(argument, "=")
		switch option {
		case "--help", "--version", "--listen", "--zero", "--send-only", "--recv-only":
			return false
		case "--source", "--source-port", "--wait", "--idle-timeout", "--exec", "--sh-exec", "--proxy", "--proxy-type", "--proxy-auth":
			if !attached {
				index++
			}
		default:
			if strings.HasPrefix(argument, "--") {
				continue
			}
			for offset := 1; offset < len(argument); offset++ {
				if strings.ContainsRune("hlzd", rune(argument[offset])) {
					return false
				}
				if strings.ContainsRune("epwsiImWOXxTc", rune(argument[offset])) {
					if offset+1 == len(argument) {
						index++
					}
					break
				}
			}
		}
	}
	return operands >= 2
}
