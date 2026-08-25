package builtin

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerExternalCandidate("ionice", executeIonice)
}

func executeIonice(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	index := 0
	identifierMode := false
	for index < len(invocation.Args) {
		value, ok := wrapperArgument(invocation, index)
		if !ok {
			return unresolvedWrapper(shell)
		}
		if value == "--" {
			index++
			break
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			break
		}
		if value == "-p" || value == "--pid" || value == "-P" || value == "--pgid" || value == "-u" || value == "--uid" {
			identifierMode = true
			if _, ok := wrapperArgument(invocation, index+1); !ok {
				return unresolvedWrapper(shell)
			}
			index += 2
			continue
		}
		if value == "-c" || value == "--class" || value == "-n" || value == "--classdata" {
			if _, ok := wrapperArgument(invocation, index+1); !ok {
				return unresolvedWrapper(shell)
			}
			index += 2
			continue
		}
		if strings.HasPrefix(value, "--class=") || strings.HasPrefix(value, "--classdata=") || strings.HasPrefix(value, "-c") && len(value) > 2 || strings.HasPrefix(value, "-n") && len(value) > 2 {
			index++
			continue
		}
		if value == "-t" || value == "--ignore" {
			index++
			continue
		}
		return unresolvedWrapper(shell)
	}
	if identifierMode {
		return shell.StopUnresolved("ionice identifier mode does not execute a nested command"), nil
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
