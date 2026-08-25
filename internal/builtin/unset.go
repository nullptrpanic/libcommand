package builtin

import (
	"context"
	"fmt"
	"strconv"
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
		if err := unsetShellTarget(shell, argument); err != nil {
			return commandResult(shell, nil, []byte(fmt.Sprintf("unset: `%s': %v\n", argument, err)), 1), nil
		}
	}
	return commandResult(shell, nil, nil, 0), nil
}

func unsetShellTarget(shell *runtime.CommandContext, target string) error {
	if syntax.ValidName(target) {
		return shell.UnsetVariable(target)
	}
	open := strings.IndexByte(target, '[')
	if open <= 0 || !strings.HasSuffix(target, "]") {
		return fmt.Errorf("not a valid identifier")
	}
	name := target[:open]
	if !syntax.ValidName(name) {
		return fmt.Errorf("not a valid identifier")
	}
	variable := shell.Variable(name)
	if variable.ReadOnly {
		return fmt.Errorf("cannot unset: readonly variable")
	}
	index := target[open+1 : len(target)-1]
	switch variable.Kind {
	case expand.Indexed:
		parsed, err := strconv.Atoi(index)
		if err != nil || parsed < 0 {
			return fmt.Errorf("invalid array index")
		}
		if parsed >= len(variable.List) {
			return nil
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
		return shell.AssignIndexedVariable(name, variable, shell.VariableUnknown(name), normalizeIndexedSlots(slots, len(variable.List)))
	case expand.Associative:
		delete(variable.Map, index)
		return shell.AssignVariable(name, variable, shell.VariableUnknown(name))
	default:
		return nil
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
