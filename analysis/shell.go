package analysis

import (
	"context"
	"path"
	"strconv"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Shell detects an interactive Shell whose input is connected to a network endpoint.
func Shell(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	interactive := concrete && interactiveShellReadsInput(arguments)
	observeInteractiveShell(ctx, shell, invocation, interactive)
	risky := interactive && networkFeedsStdin(shell.Redirects())
	riskType := RiskType("")
	if risky {
		riskType = RiskTypeReverseShell
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func networkFeedsStdin(redirects []*libcommand.Redirect) bool {
	networkFDs := make(map[int]bool)
	for _, redirect := range redirects {
		target := path.Clean(redirect.Target)
		network := !redirect.Unresolved && (strings.HasPrefix(target, "/dev/tcp/") || strings.HasPrefix(target, "/dev/udp/"))
		switch redirect.Operator {
		case "&>", "&>>":
			networkFDs[1] = network
			networkFDs[2] = network
		case ">&":
			if network {
				networkFDs[redirect.FD] = true
				if redirect.FD == 1 {
					networkFDs[2] = true
				}
				continue
			}
			targetFD, err := strconv.Atoi(redirect.Target)
			if err == nil {
				networkFDs[redirect.FD] = networkFDs[targetFD]
				continue
			}
			networkFDs[redirect.FD] = false
			if redirect.FD == 1 {
				networkFDs[2] = false
			}
		case "<&":
			if network {
				networkFDs[redirect.FD] = true
				continue
			}
			targetFD, err := strconv.Atoi(redirect.Target)
			if err == nil {
				networkFDs[redirect.FD] = networkFDs[targetFD]
				continue
			}
			networkFDs[redirect.FD] = false
		default:
			networkFDs[redirect.FD] = network
		}
	}
	return networkFDs[0]
}

func interactiveShellReadsInput(arguments []string) bool {
	interactive := false
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			break
		}
		if strings.HasPrefix(argument, "--") {
			continue
		}
		options := argument[1:]
		if strings.ContainsRune(options, 'c') {
			return false
		}
		interactive = interactive || strings.ContainsRune(options, 'i')
	}
	return interactive
}
