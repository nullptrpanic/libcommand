package builtin

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	registerCommand("test", executeTest)
}

func executeTest(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	name := invocation.Name
	if name == "[" {
		arguments := invocation.Args
		if len(arguments) == 0 || arguments[len(arguments)-1].Kind != runtime.ArgumentString || arguments[len(arguments)-1].Value != "]" {
			return &runtime.CommandResult{Stderr: []byte("[: missing ]\n"), ExitCode: 2}, nil
		}
		trimmed := *invocation
		trimmed.Args = arguments[:len(arguments)-1]
		invocation = &trimmed
	}
	args, concrete := concreteArguments(invocation)
	if !concrete {
		return shell.ResultUnknown(&runtime.CommandResult{}, false, false, true), nil
	}
	truth, failure := testTruth(shell, args)
	if failure != nil {
		return &runtime.CommandResult{
			Stderr:   []byte(fmt.Sprintf("%s: %s\n", name, failure.message)),
			ExitCode: failure.exitCode,
		}, nil
	}
	if truth == testUnknown {
		return shell.ResultUnknown(&runtime.CommandResult{}, false, false, true), nil
	}
	exitCode := 1
	if truth == testTrue {
		exitCode = 0
	}
	return &runtime.CommandResult{ExitCode: exitCode}, nil
}

type testTruthValue uint8

const (
	testFalse testTruthValue = iota
	testTrue
	testUnknown
)

type testFailure struct {
	exitCode int
	message  string
}

func testTruth(shell *runtime.CommandContext, args []string) (testTruthValue, *testFailure) {
	if len(args) > 1 && args[0] == "!" {
		value, failure := testTruth(shell, args[1:])
		return invertTestTruth(value), failure
	}
	switch len(args) {
	case 0:
		return testFalse, nil
	case 1:
		return booleanTestTruth(args[0] != ""), nil
	case 2:
		switch args[0] {
		case "-n":
			return booleanTestTruth(args[1] != ""), nil
		case "-z":
			return booleanTestTruth(args[1] == ""), nil
		default:
			if isFileTestOperator(args[0]) {
				return fileTestTruth(shell, args[0], args[1]), nil
			}
			return testUnknown, nil
		}
	case 3:
		if args[0] == "!" {
			value, failure := testTruth(shell, args[1:])
			return invertTestTruth(value), failure
		}
		switch args[1] {
		case "=", "==":
			return booleanTestTruth(args[0] == args[2]), nil
		case "!=":
			return booleanTestTruth(args[0] != args[2]), nil
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			left, leftErr := strconv.ParseInt(args[0], 10, 64)
			right, rightErr := strconv.ParseInt(args[2], 10, 64)
			if leftErr != nil || rightErr != nil {
				operand := args[0]
				if leftErr == nil {
					operand = args[2]
				}
				return testFalse, &testFailure{exitCode: 2, message: fmt.Sprintf("%s: integer expression expected", operand)}
			}
			switch args[1] {
			case "-eq":
				return booleanTestTruth(left == right), nil
			case "-ne":
				return booleanTestTruth(left != right), nil
			case "-lt":
				return booleanTestTruth(left < right), nil
			case "-le":
				return booleanTestTruth(left <= right), nil
			case "-gt":
				return booleanTestTruth(left > right), nil
			default:
				return booleanTestTruth(left >= right), nil
			}
		}
	}
	return testUnknown, nil
}

func isFileTestOperator(name string) bool {
	switch name {
	case "-e", "-f", "-r", "-w", "-x", "-d", "-s":
		return true
	default:
		return false
	}
}

func fileTestTruth(shell *runtime.CommandContext, operator, name string) testTruthValue {
	resolved := shell.ResolvePath(name)
	kind := shell.PathKind(resolved)
	if kind == runtime.PathFile {
		switch operator {
		case "-d", "-x":
			return testFalse
		case "-s":
			contents, unknown, _ := shell.ReadFile(resolved)
			if unknown {
				return testUnknown
			}
			return booleanTestTruth(len(contents) != 0)
		default:
			return testTrue
		}
	}
	if kind == runtime.PathDirectory {
		switch operator {
		case "-e", "-d", "-r", "-w", "-x":
			return testTrue
		default:
			return testFalse
		}
	}
	return testFalse
}

func booleanTestTruth(value bool) testTruthValue {
	if value {
		return testTrue
	}
	return testFalse
}

func invertTestTruth(value testTruthValue) testTruthValue {
	switch value {
	case testTrue:
		return testFalse
	case testFalse:
		return testTrue
	default:
		return testUnknown
	}
}
