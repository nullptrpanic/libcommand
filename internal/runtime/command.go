package runtime

import (
	iofs "io/fs"

	"mvdan.cc/sh/v3/syntax"
)

func commandDefinitionExecutable(definition *CommandDefinition) bool {
	return definition != nil && definition.Command != nil
}

func (e *ExecutionContext) lookupCommandDefinition(name string) *CommandDefinition {
	if definition := lookupLanguageControl(name); definition != nil {
		return definition
	}
	if e.config.LookupCommand == nil {
		return nil
	}
	return e.config.LookupCommand(name)
}

// Populate the final call snapshot only after syntax operands are prepared and
// their combined retained size has been checked.
func populateCommandInvocation(s *State, invocation *Invocation, internal bool) {
	if len(invocation.Args) == 0 {
		invocation.Args = nil
	}
	directory, directoryUnresolved := s.dir.Data()
	input, stdinUnresolved := s.stdin.Data()
	invocation.Dir, invocation.Stdin = directory, input
	var unresolvedEnvironment []string
	if !internal {
		invocation.Env = s.vars.exported()
		unresolvedEnvironment = unknownExportedVariables(s)
		for _, variable := range unresolvedEnvironment {
			invocation.Env[variable] = ""
		}
		invocation.Stdin = append([]byte(nil), input...)
	}
	nameUnresolved := invocation.Name == ""
	if nameUnresolved || len(unresolvedEnvironment) != 0 || stdinUnresolved || directoryUnresolved {
		invocation.Unresolved = &InvocationUnresolved{
			Name:  nameUnresolved,
			Env:   unresolvedEnvironment,
			Dir:   directoryUnresolved,
			Stdin: stdinUnresolved,
		}
	}
	if stdinUnresolved {
		invocation.Stdin = nil
	}
	if directoryUnresolved {
		invocation.Dir = ""
	}
}

func concreteArguments(values []string) []*Argument {
	if len(values) == 0 {
		return nil
	}
	arguments := make([]*Argument, len(values))
	for index, value := range values {
		arguments[index] = &Argument{Kind: ArgumentString, Value: value}
	}
	return arguments
}

func concreteArgumentValues(arguments []*Argument) []string {
	if len(arguments) == 0 {
		return nil
	}
	values := make([]string, len(arguments))
	for index := range arguments {
		values[index] = arguments[index].Value
	}
	return values
}

func hasUnresolvedArguments(arguments []*Argument) bool {
	for index := range arguments {
		if arguments[index].Kind == ArgumentUnresolved {
			return true
		}
	}
	return false
}

func (e *ExecutionContext) expandCallArguments(s *State, words []*syntax.Word) (fields []*Argument, err error) {
	originalVars := s.vars
	transactionStarted := false
	defer func() {
		if err != nil && transactionStarted {
			s.vars = originalVars
		}
	}()
	materializedBytes := 0
	directoryCache := make(map[string][]iofs.DirEntry)
	for _, word := range words {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		if positional, handled := quotedPositionalArguments(s, word); handled {
			for _, argument := range positional {
				materializedBytes, err = e.addMaterializedString(materializedBytes, argument.Value)
				if err != nil {
					return nil, err
				}
			}
			fields = append(fields, positional...)
			continue
		}
		if err := normalizeWordArithmeticLiterals(word); err != nil {
			return nil, err
		}
		certainty := wordCertainty(s, word)
		if err := validateParameterExpansions(s, word, certainty.dataUnknown()); err != nil {
			return nil, err
		}
		if err := e.checkBraceExpansion(word, materializedBytes); err != nil {
			return nil, err
		}
		restore := maskInactiveParameterWords(s, word)
		expanded, nextMaterializedBytes, expandErr := e.expandPreparedWordFields(
			s,
			word,
			materializedBytes,
			directoryCache,
			restore,
			func() {
				if !transactionStarted && wordMayMutateVariables(word) {
					s.vars = s.vars.clone()
					transactionStarted = true
				}
			},
		)
		if expandErr != nil {
			return nil, expandErr
		}
		materializedBytes = nextMaterializedBytes
		if certainty.hostUnknown() || certainty.dataUnknown() {
			fields = append(fields, &Argument{Kind: ArgumentUnresolved})
			continue
		}
		fields = append(fields, concreteArguments(expanded)...)
	}
	return fields, nil
}

// Quoted $@ has a known number of fields even when some values are unknown.
// Other unknown expansions may also have unknown field cardinality.
func quotedPositionalArguments(s *State, word *syntax.Word) ([]*Argument, bool) {
	if word == nil || len(word.Parts) != 1 {
		return nil, false
	}
	quoted, ok := word.Parts[0].(*syntax.DblQuoted)
	if !ok || len(quoted.Parts) != 1 {
		return nil, false
	}
	parameter, ok := quoted.Parts[0].(*syntax.ParamExp)
	if !ok || parameter.Param.Value != "@" || parameter.Length || parameter.Excl || parameter.Width || parameter.Index != nil || parameter.Slice != nil || parameter.Repl != nil || parameter.Exp != nil {
		return nil, false
	}
	return s.positionalArguments(), true
}
