package runtime

import (
	"bytes"
	"fmt"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) evaluateDeclarationCommand(s *State, declaration *syntax.DeclClause) ([]*pathResult, error) {
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(declaration)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	var arguments []*Argument
	if e.syntaxCommandNeedsExpandedArguments(s, declaration.Variant.Value) {
		snapshot := s.clone()
		var err error
		arguments, err = e.declarationCommandArguments(s, declaration)
		if err != nil {
			*s = *snapshot
			result, resultErr := e.outcomeFromExpansionError(s, err, fmt.Sprintf("expand declaration arguments: %v", err), sourceLocation(declaration))
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
	}
	return e.evaluateExpandedCommand(s, declaration, nil, declaration.Variant.Value, arguments, declaration)
}

func (e *ExecutionContext) syntaxCommandNeedsExpandedArguments(s *State, name string) bool {
	if s.functions[name] != nil {
		return true
	}
	definition := e.lookupCommandDefinition(name)
	return definition == nil || definition.UserOverride || !definition.Builtin
}

func (e *ExecutionContext) declarationCommandArguments(s *State, declaration *syntax.DeclClause) ([]*Argument, error) {
	arguments := make([]*Argument, 0, len(declaration.Args))
	for _, assignment := range declaration.Args {
		if assignment.Name == nil {
			value, unknown, err := e.literalValueWithCertainty(s, assignment.Value)
			if err != nil {
				return nil, err
			}
			if unknown {
				arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
			} else {
				arguments = append(arguments, &Argument{Kind: ArgumentString, Value: value})
			}
			continue
		}

		name := assignment.Name.Value
		if assignment.Array != nil {
			if _, _, _, err := e.arrayValue(s, assignment.Array, expand.Indexed); err != nil {
				return nil, err
			}
			if assignment.Append {
				name += "+"
			}
			arguments = append(arguments, &Argument{Kind: ArgumentString, Value: name})
			continue
		}
		if assignment.Index != nil {
			indexWord, concrete := assignment.Index.(*syntax.Word)
			if !concrete {
				arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
				continue
			}
			index, indexUnknown, err := e.literalValueWithCertainty(s, indexWord)
			if err != nil {
				return nil, err
			}
			value := ""
			valueUnknown := false
			if assignment.Value != nil {
				value, valueUnknown, err = e.literalValueWithCertainty(s, assignment.Value)
				if err != nil {
					return nil, err
				}
			}
			if indexUnknown || valueUnknown {
				arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
				continue
			}
			argument := name + "[" + index + "]"
			if !assignment.Naked {
				if assignment.Append {
					argument += "+"
				}
				argument += "=" + value
			}
			arguments = append(arguments, &Argument{Kind: ArgumentString, Value: argument})
			continue
		}
		if assignment.Naked {
			arguments = append(arguments, &Argument{Kind: ArgumentString, Value: name})
			continue
		}
		value, unknown, err := e.literalValueWithCertainty(s, assignment.Value)
		if err != nil {
			return nil, err
		}
		if unknown {
			arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
			continue
		}
		operator := "="
		if assignment.Append {
			operator = "+="
		}
		arguments = append(arguments, &Argument{Kind: ArgumentString, Value: name + operator + value})
	}
	return arguments, nil
}

func (e *ExecutionContext) evaluateLetCommand(s *State, clause *syntax.LetClause) ([]*pathResult, error) {
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(clause)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	var arguments []*Argument
	if e.syntaxCommandNeedsExpandedArguments(s, "let") {
		snapshot := s.clone()
		var err error
		arguments, err = e.letCommandArguments(s, clause)
		if err != nil {
			*s = *snapshot
			result, resultErr := e.outcomeFromExpansionError(s, err, fmt.Sprintf("expand let arguments: %v", err), sourceLocation(clause))
			return []*pathResult{{state: s, status: result.status}}, resultErr
		}
	}
	return e.evaluateExpandedCommand(s, clause, nil, "let", arguments, clause)
}

func (e *ExecutionContext) letCommandArguments(s *State, clause *syntax.LetClause) ([]*Argument, error) {
	arguments := make([]*Argument, 0, len(clause.Exprs))
	printer := syntax.NewPrinter()
	for _, expression := range clause.Exprs {
		if arithmHasHostUnknown(s, expression) {
			return nil, fmt.Errorf("let expression depends on host runtime state")
		}
		if arithmHasUnknownData(s, expression) {
			arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
			continue
		}
		if word, ok := expression.(*syntax.Word); ok {
			value, unknown, err := e.literalValueWithCertainty(s, word)
			if err != nil {
				return nil, err
			}
			if unknown {
				arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
			} else {
				arguments = append(arguments, &Argument{Kind: ArgumentString, Value: value})
			}
			continue
		}
		var source bytes.Buffer
		if err := printer.Print(&source, expression); err != nil {
			return nil, err
		}
		arguments = append(arguments, &Argument{Kind: ArgumentString, Value: source.String()})
	}
	return arguments, nil
}
