package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

type testCommand func(context.Context, *State, *Invocation) (*CommandResult, error)

func parseForTest(t testing.TB, source, name string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), name)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func resultForTest(state *State, stdout, stderr []byte, exitCode int) *CommandResult {
	return uncertainResultForTest(state, stdout, stderr, exitCode, false, false, false)
}

func stoppedResultForTest(state *State, stdout, stderr []byte, exitCode int) *CommandResult {
	result := resultForTest(state, stdout, stderr, exitCode)
	result.Action = CommandStop
	return result
}

func uncertainResultForTest(state *State, stdout, stderr []byte, exitCode int, stdoutUnresolved, stderrUnresolved, exitUnresolved bool) *CommandResult {
	result := &CommandResult{}
	result.AddOutput(state, &CommandOutput{
		Stdout:   newUncertain(stdout, stdoutUnresolved),
		Stderr:   newUncertain(stderr, stderrUnresolved),
		ExitCode: newUncertain(exitCode, exitUnresolved),
	})
	return result
}

func noOpDispatch(_ context.Context, state *State, _ *Invocation) (*CommandResult, error) {
	return resultForTest(state, nil, nil, 0), nil
}

func lookupAllCommands(command testCommand) CommandLookupFunc {
	return func(name string) *CommandDefinition {
		if builtin := runtimeTestBuiltin(name); builtin != nil {
			return builtin
		}
		return &CommandDefinition{Command: adaptTestCommand(command), Candidate: true, UserOverride: true}
	}
}

func runtimeTestBuiltin(name string) *CommandDefinition {
	definition := &CommandDefinition{Builtin: true}
	switch name {
	case ":", "true":
		definition.Command = func(_ context.Context, execution *CommandContext, _ *Invocation) (*CommandResult, error) {
			return execution.Result(execution.Output().Build()), nil
		}
	case "false":
		definition.Command = func(_ context.Context, execution *CommandContext, _ *Invocation) (*CommandResult, error) {
			return execution.Result(execution.Output().ExitCode(Resolved(1)).Build()), nil
		}
	case "echo":
		definition.Command = func(_ context.Context, execution *CommandContext, invocation *Invocation) (*CommandResult, error) {
			arguments := invocation.Args
			newline := true
			if len(arguments) != 0 && arguments[0].Kind == ArgumentString && arguments[0].Value == "-n" {
				newline = false
				arguments = arguments[1:]
			}
			values := make([]string, len(arguments))
			for index, argument := range arguments {
				if argument.Kind != ArgumentString {
					return execution.Result(execution.Output().Stdout(Unresolved[[]byte](nil)).Build()), nil
				}
				values[index] = argument.Value
			}
			output := strings.Join(values, " ")
			if newline {
				output += "\n"
			}
			return execution.Result(execution.Output().Stdout(Resolved([]byte(output))).Build()), nil
		}
	case "set":
		definition.Command = runtimeTestSetCommand
	case "shift":
		definition.Command = runtimeTestShiftCommand
	case "wait":
		definition.Command = runtimeTestSuccessCommand
	case "pwd":
		definition.Command = runtimeTestPWDCommand
	case "printf":
		definition.Command = runtimeTestPrintfCommand
	case "read":
		definition.Command = runtimeTestReadCommand
	case "unset":
		definition.Command = runtimeTestUnsetCommand
	case "test", "[":
		definition.Command = runtimeTestTestCommand
	case "declare", "local", "export", "readonly", "typeset":
		definition.Command = runtimeTestDeclarationCommand
	case "let":
		definition.Command = runtimeTestLetCommand
	default:
		return nil
	}
	return definition
}

func lookupCommands(exists func(string) bool, command testCommand) CommandLookupFunc {
	return func(name string) *CommandDefinition {
		if exists(name) {
			return &CommandDefinition{Command: adaptTestCommand(command), Candidate: true, UserOverride: true}
		}
		return nil
	}
}

func adaptTestCommand(command testCommand) Command {
	return func(ctx context.Context, commandContext *CommandContext, invocation *Invocation) (*CommandResult, error) {
		return command(ctx, commandContext.State(), invocation)
	}
}

func newState(request *Request, maximum int) *State {
	state, err := initializeState(request, maximum)
	if err != nil {
		panic(err)
	}
	return state
}

func newNoOpExecutor(ctx context.Context, maxExecutionSteps int, request *Request) (*ExecutionContext, *State) {
	return newExecutorForTest(ctx, maxExecutionSteps, request, noOpDispatch)
}

func newExecutorForTest(ctx context.Context, maxExecutionSteps int, request *Request, dispatch testCommand) (*ExecutionContext, *State) {
	config := Config{MaxExecutionSteps: maxExecutionSteps, MaxMemoryBytes: defaultMaxMemoryBytes, LookupCommand: lookupAllCommands(dispatch)}
	return &ExecutionContext{ctx: ctx, config: config}, newState(request, config.MaxMemoryBytes)
}

func evaluateForTest(ctx context.Context, file *syntax.File, request *Request, config *Config) ([]*pathResult, int, error) {
	runtimeConfig := *config
	runtimeConfig.MaxMemoryBytes = normalizedMaxMemoryBytes(runtimeConfig.MaxMemoryBytes)
	s := newState(request, runtimeConfig.MaxMemoryBytes)
	candidates, err := buildCandidateIndex(ctx, file, commandCandidateLookup(config.LookupCommand))
	if err != nil {
		return []*pathResult{{state: s, status: StatusIncomplete}}, 0, err
	}
	e := &ExecutionContext{ctx: ctx, config: runtimeConfig, candidates: candidates}
	paths, err := e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, file.Stmts)
	return paths, e.executedSteps, err
}

func requireCancellationAfterDispatch(t testing.TB, expectedCommand string, evaluate func(*ExecutionContext, *State) ([]*pathResult, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e, s := newExecutorForTest(ctx, 20, &Request{}, func(_ context.Context, state *State, command *Invocation) (*CommandResult, error) {
		if command.Name != expectedCommand {
			t.Fatalf("unexpected dispatch=%#v", command)
		}
		cancel()
		return resultForTest(state, nil, nil, 0), nil
	})
	paths, err := evaluate(e, s)
	if !errors.Is(err, context.Canceled) || len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("paths=%#v err=%v", paths, err)
	}
}
