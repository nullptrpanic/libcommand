package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// These small test commands let runtime tests exercise language mechanics
// without importing internal/builtin, which would create an import cycle.
// Command compatibility itself is tested by the builtin and public packages.

func runtimeTestSuccessCommand(_ context.Context, execution *CommandContext, _ *Invocation) (*CommandResult, error) {
	return &CommandResult{}, nil
}

func runtimeTestDeclarationCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	declaration, ok := execution.CommandSyntax().(*syntax.DeclClause)
	if !ok {
		return &CommandResult{}, nil
	}
	kind := expand.Unknown
	exported := invocation.Name == "export"
	readOnly := invocation.Name == "readonly"
	assignments := make([]*syntax.Assign, 0, len(declaration.Args))
	for _, argument := range declaration.Args {
		if argument.Name != nil {
			assignments = append(assignments, argument)
			continue
		}
		if !argument.Naked || argument.Value == nil {
			continue
		}
		option := argument.Value.Lit()
		if !strings.HasPrefix(option, "-") {
			continue
		}
		for _, flag := range strings.TrimPrefix(option, "-") {
			switch flag {
			case 'a':
				kind = expand.Indexed
			case 'A':
				kind = expand.Associative
			case 'x':
				exported = true
			case 'r':
				readOnly = true
			default:
				return execution.StopUnresolved(fmt.Sprintf("unsupported declaration option -%c", flag)), nil
			}
		}
	}

	s := execution.State()
	local := invocation.Name == "local" || s.funcDepth > 0 && (invocation.Name == "declare" || invocation.Name == "typeset")
	originalVars := s.vars
	originalLocalScopes := s.localScopes
	originalLocalScopesBytes := s.localScopesBytes
	originalLocalScopesShared := s.localScopesShared
	restore := func() {
		s.vars = originalVars
		s.localScopes = originalLocalScopes
		s.localScopesBytes = originalLocalScopesBytes
		s.localScopesShared = originalLocalScopesShared
	}
	if local {
		if len(s.localScopes) == 0 {
			return &CommandResult{ExitCode: 1}, nil
		}
		for _, assignment := range assignments {
			if assignment.Name != nil && s.vars.Get(assignment.Name.Value).ReadOnly {
				restore()
				return execution.StopUnresolved(fmt.Sprintf("%s: readonly variable", assignment.Name.Value)), nil
			}
		}
		s.localScopesShared = true
		for _, assignment := range assignments {
			if assignment.Name == nil {
				continue
			}
			s.saveLocal(assignment.Name.Value)
			if assignment.Naked {
				s.vars.delete(assignment.Name.Value)
			}
		}
	}
	if err := execution.ApplyAssignments(assignments, kind, exported); err != nil {
		restore()
		return execution.ExpansionError(err, err.Error()), nil
	}
	for _, assignment := range assignments {
		if assignment.Name == nil {
			continue
		}
		name := assignment.Name.Value
		value := s.vars.Get(name)
		value.Local = value.Local || local
		value.ReadOnly = value.ReadOnly || readOnly
		s.vars.putIndexedWithCertainty(name, value, s.vars.isUnknown(name), s.vars.indexedSlots(name))
	}
	return &CommandResult{}, nil
}

func runtimeTestLetCommand(_ context.Context, execution *CommandContext, _ *Invocation) (*CommandResult, error) {
	clause, ok := execution.CommandSyntax().(*syntax.LetClause)
	if !ok {
		return &CommandResult{}, nil
	}
	snapshot := execution.Snapshot()
	value := 0
	for _, expression := range clause.Exprs {
		result, err := execution.Arithmetic(expression)
		if err != nil {
			execution.Restore(snapshot)
			return execution.ExpansionError(err, fmt.Sprintf("evaluate let expression: %v", err)), nil
		}
		if result.Unknown {
			execution.Restore(snapshot)
			return execution.ResultUnknown(&CommandResult{ExitCode: 1}, false, true, false), nil
		}
		if result.Failure != "" {
			return &CommandResult{Stderr: []byte("let: " + result.Failure + "\n"), ExitCode: 1}, nil
		}
		value = result.Value
	}
	return &CommandResult{ExitCode: boolExitCode(value != 0)}, nil
}

func runtimeTestConcreteArguments(invocation *Invocation) ([]string, bool) {
	arguments := make([]string, len(invocation.Args))
	for index, argument := range invocation.Args {
		if argument.Kind != ArgumentString {
			return nil, false
		}
		arguments[index] = argument.Value
	}
	return arguments, true
}

func runtimeTestSetCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	args, concrete := runtimeTestConcreteArguments(invocation)
	if !concrete {
		return nil, nil
	}
	s := execution.State()
	for len(args) != 0 {
		argument := args[0]
		if argument == "--" {
			s.replacePositionalArguments(args[1:])
			break
		}
		if len(argument) < 2 || argument[0] != '-' && argument[0] != '+' {
			s.replacePositionalArguments(args)
			break
		}
		enabled := argument[0] == '-'
		for _, option := range argument[1:] {
			switch option {
			case 'e':
				s.options.errexit = enabled
			case 'u':
				s.options.noUnset = enabled
			case 'f':
				s.options.noGlob = enabled
			}
		}
		args = args[1:]
	}
	return &CommandResult{}, nil
}

func runtimeTestShiftCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	args, concrete := runtimeTestConcreteArguments(invocation)
	if !concrete || len(args) > 1 {
		return &CommandResult{ExitCode: 1}, nil
	}
	amount := 1
	if len(args) == 1 {
		var err error
		amount, err = strconv.Atoi(args[0])
		if err != nil || amount < 0 {
			return &CommandResult{ExitCode: 2}, nil
		}
	}
	positional := execution.PositionalArguments()
	if amount > len(positional) {
		return &CommandResult{ExitCode: 1}, nil
	}
	execution.ReplacePositionalArguments(positional[amount:])
	return &CommandResult{}, nil
}

func runtimeTestPWDCommand(_ context.Context, execution *CommandContext, _ *Invocation) (*CommandResult, error) {
	directory, unknown := execution.Directory()
	return execution.ResultUnknown(&CommandResult{Stdout: []byte(directory + "\n")}, unknown, false, false), nil
}

func runtimeTestPrintfCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	args, concrete := runtimeTestConcreteArguments(invocation)
	if !concrete {
		return nil, nil
	}
	variable := ""
	if len(args) >= 2 && args[0] == "-v" {
		variable = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		return &CommandResult{ExitCode: 2}, nil
	}
	value, err := execution.Format(args[0], args[1:])
	if err != nil {
		return &CommandResult{ExitCode: 1}, nil
	}
	if variable == "" {
		return &CommandResult{Stdout: []byte(value)}, nil
	}
	execution.RecordVariableRollback(variable)
	if err := execution.AssignVariable(variable, &expand.Variable{Set: true, Kind: expand.String, Str: value}, false); err != nil {
		return nil, err
	}
	return &CommandResult{}, nil
}

func runtimeTestReadCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	args, concrete := runtimeTestConcreteArguments(invocation)
	if !concrete {
		return nil, nil
	}
	for len(args) != 0 && strings.HasPrefix(args[0], "-") {
		if args[0] != "-r" {
			return &CommandResult{ExitCode: 2}, nil
		}
		args = args[1:]
	}
	if len(args) == 0 {
		args = []string{"REPLY"}
	}
	input, inputUnresolved := execution.Input()
	line := ""
	terminated := false
	if index := strings.IndexByte(string(input), '\n'); index >= 0 {
		line = string(input[:index])
		execution.SetInput(input[index+1:], inputUnresolved)
		terminated = true
	} else {
		line = string(input)
		execution.SetInput(nil, inputUnresolved)
	}
	fields := strings.Fields(line)
	for index, name := range args {
		value := ""
		if index < len(fields) {
			if index == len(args)-1 {
				value = strings.Join(fields[index:], " ")
			} else {
				value = fields[index]
			}
		}
		if err := execution.AssignVariable(name, &expand.Variable{Set: true, Kind: expand.String, Str: value}, inputUnresolved); err != nil {
			return nil, err
		}
	}
	exitCode := 0
	if !terminated && line == "" {
		exitCode = 1
	}
	return execution.ResultUnknown(&CommandResult{ExitCode: exitCode}, false, false, inputUnresolved), nil
}

func runtimeTestUnsetCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	args, concrete := runtimeTestConcreteArguments(invocation)
	if !concrete {
		return nil, nil
	}
	functions := false
	if len(args) != 0 && args[0] == "-f" {
		functions = true
		args = args[1:]
	} else if len(args) != 0 && args[0] == "-v" {
		args = args[1:]
	} else if len(args) != 0 && strings.HasPrefix(args[0], "-") {
		return &CommandResult{ExitCode: 2}, nil
	}
	for _, name := range args {
		if functions {
			execution.DeleteFunction(name)
		} else if err := execution.UnsetVariable(name); err != nil {
			return &CommandResult{ExitCode: 1}, nil
		}
	}
	return &CommandResult{}, nil
}

func runtimeTestTestCommand(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
	args, concrete := runtimeTestConcreteArguments(invocation)
	if !concrete {
		return nil, nil
	}
	if invocation.Name == "[" {
		if len(args) == 0 || args[len(args)-1] != "]" {
			return &CommandResult{ExitCode: 2}, nil
		}
		args = args[:len(args)-1]
	}
	truth, known, failure := runtimeTestTruth(execution, args)
	if failure != 0 {
		return &CommandResult{ExitCode: failure}, nil
	}
	if !known {
		return nil, nil
	}
	return &CommandResult{ExitCode: boolExitCode(truth)}, nil
}

func runtimeTestTruth(execution *CommandContext, args []string) (bool, bool, int) {
	if len(args) > 1 && args[0] == "!" {
		truth, known, failure := runtimeTestTruth(execution, args[1:])
		return !truth, known, failure
	}
	switch len(args) {
	case 0:
		return false, true, 0
	case 1:
		return args[0] != "", true, 0
	case 2:
		switch args[0] {
		case "-n":
			return args[1] != "", true, 0
		case "-z":
			return args[1] == "", true, 0
		case "-e":
			return execution.PathKind(execution.ResolvePath(args[1])) != PathMissing, true, 0
		case "-f":
			return execution.PathKind(execution.ResolvePath(args[1])) == PathFile, true, 0
		case "-d":
			return execution.PathKind(execution.ResolvePath(args[1])) == PathDirectory, true, 0
		}
	case 3:
		switch args[1] {
		case "=", "==":
			return args[0] == args[2], true, 0
		case "!=":
			return args[0] != args[2], true, 0
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			left, leftErr := strconv.Atoi(args[0])
			right, rightErr := strconv.Atoi(args[2])
			if leftErr != nil || rightErr != nil {
				return false, true, 2
			}
			switch args[1] {
			case "-eq":
				return left == right, true, 0
			case "-ne":
				return left != right, true, 0
			case "-lt":
				return left < right, true, 0
			case "-le":
				return left <= right, true, 0
			case "-gt":
				return left > right, true, 0
			default:
				return left >= right, true, 0
			}
		}
	}
	return false, false, 0
}
