package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerExternalCandidate("env", executeEnv)
}

func executeEnv(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	arguments := invocation.Args
	clearEnvironment := false
	var unset []string
	assignments := make(map[string]string)
	index := 0
	for index < len(arguments) {
		argument := arguments[index]
		if argument.Kind != runtime.ArgumentString {
			return unresolvedStderrCommandResult(shell, 1), nil
		}
		if argument.Value == "--" {
			index++
			break
		}
		if argument.Value == "-i" || argument.Value == "--ignore-environment" {
			clearEnvironment = true
			index++
			continue
		}
		if argument.Value == "-u" || argument.Value == "--unset" {
			if index+1 >= len(arguments) || arguments[index+1].Kind != runtime.ArgumentString {
				return commandResult(shell, nil, []byte("env: option requires an argument\n"), 1), nil
			}
			unset = append(unset, arguments[index+1].Value)
			index += 2
			continue
		}
		if strings.HasPrefix(argument.Value, "-") {
			return shell.StopUnresolved(fmt.Sprintf("env option %q is not supported", argument.Value)), nil
		}
		name, value, assignment := strings.Cut(argument.Value, "=")
		if !assignment || !syntax.ValidName(name) {
			break
		}
		assignments[name] = value
		index++
	}
	if index == len(arguments) {
		stdout, stdoutUnknown, err := environmentOutput(ctx, shell, clearEnvironment, unset, assignments)
		if err != nil {
			return nil, err
		}
		return uncertainCommandResult(shell, stdout, nil, 0, stdoutUnknown, false, false), nil
	}
	if arguments[index].Kind != runtime.ArgumentString {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	return shell.InvokeWithEnvironment(arguments[index].Value, arguments[index+1:], clearEnvironment, unset, assignments), nil
}

func environmentOutput(ctx context.Context, shell *runtime.CommandContext, clear bool, unset []string, assignments map[string]string) ([]byte, bool, error) {
	values := make(map[string]string)
	unknown := make(map[string]bool)
	if !clear {
		shell.EachVariable(func(name string, value *expand.Variable) bool {
			if value.Exported && value.IsSet() {
				values[name] = value.String()
				unknown[name] = shell.VariableUnknown(name)
			}
			return true
		})
	}
	for _, name := range unset {
		delete(values, name)
		delete(unknown, name)
	}
	for name, value := range assignments {
		values[name] = value
		unknown[name] = false
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	outputUnknown := false
	for index, name := range names {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
		lineBytes := len(name) + 1 + len(values[name]) + 1
		var ok bool
		total, ok = materialize.Add(total, lineBytes, shell.MaxMemoryBytes())
		if !ok {
			return nil, false, materialize.LimitError(shell.MaxMemoryBytes())
		}
		outputUnknown = outputUnknown || unknown[name]
	}
	output := make([]byte, 0, total)
	for _, name := range names {
		output = append(output, name...)
		output = append(output, '=')
		output = append(output, values[name]...)
		output = append(output, '\n')
	}
	return output, outputUnknown, nil
}
