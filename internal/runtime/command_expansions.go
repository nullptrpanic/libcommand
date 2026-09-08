package runtime

import (
	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Parser-special command operands are expanded once before middleware. These
// values live only for that call, never in State or across statement retries.
type commandExpansions struct {
	literals map[*syntax.Word]*uncertain[string]
	arrays   map[*syntax.ArrayExpr]*preparedArray
	bytes    int
	// Once prepared, execution only reuses entries. Newly parsed literal
	// assignments must not grow this retained cache after its budget check.
	ready bool
}

type preparedArray struct {
	value   expand.Variable
	slots   map[int]struct{}
	unknown bool
}

func (c *CommandContext) preparedExpansions() *commandExpansions {
	if c.expansions == nil {
		c.expansions = &commandExpansions{literals: make(map[*syntax.Word]*uncertain[string]), arrays: make(map[*syntax.ArrayExpr]*preparedArray)}
	}
	return c.expansions
}

// PrepareLiteral expands a parser-special operand once and retains its typed
// result for ExpandLiteral and ApplyAssignments during this command only.
func (c *CommandContext) PrepareLiteral(word *syntax.Word) (string, bool, error) {
	return c.preparedExpansions().literal(c.execution, c.state, word)
}

// PrepareDeclarationArguments materializes a declaration's call arguments and
// retains compound array values for ApplyAssignments. The caller owns option
// parsing and supplies the declared array kind.
func (c *CommandContext) PrepareDeclarationArguments(clause *syntax.DeclClause, kind expand.ValueKind) ([]*Argument, error) {
	return c.execution.declarationCommandArguments(c.state, clause, kind, c.preparedExpansions())
}

// PrepareLetArguments normalizes a parser-special arithmetic call to ordinary
// arguments. Arithmetic evaluation of those arguments remains caller-owned.
func (c *CommandContext) PrepareLetArguments(clause *syntax.LetClause) ([]*Argument, error) {
	return c.execution.letCommandArguments(c.state, clause)
}

func (prepared *commandExpansions) literal(e *ExecutionContext, s *State, word *syntax.Word) (string, bool, error) {
	if prepared != nil {
		if value := prepared.literals[word]; value != nil {
			data, unknown := value.Data()
			return data, unknown, nil
		}
	}
	value, unknown, err := e.literalValueWithCertainty(s, word)
	if err == nil && prepared != nil && !prepared.ready {
		if err := prepared.charge(e, s, materialize.EntryBytes+len(value)); err != nil {
			return "", false, err
		}
		prepared.literals[word] = newUncertain(value, unknown)
	}
	return value, unknown, err
}

func (prepared *commandExpansions) array(e *ExecutionContext, s *State, array *syntax.ArrayExpr, kind expand.ValueKind) (expand.Variable, map[int]struct{}, bool, error) {
	if prepared != nil {
		if value := prepared.arrays[array]; value != nil {
			return cloneVariable(value.value), value.slots, value.unknown, nil
		}
	}
	if kind == expand.Unknown {
		kind = expand.Indexed
	}
	value, slots, unknown, err := e.arrayValue(s, array, kind)
	if err == nil && prepared != nil && !prepared.ready {
		if err := prepared.charge(e, s, materialize.EntryBytes+variableValueBytes(&value)+indexedSlotsBytes("", slots)); err != nil {
			return expand.Variable{}, nil, false, err
		}
		prepared.arrays[array] = &preparedArray{value: value, slots: slots, unknown: unknown}
	}
	return value, slots, unknown, err
}

func (prepared *commandExpansions) charge(e *ExecutionContext, s *State, size int) error {
	var ok bool
	prepared.bytes, ok = materialize.Add(prepared.bytes, size, e.config.MaxMemoryBytes)
	if !ok {
		return materialize.LimitError(e.config.MaxMemoryBytes)
	}
	return e.checkPathsMaterialization([]*pathResult{{state: s, status: StatusCompleted}}, prepared.bytes, unknownLocation)
}
