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
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	if len(args) == 0 {
		return queryTraps(shell, nil), nil
	}
	if args[0] == "-l" {
		return commandResult(shell, nil, []byte("trap: -l is not supported\n"), 2), nil
	}
	if args[0] == "-p" {
		return queryTraps(shell, args[1:]), nil
	}
	if len(args) < 2 {
		return commandResult(shell, nil, []byte("trap: usage: trap [-lp] [[arg] signal ...]\n"), 2), nil
	}
	action := args[0]
	for _, rawSignal := range args[1:] {
		signal := canonicalSignal(rawSignal)
		if signal == "" {
			return commandResult(shell, nil, []byte("trap: invalid signal specification\n"), 1), nil
		}
		if action == "-" {
			shell.DeleteTrap(signal)
			continue
		}
		shell.SetTrap(signal, action)
	}
	return commandResult(shell, nil, nil, 0), nil
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
			return commandResult(shell, nil, []byte("trap: invalid signal specification\n"), 1)
		}
		command, exists := traps[signal]
		if !exists {
			continue
		}
		fmt.Fprintf(&output, "trap -- %s %s\n", quoteShellWord(command), signal)
	}
	return commandResult(shell, []byte(output.String()), nil, 0)
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
