package runtime

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
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
		arguments, err = e.declarationCommandArguments(s, declaration, expand.Indexed, nil)
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

func (e *ExecutionContext) declarationCommandArguments(s *State, declaration *syntax.DeclClause, kind expand.ValueKind, prepared *commandExpansions) ([]*Argument, error) {
	if _, ok := multiplyMaterialization(len(declaration.Args), materialize.EntryBytes, e.config.MaxMemoryBytes); !ok {
		return nil, materialize.LimitError(e.config.MaxMemoryBytes)
	}
	arguments := make([]*Argument, 0, len(declaration.Args))
	for _, assignment := range declaration.Args {
		if assignment.Name == nil {
			value, unknown, err := prepared.literal(e, s, assignment.Value)
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
			if _, _, _, err := prepared.array(e, s, assignment.Array, kind); err != nil {
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
			index, indexUnknown, err := prepared.literal(e, s, indexWord)
			if err != nil {
				return nil, err
			}
			value := ""
			valueUnknown := false
			if assignment.Value != nil {
				value, valueUnknown, err = prepared.literal(e, s, assignment.Value)
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
		value, unknown, err := prepared.literal(e, s, assignment.Value)
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
	if _, ok := multiplyMaterialization(len(clause.Exprs), materialize.EntryBytes, e.config.MaxMemoryBytes); !ok {
		return nil, materialize.LimitError(e.config.MaxMemoryBytes)
	}
	arguments := make([]*Argument, 0, len(clause.Exprs))
	materialized := 0
	chargeLast := func() error {
		if len(arguments) == 0 {
			return nil
		}
		var err error
		materialized, err = e.addMaterializedString(materialized, arguments[len(arguments)-1].Value)
		return err
	}
	for _, expression := range clause.Exprs {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		if err := chargeLast(); err != nil {
			return nil, err
		}
		value, unknown, err := e.letArgument(s, expression)
		if err != nil {
			return nil, err
		}
		if unknown {
			arguments = append(arguments, &Argument{Kind: ArgumentUnresolved})
		} else {
			arguments = append(arguments, &Argument{Kind: ArgumentString, Value: value})
		}
	}
	if err := chargeLast(); err != nil {
		return nil, err
	}
	return arguments, nil
}

// The parser represents unquoted let operands as arithmetic syntax. Expand
// their Shell words, not the arithmetic itself, then reuse the syntax printer
// to reconstruct the single argument. Bare variable names remain literal.
func (e *ExecutionContext) letArgument(s *State, expression syntax.ArithmExpr) (string, bool, error) {
	if word, ok := expression.(*syntax.Word); ok {
		return e.literalValueWithCertainty(s, word)
	}
	original := make(map[*syntax.Word][]syntax.WordPart)
	defer func() {
		for word, parts := range original {
			word.Parts = parts
		}
	}()
	var expansionErr error
	unknown, materialized := false, 0
	syntax.Walk(expression, func(node syntax.Node) bool {
		if expansionErr != nil {
			return false
		}
		if expansionErr = e.ctx.Err(); expansionErr != nil {
			return false
		}
		word, ok := node.(*syntax.Word)
		if !ok {
			return true
		}
		value, unresolved, err := e.literalValueWithCertainty(s, word)
		if err == nil {
			materialized, err = e.addMaterializedString(materialized, value)
		}
		expansionErr = err
		unknown = unknown || unresolved
		original[word] = word.Parts
		word.Parts = []syntax.WordPart{&syntax.Lit{Value: value}}
		return false
	})
	if expansionErr != nil || unknown {
		return "", unknown, expansionErr
	}
	var source bytes.Buffer
	err := syntax.NewPrinter(syntax.SingleLine(true)).Print(&source, &syntax.LetClause{Exprs: []syntax.ArithmExpr{expression}})
	return strings.TrimPrefix(source.String(), "let "), false, err
}
