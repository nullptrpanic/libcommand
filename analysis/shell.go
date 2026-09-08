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
	input := redirectedFiles(redirects)[0]
	if input == nil {
		return false
	}
	target := path.Clean(input.Target)
	return strings.HasPrefix(target, "/dev/tcp/") || strings.HasPrefix(target, "/dev/udp/")
}

// Resolve in source order: duplication captures the endpoint at that moment,
// while reopening or closing a descriptor replaces its previous endpoint.
func redirectedFiles(redirects []*libcommand.Redirect) map[int]*libcommand.Redirect {
	files := make(map[int]*libcommand.Redirect)
	for _, redirect := range redirects {
		if redirect.Unresolved {
			files[redirect.FD] = nil
			if redirect.Operator == "&>" || redirect.Operator == "&>>" {
				files[1], files[2] = nil, nil
			}
			continue
		}
		switch redirect.Operator {
		case "&>", "&>>":
			files[1], files[2] = redirect, redirect
		case ">&", "<&":
			targetFD, err := strconv.Atoi(strings.TrimSuffix(redirect.Target, "-"))
			if err == nil && targetFD >= 0 {
				files[redirect.FD] = files[targetFD]
				if strings.HasSuffix(redirect.Target, "-") {
					files[targetFD] = nil
				}
			} else if strings.HasPrefix(redirect.Target, "/") {
				files[redirect.FD] = redirect
				if redirect.Operator == ">&" && redirect.FD == 1 {
					files[2] = redirect
				}
			} else {
				files[redirect.FD] = nil
			}
		default:
			files[redirect.FD] = redirect
		}
	}
	return files
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
