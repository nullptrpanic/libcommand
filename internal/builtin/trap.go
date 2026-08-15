package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("trap", executeTrap)
}

func executeTrap(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return shell.ResultUnknown(&runtime.CommandResult{ExitCode: 1}, false, true, false), nil
	}
	if len(args) == 0 {
		return queryTraps(shell, nil), nil
	}
	if args[0] == "-l" {
		return &runtime.CommandResult{Stderr: []byte("trap: -l is not supported\n"), ExitCode: 2}, nil
	}
	if args[0] == "-p" {
		return queryTraps(shell, args[1:]), nil
	}
	if len(args) < 2 {
		return &runtime.CommandResult{Stderr: []byte("trap: usage: trap [-lp] [[arg] signal ...]\n"), ExitCode: 2}, nil
	}
	action := args[0]
	for _, rawSignal := range args[1:] {
		signal := canonicalSignal(rawSignal)
		if signal == "" {
			return &runtime.CommandResult{Stderr: []byte("trap: invalid signal specification\n"), ExitCode: 1}, nil
		}
		if action == "-" {
			shell.DeleteTrap(signal)
			continue
		}
		shell.SetTrap(signal, action)
	}
	return &runtime.CommandResult{}, nil
}

func queryTraps(shell *runtime.CommandContext, signals []string) *runtime.CommandResult {
	traps := shell.Traps()
	if len(signals) == 0 {
		signals = make([]string, 0, len(traps))
		for signal := range traps {
			signals = append(signals, signal)
		}
		sort.Strings(signals)
	}
	var output strings.Builder
	for _, rawSignal := range signals {
		signal := canonicalSignal(rawSignal)
		if signal == "" {
			return &runtime.CommandResult{Stderr: []byte("trap: invalid signal specification\n"), ExitCode: 1}
		}
		command, exists := traps[signal]
		if !exists {
			continue
		}
		fmt.Fprintf(&output, "trap -- %s %s\n", quoteShellWord(command), signal)
	}
	return &runtime.CommandResult{Stdout: []byte(output.String())}
}

func canonicalSignal(signal string) string {
	upper := strings.ToUpper(signal)
	upper = strings.TrimPrefix(upper, "SIG")
	if upper == "0" {
		return "EXIT"
	}
	switch upper {
	case "EXIT", "ERR":
		return upper
	default:
		return ""
	}
}
