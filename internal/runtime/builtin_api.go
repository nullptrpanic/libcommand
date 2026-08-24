package runtime

import (
	"context"
	"strconv"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// ArithmeticResult is one arithmetic evaluation requested by a command.
type ArithmeticResult struct {
	Value   int
	Unknown bool
	Failure string
}

// PathKind describes one path in the virtual filesystem.
type PathKind uint8

const (
	PathMissing PathKind = iota
	PathFile
	PathDirectory
	PathDevice
)

// CommandKind describes the first definition selected by shell lookup.
type CommandKind uint8

const (
	CommandMissing CommandKind = iota
	CommandFunction
	CommandBuiltin
	CommandFile
)

func (c *CommandContext) Arithmetic(expression syntax.ArithmExpr) (*ArithmeticResult, error) {
	if arithmHasUnknownData(c.state, expression) {
		return &ArithmeticResult{Unknown: true}, nil
	}
	value, err := c.execution.arithmeticExpressionValue(c.state, expression)
	if err == nil {
		return &ArithmeticResult{Value: value}, nil
	}
	if expansionRequested(err) || isUnknownValueError(err) {
		return nil, err
	}
	if _, incomplete := c.execution.incompleteEvaluationIssue(err); incomplete {
		return nil, err
	}
	return &ArithmeticResult{Failure: err.Error()}, nil
}

// ApplyAssignments applies parsed Shell assignments to the active command
// state. Expansion, readonly checks, and materialization budgets remain owned
// by the runtime.
func (c *CommandContext) ApplyAssignments(assignments []*syntax.Assign, declaredKind expand.ValueKind, exported bool) error {
	return c.execution.applyAssignments(c.state, assignments, declaredKind, exported)
}

// ExpandLiteral expands one word without field splitting or pathname
// expansion for a stateful builtin argument.
func (c *CommandContext) ExpandLiteral(word *syntax.Word) (string, bool, error) {
	return c.execution.literalValueWithCertainty(c.state, word)
}

func (c *CommandContext) MaxMemoryBytes() int {
	return normalizedMaxMemoryBytes(c.execution.config.MaxMemoryBytes)
}

func (c *CommandContext) Directory() (string, bool) {
	return c.state.dir.Data()
}

func (c *CommandContext) SetDirectory(directory string, unresolved bool) {
	if unresolved {
		c.state.dir = newUnresolved(directory)
		return
	}
	c.state.dir = newCertain(directory)
}

func (c *CommandContext) ResolvePath(name string) string {
	directory, _ := c.state.dir.Data()
	return c.state.fs.resolve(directory, name)
}

func (c *CommandContext) PathKind(name string) PathKind {
	return c.state.fs.pathKind(name)
}

func (c *CommandContext) EnsureDirectory(name string) error {
	return c.state.fs.ensureDir(name)
}

// RemovePath removes one resolved path from the virtual filesystem. Recursive
// removal preserves the virtual root while deleting all of its contents.
func (c *CommandContext) RemovePath(ctx context.Context, name string, recursive bool) error {
	return c.state.fs.remove(ctx, name, recursive)
}

func (c *CommandContext) Format(format string, arguments []string) (string, error) {
	value, _, err := expand.Format(c.execution.expansionConfig(c.state), format, arguments)
	return value, err
}

func (c *CommandContext) ParseInteger(value string) (int64, bool, error) {
	return parseBashIntegerLiteral(value)
}

func (c *CommandContext) ParseArithmetic(source string) (syntax.ArithmExpr, error) {
	return ParseArithmetic(c.execution.ctx, source)
}

// Input returns the remaining stdin bytes and whether the complete stream is
// unresolved.
func (c *CommandContext) Input() ([]byte, bool) {
	input, unresolved := c.state.stdin.Data()
	return append([]byte(nil), input...), unresolved
}

// InputUnresolved reports whether the remaining stdin stream is unresolved
// without materializing a copy of its concrete prefix.
func (c *CommandContext) InputUnresolved() bool {
	_, unresolved := c.state.stdin.Data()
	return unresolved
}

// SetInput replaces the remaining stdin bytes and their resolution state.
func (c *CommandContext) SetInput(input []byte, unresolved bool) {
	input = append([]byte(nil), input...)
	c.setInputView(input, unresolved)
}

// ConsumeInput returns and removes one input segment without copying the
// unconsumed suffix. maximum zero means no byte limit. exact ignores the
// delimiter and succeeds only when maximum bytes are available. When
// joinEscapedLines is true, backslash-newline pairs are removed before the
// terminating newline is found.
func (c *CommandContext) ConsumeInput(delimiter byte, maximum int, exact, joinEscapedLines bool) ([]byte, bool, bool) {
	input, unresolved := c.state.stdin.Data()
	if maximum > 0 && exact {
		count := min(maximum, len(input))
		result := append([]byte(nil), input[:count]...)
		c.setInputView(input[count:len(input):len(input)], unresolved)
		return result, count == maximum, unresolved
	}

	var result []byte
	segmentStart := 0
	for index, value := range input {
		if value == delimiter {
			if joinEscapedLines && delimiter == '\n' {
				backslashes := 0
				for position := index - 1; position >= segmentStart && input[position] == '\\'; position-- {
					backslashes++
				}
				if backslashes%2 != 0 {
					result = append(result, input[segmentStart:index-1]...)
					segmentStart = index + 1
					continue
				}
			}
			result = append(result, input[segmentStart:index]...)
			c.setInputView(input[index+1:len(input):len(input)], unresolved)
			return result, true, unresolved
		}
		if maximum > 0 && index+1 == maximum {
			result = append(result, input[segmentStart:maximum]...)
			c.setInputView(input[maximum:len(input):len(input)], unresolved)
			return result, true, unresolved
		}
	}
	result = append(result, input[segmentStart:]...)
	c.setInputView(nil, unresolved)
	return result, false, unresolved
}

func (c *CommandContext) setInputView(input []byte, unresolved bool) {
	if unresolved {
		c.state.stdin = newUnresolved(input)
		return
	}
	c.state.stdin = newCertain(input)
}

func (c *CommandContext) CandidateContext() bool {
	return c.execution.discoveryDepth > 0
}

func (c *CommandContext) FunctionDepth() int {
	return c.state.funcDepth
}

func (c *CommandContext) SnapshotForUnknownFailure() *State {
	return c.state.snapshotForUnknownFailure()
}

func (c *CommandContext) Snapshot() *State {
	return c.state.clone()
}

func (c *CommandContext) Restore(snapshot *State) {
	pathID := c.state.pathID
	parent := c.state.parent
	retainedParentBytes := c.state.retainedParentBytes
	*c.state = *snapshot
	c.state.pathID = pathID
	c.state.parent = parent
	c.state.retainedParentBytes = retainedParentBytes
	c.state.frozen = false
	c.state.frozenBytes = 0
}

func (c *CommandContext) SetUnknownExitWithFailure(snapshot *State) {
	c.state.setUnknownExitCodeWithFailure(snapshot)
}

func (c *CommandContext) EachVariable(yield func(string, *expand.Variable) bool) {
	c.state.vars.Each(func(name string, value expand.Variable) bool {
		return yield(name, &value)
	})
}

func (c *CommandContext) Option(name string) (bool, bool) {
	switch name {
	case "allexport":
		return c.state.options.allExport, true
	case "errexit":
		return c.state.options.errexit, true
	case "errtrace":
		return c.state.options.errTrace, true
	case "noglob":
		return c.state.options.noGlob, true
	case "nounset":
		return c.state.options.noUnset, true
	case "pipefail":
		return c.state.options.pipefail, true
	case "verbose":
		return c.state.options.verbose, true
	case "xtrace":
		return c.state.options.xtrace, true
	case "dotglob":
		return c.state.options.dotGlob, true
	case "extglob":
		return c.state.options.extGlob, true
	case "globstar":
		return c.state.options.globStar, true
	case "inherit_errexit":
		return c.state.options.inheritErrexit, true
	case "nocaseglob":
		return c.state.options.noCaseGlob, true
	case "nullglob":
		return c.state.options.nullGlob, true
	case "command-string":
		return c.state.options.commandString, true
	default:
		return false, false
	}
}

func (c *CommandContext) SetOption(name string, enabled bool) bool {
	return setShellOption(c.state, name, enabled)
}

func setShellOption(state *State, name string, enabled bool) bool {
	switch name {
	case "allexport":
		state.options.allExport = enabled
	case "errexit":
		state.options.errexit = enabled
	case "errtrace":
		state.options.errTrace = enabled
	case "noglob":
		state.options.noGlob = enabled
	case "nounset":
		state.options.noUnset = enabled
	case "pipefail":
		state.options.pipefail = enabled
	case "verbose":
		state.options.verbose = enabled
	case "xtrace":
		state.options.xtrace = enabled
	case "dotglob":
		state.options.dotGlob = enabled
	case "extglob":
		state.options.extGlob = enabled
	case "globstar":
		state.options.globStar = enabled
	case "inherit_errexit":
		state.options.inheritErrexit = enabled
	case "nocaseglob":
		state.options.noCaseGlob = enabled
	case "nullglob":
		state.options.nullGlob = enabled
	case "command-string":
		state.options.commandString = enabled
	default:
		return false
	}
	return true
}

func (c *CommandContext) Variable(name string) *expand.Variable {
	value := c.state.vars.Get(name)
	return &value
}

func (c *CommandContext) VariableUnknown(name string) bool {
	return c.state.vars.isUnknown(name)
}

func (c *CommandContext) VariableVersion(name string) uint64 {
	return c.state.vars.version(name)
}

func (c *CommandContext) AssignVariable(name string, value *expand.Variable, unknown bool) error {
	if err := c.execution.checkVariableMaterialization(value); err != nil {
		return err
	}
	return c.mutateVariables(func() error {
		return assignShellVariable(c.state, name, *value, unknown)
	})
}

func (c *CommandContext) AssignIndexedVariable(name string, value *expand.Variable, unknown bool, slots map[int]struct{}) error {
	if err := c.execution.checkVariableMaterialization(value); err != nil {
		return err
	}
	return c.mutateVariables(func() error {
		return assignShellIndexedVariable(c.state, name, *value, unknown, slots)
	})
}

func (c *CommandContext) UnsetVariable(name string) error {
	return c.mutateVariables(func() error {
		return unsetShellVariable(c.state, name)
	})
}

func (c *CommandContext) PositionalArguments() []string {
	count, _ := strconv.Atoi(c.state.vars.Get("#").String())
	arguments := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		arguments = append(arguments, c.state.vars.Get(strconv.Itoa(index)).String())
	}
	return arguments
}

func (c *CommandContext) ReadFile(name string) ([]byte, bool, bool) {
	return c.state.fs.readFile(name)
}

func (c *CommandContext) ReplacePositionalArguments(arguments []string) {
	c.state.replacePositionalArguments(arguments)
}

func (c *CommandContext) RecordVariableRollback(name string) {
	c.execution.recordVariableRollback(c.state, name)
}

func (c *CommandContext) SaveLocal(name string) bool {
	if len(c.state.localScopes) == 0 {
		return false
	}
	c.state.saveLocal(name)
	return true
}

func (c *CommandContext) DeleteFunction(name string) {
	c.state.deleteFunction(name)
}

func (c *CommandContext) DefineVariable(name string, value *expand.Variable, unknown bool, slots map[int]struct{}) error {
	if err := validateShellVariableName(name); err != nil {
		return err
	}
	if err := c.execution.checkVariableMaterialization(value); err != nil {
		return err
	}
	return c.mutateVariables(func() error {
		c.state.vars.putIndexedWithCertainty(name, *value, unknown, slots)
		return nil
	})
}

func (c *CommandContext) mutateVariables(change func() error) (err error) {
	original := c.state.vars
	c.state.vars = c.state.vars.clone()
	defer func() {
		if err != nil {
			c.state.vars = original
		}
	}()
	if err = change(); err == nil {
		err = c.execution.checkStateMaterialization(c.state)
	}
	return err
}

func (c *CommandContext) IndexedSlots(name string) map[int]struct{} {
	return c.state.vars.indexedSlots(name)
}

func (c *CommandContext) LookupCommand(name string) CommandKind {
	if _, exists := c.state.functions[name]; exists {
		return CommandFunction
	}
	if definition := c.execution.lookupCommandDefinition(name); definition != nil && definition.Command != nil && !definition.Fallback {
		if definition.Builtin && !definition.UserOverride {
			return CommandBuiltin
		}
		return CommandFile
	}
	return CommandMissing
}

func (c *CommandContext) LocalScopeAvailable() bool {
	return len(c.state.localScopes) != 0
}

func (c *CommandContext) CommandState(name string) []uint64 {
	return append([]uint64(nil), c.state.commandStates[name]...)
}

func (c *CommandContext) SetCommandState(name string, values []uint64) {
	if c.state.commandStates == nil {
		c.state.commandStates = make(map[string][]uint64)
	}
	c.state.commandStates[name] = append([]uint64(nil), values...)
}

func (c *CommandContext) SetTrap(signal, command string) {
	c.state.setTrap(signal, command)
	if signal == "EXIT" {
		c.state.exitTrapInherited = false
	}
}

func (c *CommandContext) DeleteTrap(signal string) {
	c.state.deleteTrap(signal)
	if signal == "EXIT" {
		c.state.exitTrapInherited = false
	}
}

func (c *CommandContext) Traps() map[string]string {
	result := make(map[string]string, len(c.state.traps))
	for signal, command := range c.state.traps {
		result[signal] = command
	}
	return result
}
