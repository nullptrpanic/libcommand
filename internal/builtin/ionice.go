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
		argument := invocation.Args[index]
		if argument.Kind != runtime.ArgumentString {
			return unresolvedWrapperArgument(shell, invocation.Name)
		}
		value := argument.Value
		if value == "--" {
			index++
			break
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			break
		}
		if value == "-p" || value == "--pid" || value == "-P" || value == "--pgid" || value == "-u" || value == "--uid" {
			identifierMode = true
			if index+1 >= len(invocation.Args) || invocation.Args[index+1].Kind != runtime.ArgumentString {
				return unresolvedWrapperArgument(shell, invocation.Name)
			}
			index += 2
			continue
		}
		if value == "-c" || value == "--class" || value == "-n" || value == "--classdata" {
			if index+1 >= len(invocation.Args) || invocation.Args[index+1].Kind != runtime.ArgumentString {
				return unresolvedWrapperArgument(shell, invocation.Name)
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
		return unsupportedWrapperOption(shell, invocation.Name, value)
	}
	if identifierMode {
		return shell.StopUnresolved("ionice identifier mode does not execute a nested command"), nil
	}
	return invokeExternalWrapper(shell, invocation, index, nil)
}
