package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerCommand("unset", executeUnset)
}

func executeUnset(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	functions := false
	options := true
	for _, argument := range args {
		if options && argument == "--" {
			options = false
			continue
		}
		if options && argument == "-f" {
			functions = true
			continue
		}
		if options && argument == "-v" {
			functions = false
			continue
		}
		if options && strings.HasPrefix(argument, "-") {
			return commandResult(shell, nil, []byte("unset: unsupported option\n"), 2), nil
		}
		if functions {
			shell.DeleteFunction(argument)
			continue
		}
		if failure, err := unsetShellTarget(shell, argument); failure != nil || err != nil {
			return failure, err
		}
	}
	return commandResult(shell, nil, nil, 0), nil
}

// A nil result means this operand succeeded and the next may be processed.
func unsetShellTarget(shell *runtime.CommandContext, target string) (*runtime.CommandResult, error) {
	failure := func(message string) (*runtime.CommandResult, error) {
		return commandResult(shell, nil, []byte(fmt.Sprintf("unset: `%s': %s\n", target, message)), 1), nil
	}
	if syntax.ValidName(target) {
		if err := shell.UnsetVariable(target); err != nil {
			return failure(err.Error())
		}
		return nil, nil
	}
	open := strings.IndexByte(target, '[')
	if open <= 0 || !strings.HasSuffix(target, "]") {
		return failure("not a valid identifier")
	}
	name := target[:open]
	if !syntax.ValidName(name) {
		return failure("not a valid identifier")
	}
	variable := shell.Variable(name)
	if variable.ReadOnly {
		return failure("cannot unset: readonly variable")
	}
	index := target[open+1 : len(target)-1]
	switch variable.Kind {
	case expand.Indexed:
		if index == "@" || index == "*" {
			variable.List = nil
			return nil, shell.AssignIndexedVariable(name, variable, false, nil)
		}
		expression, err := shell.ParseArithmetic(index)
		if err != nil {
			return failure("invalid array index")
		}
		value, err := shell.Arithmetic(expression)
		if err != nil {
			return nil, err
		}
		if value.Unknown {
			if err := shell.AssignIndexedVariable(name, variable, true, shell.IndexedSlots(name)); err != nil {
				return nil, err
			}
			return unresolvedExitCommandResult(shell, 0), nil
		}
		parsed := value.Value
		if value.Failure != "" || parsed < 0 {
			return failure("invalid array index")
		}
		if parsed >= len(variable.List) {
			return nil, nil
		}
		slots := explicitIndexedSlots(shell.IndexedSlots(name), len(variable.List))
		delete(slots, parsed)
		variable.List[parsed] = ""
		for len(variable.List) > 0 {
			last := len(variable.List) - 1
			if _, present := slots[last]; present {
				break
			}
			variable.List = variable.List[:last]
		}
		return nil, shell.AssignIndexedVariable(name, variable, shell.VariableUnknown(name), normalizeIndexedSlots(slots, len(variable.List)))
	case expand.Associative:
		delete(variable.Map, index)
		return nil, shell.AssignVariable(name, variable, shell.VariableUnknown(name))
	default:
		return nil, nil
	}
}

func explicitIndexedSlots(slots map[int]struct{}, length int) map[int]struct{} {
	if slots != nil {
		return slots
	}
	result := make(map[int]struct{}, length)
	for index := range length {
		result[index] = struct{}{}
	}
	return result
}

func normalizeIndexedSlots(slots map[int]struct{}, length int) map[int]struct{} {
	if len(slots) == length {
		contiguous := true
		for index := range length {
			if _, exists := slots[index]; !exists {
				contiguous = false
				break
			}
		}
		if contiguous {
			return nil
		}
	}
	result := make(map[int]struct{}, len(slots))
	for index := range slots {
		if index >= 0 && index < length {
			result[index] = struct{}{}
		}
	}
	return result
}
