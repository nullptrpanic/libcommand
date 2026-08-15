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

func (e *ExecutionContext) commandInvocation(s *State, name string, args []*Argument, input []byte, source *location, internal bool) (*Invocation, Status, error) {
	path := &pathResult{state: s, status: StatusCompleted}
	if !internal {
		invocationBytes, ok := commandInvocationMaterialization(s, name, args, input)
		if !ok {
			err := e.failMaterialization([]*pathResult{path}, source)
			return nil, path.status, err
		}
		if err := e.checkPathsMaterialization([]*pathResult{path}, invocationBytes, source); err != nil {
			return nil, path.status, err
		}
	}

	var invocationArguments []*Argument
	if len(args) != 0 {
		invocationArguments = args
	}
	directory, directoryUnresolved := s.dir.Data()
	_, stdinUnresolved := s.stdin.Data()
	invocation := &Invocation{Name: name, Args: invocationArguments, Dir: directory, Stdin: input}
	if internal {
		if stdinUnresolved || directoryUnresolved {
			invocation.Unresolved = &InvocationUnresolved{Dir: directoryUnresolved, Stdin: stdinUnresolved}
		}
		if stdinUnresolved {
			invocation.Stdin = nil
		}
		if directoryUnresolved {
			invocation.Dir = ""
		}
		return invocation, StatusCompleted, nil
	}

	invocation.Env = s.vars.exported()
	unresolvedEnvironment := unknownExportedVariables(s)
	for _, variable := range unresolvedEnvironment {
		invocation.Env[variable] = ""
	}
	if len(unresolvedEnvironment) != 0 || stdinUnresolved || directoryUnresolved {
		invocation.Unresolved = &InvocationUnresolved{
			Env:   unresolvedEnvironment,
			Dir:   directoryUnresolved,
			Stdin: stdinUnresolved,
		}
	}
	invocation.Stdin = append([]byte(nil), input...)
	if stdinUnresolved {
		invocation.Stdin = nil
	}
	if directoryUnresolved {
		invocation.Dir = ""
	}
	return invocation, StatusCompleted, nil
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
		if err := normalizeWordArithmeticLiterals(word); err != nil {
			return nil, err
		}
		if err := e.checkWordMaterialization(s, word, materializedBytes); err != nil {
			return nil, err
		}
		certainty := wordCertainty(s, word)
		if !certainty.dataUnknown() {
			if err := validateUnknownParameterExpansions(s, word); err != nil {
				return nil, err
			}
		}
		if err := e.checkBraceExpansion(word, materializedBytes); err != nil {
			return nil, err
		}
		restore := maskInactiveParameterWords(s, word)
		overrides, restoreParameters, prepareErr := e.prepareParameterExpansions(s, word)
		if prepareErr != nil {
			return nil, prepareErr
		}
		if !transactionStarted && wordMayMutateVariables(word) {
			s.vars = s.vars.clone()
			transactionStarted = true
		}
		expanded, nextMaterializedBytes, expandErr := e.expandFieldsWithinLimit(e.expansionConfigWithDirectoryCache(s, overrides, directoryCache), word, materializedBytes)
		restoreParameters()
		restore()
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
