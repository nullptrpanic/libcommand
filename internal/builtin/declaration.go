package builtin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type declarationSpec struct {
	name        string
	kind        expand.ValueKind
	exported    bool
	readOnly    bool
	local       bool
	assignments []*syntax.Assign
}

type declarationError struct {
	message  string
	exitCode int
}

func (err *declarationError) Error() string {
	return err.message
}

func executeDeclaration(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	spec, resolved, err := declarationForInvocation(shell, invocation)
	if err != nil {
		var usage *declarationError
		if errors.As(err, &usage) {
			return commandResult(shell, nil, []byte(usage.message+"\n"), usage.exitCode), nil
		}
		return shell.ExpansionError(err, err.Error()), nil
	}
	if !resolved {
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	return applyDeclaration(shell, spec)
}

func declarationForInvocation(shell *runtime.CommandContext, invocation *runtime.Invocation) (*declarationSpec, bool, error) {
	if clause, ok := shell.CommandSyntax().(*syntax.DeclClause); ok && clause.Variant != nil && clause.Variant.Value == invocation.Name {
		return declarationFromClause(shell, clause)
	}
	arguments, concrete := concreteArguments(invocation)
	if !concrete {
		return nil, false, nil
	}
	return declarationFromArguments(shell, invocation.Name, arguments)
}

func declarationFromClause(shell *runtime.CommandContext, clause *syntax.DeclClause) (*declarationSpec, bool, error) {
	spec := newDeclarationSpec(shell, clause.Variant.Value)
	optionsEnded := false
	for _, assignment := range clause.Args {
		if assignment.Name != nil {
			spec.assignments = append(spec.assignments, assignment)
			optionsEnded = true
			continue
		}
		argument, unknown, err := shell.ExpandLiteral(assignment.Value)
		if err != nil {
			return nil, false, err
		}
		if unknown {
			return nil, false, nil
		}
		if !optionsEnded && strings.HasPrefix(argument, "-") {
			ended, err := applyDeclarationOption(spec, argument)
			if err != nil {
				return nil, false, err
			}
			if ended {
				optionsEnded = true
			}
			continue
		}
		parsed, err := parseDeclarationAssignment(argument)
		if err != nil {
			return nil, false, invalidDeclarationIdentifier(spec.name, argument)
		}
		spec.assignments = append(spec.assignments, parsed)
		optionsEnded = true
	}
	return spec, true, nil
}

func declarationFromArguments(shell *runtime.CommandContext, name string, arguments []string) (*declarationSpec, bool, error) {
	spec := newDeclarationSpec(shell, name)
	index := 0
	for index < len(arguments) && strings.HasPrefix(arguments[index], "-") {
		ended, err := applyDeclarationOption(spec, arguments[index])
		if err != nil {
			return nil, false, err
		}
		index++
		if ended {
			break
		}
	}
	for _, argument := range arguments[index:] {
		assignment, err := parseDeclarationAssignment(argument)
		if err != nil {
			return nil, false, invalidDeclarationIdentifier(name, argument)
		}
		spec.assignments = append(spec.assignments, assignment)
	}
	return spec, true, nil
}

func newDeclarationSpec(shell *runtime.CommandContext, name string) *declarationSpec {
	return &declarationSpec{
		name:     name,
		kind:     expand.Unknown,
		exported: name == "export",
		readOnly: name == "readonly",
		local:    name == "local" || shell.FunctionDepth() > 0 && (name == "declare" || name == "typeset"),
	}
}

func applyDeclarationOption(spec *declarationSpec, argument string) (bool, error) {
	if argument == "--" {
		return true, nil
	}
	for _, option := range strings.TrimPrefix(argument, "-") {
		switch option {
		case 'a':
			spec.kind = expand.Indexed
		case 'A':
			spec.kind = expand.Associative
		case 'x':
			spec.exported = true
		case 'r':
			spec.readOnly = true
		default:
			return false, &declarationError{message: fmt.Sprintf("%s: unsupported option -%c", spec.name, option), exitCode: 2}
		}
	}
	return false, nil
}

func invalidDeclarationIdentifier(name, argument string) error {
	return &declarationError{message: fmt.Sprintf("%s: `%s': not a valid identifier", name, argument), exitCode: 1}
}

func parseDeclarationAssignment(argument string) (*syntax.Assign, error) {
	name, value, assigned := strings.Cut(argument, "=")
	appendMode := assigned && strings.HasSuffix(name, "+")
	if appendMode {
		name = strings.TrimSuffix(name, "+")
	}
	if !syntax.ValidName(name) {
		return nil, fmt.Errorf("invalid variable name")
	}
	assignment := &syntax.Assign{Name: &syntax.Lit{Value: name}, Naked: !assigned, Append: appendMode}
	if assigned {
		assignment.Value = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
	}
	return assignment, nil
}

func applyDeclaration(shell *runtime.CommandContext, spec *declarationSpec) (*runtime.CommandResult, error) {
	if spec.local && !shell.LocalScopeAvailable() {
		return commandResult(shell, nil, []byte("local: can only be used in a function\n"), 1), nil
	}
	for _, assignment := range spec.assignments {
		if assignment.Name == nil || !shell.Variable(assignment.Name.Value).ReadOnly {
			continue
		}
		if !spec.local && assignment.Naked {
			continue
		}
		name := assignment.Name.Value
		message := fmt.Sprintf("%s: readonly variable\n", name)
		if spec.local {
			message = fmt.Sprintf("%s: %s: readonly variable\n", spec.name, name)
		}
		return commandResult(shell, nil, []byte(message), 1), nil
	}
	snapshot := shell.Snapshot()
	restore := func() {
		shell.Restore(snapshot)
	}
	if spec.local {
		for _, assignment := range spec.assignments {
			if assignment.Name == nil {
				continue
			}
			name := assignment.Name.Value
			shell.SaveLocal(name)
			if assignment.Naked {
				if err := shell.UnsetVariable(name); err != nil {
					restore()
					return nil, err
				}
			}
		}
	}
	if err := shell.ApplyAssignments(spec.assignments, spec.kind, spec.exported); err != nil {
		restore()
		return shell.ExpansionError(err, err.Error()), nil
	}
	for _, assignment := range spec.assignments {
		if assignment.Name == nil {
			continue
		}
		name := assignment.Name.Value
		value := shell.Variable(name)
		value.Local = value.Local || spec.local
		value.ReadOnly = value.ReadOnly || spec.readOnly
		if err := shell.DefineVariable(name, value, shell.VariableUnknown(name), shell.IndexedSlots(name)); err != nil {
			restore()
			return nil, err
		}
	}
	return commandResult(shell, nil, nil, 0), nil
}
