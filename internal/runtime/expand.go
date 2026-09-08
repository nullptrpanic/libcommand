package runtime

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type substitutionRequest struct {
	state        *State
	substitution *syntax.CmdSubst
}

type processSubstitutionRequest struct {
	state        *State
	substitution *syntax.ProcSubst
}

func (request *processSubstitutionRequest) Error() string {
	return "process substitution requires evaluation"
}

func (request *substitutionRequest) Error() string {
	return "command substitution requires evaluation"
}

func requestedSubstitution(err error) (*substitutionRequest, bool) {
	var request *substitutionRequest
	if errors.As(err, &request) {
		return request, true
	}
	return nil, false
}

func requestedProcessSubstitution(err error) (*processSubstitutionRequest, bool) {
	var request *processSubstitutionRequest
	if errors.As(err, &request) {
		return request, true
	}
	return nil, false
}

func expansionRequested(err error) bool {
	if _, requested := requestedSubstitution(err); requested {
		return true
	}
	_, requested := requestedProcessSubstitution(err)
	return requested
}

func (e *ExecutionContext) expansionConfig(s *State) *expand.Config {
	return e.expansionConfigWithOverrides(s, nil)
}

func (e *ExecutionContext) expansionConfigWithOverrides(s *State, overrides *expansionOverrides) *expand.Config {
	return e.expansionConfigWithDirectoryCache(s, overrides, nil)
}

func (e *ExecutionContext) expansionConfigWithDirectoryCache(s *State, overrides *expansionOverrides, directoryCache map[string][]iofs.DirEntry) *expand.Config {
	config := &expand.Config{
		Env:        &stateEnvironment{state: s, overrides: overrides, maximum: e.config.MaxMemoryBytes},
		GlobStar:   s.options.globStar,
		DotGlob:    s.options.dotGlob,
		NoCaseGlob: s.options.noCaseGlob,
		NullGlob:   s.options.nullGlob,
		NoUnset:    s.options.noUnset,
		ExtGlob:    s.options.extGlob,
		CmdSubst: func(writer io.Writer, substitution *syntax.CmdSubst) error {
			result, exists := s.substitution(substitution)
			if !exists {
				return &substitutionRequest{state: s, substitution: substitution}
			}
			stdout, _ := result.stdout.Data()
			_, err := writer.Write(stdout)
			return err
		},
		ProcSubst: func(substitution *syntax.ProcSubst) (string, error) {
			if path, exists := s.processSubstitution(substitution); exists {
				return path, nil
			}
			return "", &processSubstitutionRequest{state: s, substitution: substitution}
		},
	}
	if !s.options.noGlob {
		config.ReadDir2 = func(name string) ([]iofs.DirEntry, error) {
			if err := e.ctx.Err(); err != nil {
				return nil, err
			}
			name = path.Clean(name)
			if entries, exists := directoryCache[name]; exists {
				return entries, nil
			}
			entries, err := s.fs.readDir(e.ctx, name)
			if err == nil && directoryCache != nil {
				directoryCache[name] = entries
			}
			return entries, err
		}
	}
	return config
}

func (e *ExecutionContext) expandWords(s *State, words []*syntax.Word) (fields []string, err error) {
	expansion, err := e.expandFields(s, words, false)
	if err != nil {
		return nil, err
	}
	return expansion.fields, nil
}

type expandedFields struct {
	fields      []string
	arguments   []*Argument
	hostUnknown bool
	unknownList bool
}

func (e *ExecutionContext) expandFields(s *State, words []*syntax.Word, allowHostUnknown bool) (expansion *expandedFields, err error) {
	originalVars := s.vars
	s.vars = s.vars.clone()
	defer func() {
		if err != nil {
			s.vars = originalVars
		}
	}()
	expansion = &expandedFields{}
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
				expansion.fields = append(expansion.fields, argument.Value)
			}
			expansion.arguments = append(expansion.arguments, positional...)
			continue
		}
		if err := normalizeWordArithmeticLiterals(word); err != nil {
			return nil, err
		}
		if err := validateParameterExpansions(s, word, false); err != nil {
			return nil, err
		}
		if err := e.checkBraceExpansion(word, materializedBytes); err != nil {
			return nil, err
		}
		dataUnknown := wordHasUnknownData(s, word)
		restore := noopRestore
		wordUnknown := false
		if word == nil || word.Lit() == "" {
			restore = maskInactiveParameterWords(s, word)
			wordUnknown = unmaskedWordCertainty(s, word, false).hostUnknown()
		}
		expanded, nextMaterializedBytes, expandErr := e.expandPreparedWordFields(
			s,
			word,
			materializedBytes,
			directoryCache,
			restore,
			noopRestore,
		)
		if expandErr != nil {
			return nil, expandErr
		}
		materializedBytes = nextMaterializedBytes
		if wordUnknown {
			if !allowHostUnknown {
				return nil, fmt.Errorf("word depends on host runtime state")
			}
			expansion.hostUnknown = true
			expansion.unknownList = true
			expansion.fields = append(expansion.fields, "")
			expansion.arguments = append(expansion.arguments, &Argument{Kind: ArgumentUnresolved})
		} else if dataUnknown {
			expansion.unknownList = true
			expansion.fields = append(expansion.fields, expanded...)
			expansion.arguments = append(expansion.arguments, &Argument{Kind: ArgumentUnresolved})
		} else {
			expansion.fields = append(expansion.fields, expanded...)
			expansion.arguments = append(expansion.arguments, concreteArguments(expanded)...)
		}
	}
	return expansion, nil
}

func (e *ExecutionContext) expandPreparedWordFields(
	s *State,
	word *syntax.Word,
	materializedBytes int,
	directoryCache map[string][]iofs.DirEntry,
	restoreWord func(),
	beforeArithmetic func(),
) ([]string, int, error) {
	defer restoreWord()
	overrides, restoreParameters, err := e.prepareParameterExpansions(s, word)
	if err != nil {
		return nil, materializedBytes, err
	}
	defer restoreParameters()
	beforeArithmetic()
	restoreArithmetic, err := e.prepareArithmeticExpansions(s, word)
	if err != nil {
		return nil, materializedBytes, err
	}
	defer restoreArithmetic()
	if err := e.checkWordMaterialization(s, word, materializedBytes); err != nil {
		return nil, materializedBytes, err
	}
	return e.expandFieldsWithinLimit(
		e.expansionConfigWithDirectoryCache(s, overrides, directoryCache),
		word,
		materializedBytes,
	)
}

func (e *ExecutionContext) applyAssignments(s *State, assignments []*syntax.Assign, declaredKind expand.ValueKind, exported bool) error {
	return e.applyPreparedAssignments(s, assignments, declaredKind, exported, nil)
}

func (e *ExecutionContext) applyPreparedAssignments(s *State, assignments []*syntax.Assign, declaredKind expand.ValueKind, exported bool, prepared *commandExpansions) (err error) {
	originalVars := s.vars
	s.vars = s.vars.clone()
	defer func() {
		if err != nil {
			s.vars = originalVars
		}
	}()
	stateBytes, ok := stateMaterialization(s)
	if !ok || stateBytes < s.vars.materializedBytes {
		return materialize.LimitError(normalizedMaxMemoryBytes(e.config.MaxMemoryBytes))
	}
	nonVariableBytes := stateBytes - s.vars.materializedBytes
	if prepared != nil {
		nonVariableBytes = addRetainedBytes(nonVariableBytes, prepared.bytes)
	}
	for index, assignment := range assignments {
		if index%256 == 0 {
			if err := e.ctx.Err(); err != nil {
				return err
			}
		}
		if assignment.Name == nil {
			continue
		}
		name := assignment.Name.Value
		if current := s.vars.Get(name); current.ReadOnly && !assignment.Naked {
			return fmt.Errorf("%s: readonly variable", name)
		}
		if assignment.Naked {
			current := s.vars.Get(name)
			if !current.Declared() {
				current = expand.Variable{Kind: declaredKind}
			}
			current.Exported = current.Exported || exported || s.options.allExport
			s.vars.putIndexedWithCertainty(name, current, s.vars.isUnknown(name), s.vars.indexedSlots(name))
			if err := e.checkStateMaterializationWithNonVariableBytes(s, nonVariableBytes); err != nil {
				return fmt.Errorf("apply assignment %s: %w", name, err)
			}
			continue
		}

		kind := declaredKind
		if assignment.Array != nil && kind == expand.Unknown {
			kind = expand.Indexed
		}
		value, indexedSlots, unknown, err := e.assignmentValue(s, assignment, kind, prepared)
		if err != nil {
			return fmt.Errorf("expand assignment %s: %w", name, err)
		}
		current := s.vars.Get(name)
		scalarArray := false
		if assignment.Array == nil && assignment.Index == nil {
			if arrayValue, arrayAssignment := scalarArrayAssignment(current, declaredKind, value, assignment.Append); arrayAssignment {
				value = arrayValue
				if value.Kind == expand.Indexed {
					indexedSlots = setIndexedSlot(s.vars.indexedSlots(name), len(current.List), 0)
				}
				unknown = unknown || s.vars.isUnknown(name)
				scalarArray = true
			}
		}
		value.Exported = value.Exported || exported || s.options.allExport || s.vars.Get(name).Exported
		if assignment.Append && assignment.Index == nil && !scalarArray {
			unknown = unknown || s.vars.isUnknown(name)
			addition := value
			value = appendVariable(current, value)
			if value.Kind == expand.Indexed {
				indexedSlots = appendIndexedSlots(current, addition, s.vars.indexedSlots(name), indexedSlots)
			}
		}
		if err := e.checkVariableMaterialization(&value); err != nil {
			return fmt.Errorf("expand assignment %s: %w", name, err)
		}
		s.vars.putIndexedWithCertainty(name, value, unknown, indexedSlots)
		if err := e.checkStateMaterializationWithNonVariableBytes(s, nonVariableBytes); err != nil {
			return fmt.Errorf("apply assignment %s: %w", name, err)
		}
	}
	return nil
}

func scalarArrayAssignment(current expand.Variable, declaredKind expand.ValueKind, value expand.Variable, appendMode bool) (expand.Variable, bool) {
	kind := current.Kind
	if kind != expand.Indexed && kind != expand.Associative {
		kind = declaredKind
	}
	if value.Kind != expand.String || kind != expand.Indexed && kind != expand.Associative {
		return expand.Variable{}, false
	}
	previousScalar := current.String()
	promotingScalar := current.Kind != expand.Indexed && current.Kind != expand.Associative
	current.Set = true
	current.Kind = kind
	current.Str = ""
	if kind == expand.Indexed {
		if len(current.List) == 0 {
			initial := ""
			if appendMode && promotingScalar {
				initial = previousScalar
			}
			current.List = []string{initial}
		}
		if appendMode {
			current.List[0] += value.Str
		} else {
			current.List[0] = value.Str
		}
		return current, true
	}
	if current.Map == nil {
		current.Map = make(map[string]string)
	}
	if appendMode && promotingScalar {
		current.Map["0"] = previousScalar
	}
	if appendMode {
		current.Map["0"] += value.Str
	} else {
		current.Map["0"] = value.Str
	}
	return current, true
}

func (e *ExecutionContext) assignmentValue(s *State, assignment *syntax.Assign, kind expand.ValueKind, prepared *commandExpansions) (expand.Variable, map[int]struct{}, bool, error) {
	if assignment.Array != nil {
		return prepared.array(e, s, assignment.Array, kind)
	}
	value := ""
	unknown := false
	if assignment.Value != nil {
		expanded, expandedUnknown, err := prepared.literal(e, s, assignment.Value)
		if err != nil {
			return expand.Variable{}, nil, false, err
		}
		value = expanded
		unknown = expandedUnknown
	}
	if assignment.Index != nil {
		if word, ok := assignment.Index.(*syntax.Word); ok && prepared != nil {
			if index := prepared.literals[word]; index != nil {
				value, unknown := index.Data()
				if unknown {
					return expand.Variable{}, nil, false, newUnknownValueError("array index")
				}
				copy := *assignment
				copy.Index = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
				assignment = &copy
			}
		}
		current := s.vars.Get(assignment.Name.Value)
		unknown = unknown || s.vars.isUnknown(assignment.Name.Value)
		if arithmHasUnknownData(s, assignment.Index) {
			return expand.Variable{}, nil, false, newUnknownValueError("array index")
		}
		if current.Kind == expand.Associative || kind == expand.Associative {
			if current.Map == nil {
				current.Map = make(map[string]string)
			}
			current.Kind = expand.Associative
			key, err := e.associativeIndex(s, assignment.Index)
			if err != nil {
				return expand.Variable{}, nil, false, err
			}
			if assignment.Append {
				current.Map[key] += value
			} else {
				current.Map[key] = value
			}
			current.Set = true
			return current, nil, unknown, nil
		}
		if current.List == nil {
			if current.Kind == expand.String && current.Set {
				current.List = []string{current.Str}
				current.Str = ""
			}
			current.Kind = expand.Indexed
		}
		if arithmHasHostUnknown(s, assignment.Index) {
			return expand.Variable{}, nil, false, fmt.Errorf("array index depends on host runtime state")
		}
		index, err := e.arithmeticValue(s, assignment.Index)
		if expansionRequested(err) {
			return expand.Variable{}, nil, false, err
		}
		if err != nil || index < 0 {
			return expand.Variable{}, nil, false, fmt.Errorf("invalid array index")
		}
		previousLength := len(current.List)
		current.List, err = extendIndexedArray(current.List, index, normalizedMaxMemoryBytes(e.config.MaxMemoryBytes))
		if err != nil {
			return expand.Variable{}, nil, false, err
		}
		if assignment.Append {
			current.List[index] += value
		} else {
			current.List[index] = value
		}
		current.Set = true
		slots := setIndexedSlot(s.vars.indexedSlots(assignment.Name.Value), previousLength, index)
		return current, slots, unknown, nil
	}
	return expand.Variable{Set: true, Kind: expand.String, Str: value}, nil, unknown, nil
}

func (e *ExecutionContext) arrayValue(s *State, array *syntax.ArrayExpr, kind expand.ValueKind) (expand.Variable, map[int]struct{}, bool, error) {
	unknown := false
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	if kind == expand.Associative {
		result := expand.Variable{Set: true, Kind: expand.Associative, Map: make(map[string]string)}
		materializedBytes := 0
		for _, element := range array.Elems {
			if err := e.ctx.Err(); err != nil {
				return expand.Variable{}, nil, false, err
			}
			if element.Index == nil {
				return expand.Variable{}, nil, false, errors.New("associative array element requires an explicit index")
			}
			if arithmHasUnknownData(s, element.Index) {
				return expand.Variable{}, nil, false, newUnknownValueError("associative array index")
			}
			key, err := e.associativeIndex(s, element.Index)
			if err != nil {
				return expand.Variable{}, nil, false, err
			}
			value, valueUnknown, err := e.literalValueWithCertainty(s, element.Value)
			if err != nil {
				return expand.Variable{}, nil, false, err
			}
			unknown = unknown || valueUnknown
			previous, exists := result.Map[key]
			nextMaterializedBytes := materializedBytes
			if exists {
				nextMaterializedBytes -= len(previous)
			} else {
				var ok bool
				nextMaterializedBytes, ok = materialize.Add(nextMaterializedBytes, materialize.EntryBytes+len(key), maximum)
				if !ok {
					return expand.Variable{}, nil, false, materialize.LimitError(maximum)
				}
			}
			var ok bool
			nextMaterializedBytes, ok = materialize.Add(nextMaterializedBytes, len(value), maximum)
			if !ok {
				return expand.Variable{}, nil, false, materialize.LimitError(maximum)
			}
			result.Map[key] = value
			materializedBytes = nextMaterializedBytes
		}
		return result, nil, unknown, nil
	}

	result := expand.Variable{Set: true, Kind: expand.Indexed}
	present := make(map[int]struct{}, len(array.Elems))
	materializedBytes := 0
	nextIndex := 0
	for _, element := range array.Elems {
		if err := e.ctx.Err(); err != nil {
			return expand.Variable{}, nil, false, err
		}
		index := nextIndex
		if element.Index != nil {
			if arithmHasUnknownData(s, element.Index) {
				return expand.Variable{}, nil, false, newUnknownValueError("indexed array position")
			}
			if arithmHasHostUnknown(s, element.Index) {
				return expand.Variable{}, nil, false, fmt.Errorf("array index depends on host runtime state")
			}
			value, err := e.arithmeticValue(s, element.Index)
			if expansionRequested(err) {
				return expand.Variable{}, nil, false, err
			}
			if err != nil || value < 0 {
				return expand.Variable{}, nil, false, fmt.Errorf("invalid indexed array position")
			}
			index = value
		}
		value, valueUnknown, err := e.literalValueWithCertainty(s, element.Value)
		if err != nil {
			return expand.Variable{}, nil, false, err
		}
		unknown = unknown || valueUnknown
		if index >= len(result.List) {
			if index+1 <= index {
				return expand.Variable{}, nil, false, fmt.Errorf("invalid array index")
			}
			addedSlots := index + 1 - len(result.List)
			addedBytes, ok := multiplyMaterialization(addedSlots, materialize.EntryBytes, maximum)
			if ok {
				materializedBytes, ok = materialize.Add(materializedBytes, addedBytes, maximum)
			}
			if !ok {
				return expand.Variable{}, nil, false, materialize.LimitError(maximum)
			}
		}
		result.List, err = extendIndexedArray(result.List, index, maximum)
		if err != nil {
			return expand.Variable{}, nil, false, err
		}
		materializedBytes -= len(result.List[index])
		var ok bool
		materializedBytes, ok = materialize.Add(materializedBytes, len(value), maximum)
		if !ok {
			return expand.Variable{}, nil, false, materialize.LimitError(maximum)
		}
		result.List[index] = value
		present[index] = struct{}{}
		nextIndex = index + 1
	}
	return result, normalizeIndexedSlots(present, len(result.List)), unknown, nil
}

func setIndexedSlot(slots map[int]struct{}, previousLength, index int) map[int]struct{} {
	if slots == nil && index <= previousLength {
		return nil
	}
	if slots == nil {
		slots = make(map[int]struct{}, previousLength+1)
		for current := range previousLength {
			slots[current] = struct{}{}
		}
	}
	slots[index] = struct{}{}
	return normalizeIndexedSlots(slots, max(previousLength, index+1))
}

func appendIndexedSlots(current, addition expand.Variable, currentSlots, additionSlots map[int]struct{}) map[int]struct{} {
	if current.Kind != expand.Indexed || addition.Kind != expand.Indexed || currentSlots == nil && additionSlots == nil {
		return nil
	}
	result := make(map[int]struct{}, len(current.List)+len(addition.List))
	if currentSlots == nil {
		for index := range len(current.List) {
			result[index] = struct{}{}
		}
	} else {
		for index := range currentSlots {
			result[index] = struct{}{}
		}
	}
	if additionSlots == nil {
		for index := range len(addition.List) {
			result[len(current.List)+index] = struct{}{}
		}
	} else {
		for index := range additionSlots {
			result[len(current.List)+index] = struct{}{}
		}
	}
	return normalizeIndexedSlots(result, len(current.List)+len(addition.List))
}

func extendIndexedArray(values []string, index, maximum int) ([]string, error) {
	if len(values) > index {
		return values, nil
	}
	if index+1 <= index {
		return nil, fmt.Errorf("invalid array index")
	}
	if index+1 > maximum/materialize.EntryBytes {
		return nil, materialize.LimitError(maximum)
	}
	return append(values, make([]string, index+1-len(values))...), nil
}

func (e *ExecutionContext) literalValue(s *State, word *syntax.Word) (value string, err error) {
	if word == nil {
		return "", nil
	}
	hostUnknown := literalWordCertainty(s, word).hostUnknown()
	value, err = e.expandWordValue(s, word, expand.Literal)
	if err == nil && hostUnknown {
		err = fmt.Errorf("word depends on host runtime state")
	}
	return value, err
}

func (e *ExecutionContext) patternValue(s *State, word *syntax.Word) (value string, err error) {
	return e.expandWordValue(s, word, expand.Pattern)
}

func (e *ExecutionContext) expandWordValue(s *State, word *syntax.Word, expandValue func(*expand.Config, *syntax.Word) (string, error)) (value string, err error) {
	if err := normalizeWordArithmeticLiterals(word); err != nil {
		return "", err
	}
	if err := validateParameterExpansions(s, word, false); err != nil {
		return "", err
	}
	restore := maskInactiveParameterWords(s, word)
	defer restore()
	originalVars := s.vars
	s.vars = s.vars.clone()
	defer func() {
		if err != nil {
			s.vars = originalVars
		}
	}()
	overrides, restoreParameters, prepareErr := e.prepareParameterExpansions(s, word)
	if prepareErr != nil {
		return "", prepareErr
	}
	defer restoreParameters()
	restoreArithmetic, arithmeticErr := e.prepareArithmeticExpansions(s, word)
	if arithmeticErr != nil {
		return "", arithmeticErr
	}
	defer restoreArithmetic()
	if err := e.checkWordMaterialization(s, word, 0); err != nil {
		return "", err
	}
	value, err = expandValue(e.expansionConfigWithOverrides(s, overrides), word)
	return value, err
}

type parameterExpansionRewrite struct {
	parameter *syntax.ParamExp
	name      string
	excl      bool
	index     syntax.ArithmExpr
	slice     *syntax.Slice
}

func (e *ExecutionContext) prepareParameterExpansions(s *State, node syntax.Node) (*expansionOverrides, func(), error) {
	if node == nil {
		return nil, noopRestore, nil
	}
	overrides := &expansionOverrides{
		values:     make(map[string]expand.Variable),
		references: make(map[string]*collectionReference),
	}
	rewrites := make([]*parameterExpansionRewrite, 0)
	var prepareErr error
	rewriteValue := func(parameter *syntax.ParamExp, name string, value expand.Variable, reference *collectionReference) {
		overrideName := fmt.Sprintf("\x00libcommand_parameter_%d", len(rewrites))
		overrides.values[overrideName] = value
		if reference != nil {
			overrides.references[overrideName] = reference
		}
		rewrites = append(rewrites, &parameterExpansionRewrite{
			parameter: parameter,
			name:      name,
			excl:      parameter.Excl,
			index:     parameter.Index,
			slice:     parameter.Slice,
		})
		parameter.Param.Value = overrideName
		parameter.Index = nil
		parameter.Slice = nil
	}
	syntax.Walk(node, func(current syntax.Node) bool {
		if prepareErr != nil {
			return false
		}
		if _, substitution := current.(*syntax.CmdSubst); substitution {
			return false
		}
		parameter, ok := current.(*syntax.ParamExp)
		if !ok || parameter.Param == nil {
			return true
		}
		name := parameter.Param.Value
		variable := (&stateEnvironment{state: s}).Get(name)
		if parameter.Index == nil && parameter.Slice == nil && !parameter.Excl && variable.Kind == expand.Indexed && s.vars.indexedSlots(name) != nil {
			reference := &collectionReference{name: name}
			rewriteValue(parameter, name, collectionElement(s, reference), reference)
			return true
		}
		indexWord, indexed := parameter.Index.(*syntax.Word)
		if !indexed {
			if parameter.Index != nil || parameter.Slice == nil || parameter.Excl || name == "@" || name == "*" || variable.Kind == expand.Associative {
				return true
			}
			value, sliceErr := e.sliceScalarValue(s, variable, parameter.Slice)
			prepareErr = sliceErr
			if prepareErr == nil {
				rewriteValue(parameter, name, value, nil)
			}
			return prepareErr == nil
		}
		indexLiteral := indexWord.Lit()
		if indexLiteral != "@" && indexLiteral != "*" {
			if parameter.Excl {
				return true
			}
			reference, referenceErr := e.collectionReference(s, name, parameter.Index)
			prepareErr = referenceErr
			if prepareErr != nil {
				return false
			}
			value := collectionElement(s, reference)
			if parameter.Slice != nil {
				value, prepareErr = e.sliceScalarValue(s, value, parameter.Slice)
				if prepareErr != nil {
					return false
				}
			}
			rewriteValue(parameter, name, value, reference)
			return true
		}
		var values []string
		preSliced := false
		switch variable.Kind {
		case expand.Indexed:
			slots := s.vars.indexedSlots(name)
			if parameter.Excl {
				if slots == nil {
					values = indexedExpansionKeys(nil, len(variable.List))
				} else {
					indices, sliceErr := e.sparseIndexedExpansionIndices(s, slots, parameter.Slice)
					prepareErr = sliceErr
					if prepareErr != nil {
						return false
					}
					values = make([]string, len(indices))
					for index, slot := range indices {
						values[index] = strconv.Itoa(slot)
					}
					preSliced = parameter.Slice != nil
				}
			} else if slots != nil {
				indices, sliceErr := e.sparseIndexedExpansionIndices(s, slots, parameter.Slice)
				prepareErr = sliceErr
				if prepareErr != nil {
					return false
				}
				values = make([]string, len(indices))
				for index, slot := range indices {
					values[index] = variable.List[slot]
				}
				preSliced = parameter.Slice != nil
			} else {
				return true
			}
		case expand.Associative:
			keys := make([]string, 0, len(variable.Map))
			for key := range variable.Map {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if parameter.Excl {
				values = keys
			} else {
				values = make([]string, len(keys))
				for index, key := range keys {
					values[index] = variable.Map[key]
				}
			}
		default:
			if !parameter.Excl {
				return true
			}
			if _, exists := s.vars.lookup(name); !exists {
				return true
			}
			if variable.IsSet() {
				values = []string{"0"}
			}
		}
		overrideName := fmt.Sprintf("\x00libcommand_parameter_%d", len(rewrites))
		overrides.values[overrideName] = expand.Variable{Set: true, Kind: expand.Indexed, List: values}
		rewrites = append(rewrites, &parameterExpansionRewrite{
			parameter: parameter,
			name:      name,
			excl:      parameter.Excl,
			index:     parameter.Index,
			slice:     parameter.Slice,
		})
		parameter.Param.Value = overrideName
		parameter.Excl = false
		if preSliced {
			parameter.Slice = nil
		}
		return true
	})
	restore := func() {
		for index := len(rewrites) - 1; index >= 0; index-- {
			rewrite := rewrites[index]
			rewrite.parameter.Param.Value = rewrite.name
			rewrite.parameter.Excl = rewrite.excl
			rewrite.parameter.Index = rewrite.index
			rewrite.parameter.Slice = rewrite.slice
		}
	}
	if prepareErr != nil {
		restore()
		return nil, noopRestore, prepareErr
	}
	if len(rewrites) == 0 {
		return nil, noopRestore, nil
	}
	return overrides, restore, nil
}

func (e *ExecutionContext) sliceScalarValue(s *State, value expand.Variable, slice *syntax.Slice) (expand.Variable, error) {
	offset := 0
	var err error
	if slice.Offset != nil {
		offset, err = e.arithmeticValue(s, slice.Offset)
		if err != nil {
			return expand.Variable{}, err
		}
	}
	text := value.String()
	characterCount := utf8.RuneCountInString(text)
	start := offset
	if start < 0 {
		if start < -characterCount {
			start = characterCount
		} else {
			start += characterCount
		}
	} else if start > characterCount {
		start = characterCount
	}

	end := characterCount
	if slice.Length != nil {
		length, lengthErr := e.arithmeticValue(s, slice.Length)
		if lengthErr != nil {
			return expand.Variable{}, lengthErr
		}
		if length < 0 {
			if length < -characterCount || characterCount+length < start {
				return expand.Variable{}, fmt.Errorf("substring expression < 0")
			}
			end = characterCount + length
		} else if length < characterCount-start {
			end = start + length
		}
	}

	value.Kind = expand.String
	value.Str = strings.Clone(text[runeByteOffset(text, start):runeByteOffset(text, end)])
	value.List = nil
	value.Map = nil
	return value, nil
}

func runeByteOffset(value string, runeOffset int) int {
	if runeOffset <= 0 {
		return 0
	}
	current := 0
	for byteOffset := range value {
		if current == runeOffset {
			return byteOffset
		}
		current++
	}
	return len(value)
}

func (e *ExecutionContext) collectionReference(s *State, name string, index syntax.ArithmExpr) (*collectionReference, error) {
	reference := &collectionReference{name: name}
	if s.vars.Get(name).Kind == expand.Associative {
		reference.associative = true
		key, err := e.associativeIndex(s, index)
		if err != nil {
			return nil, err
		}
		reference.key = key
		return reference, nil
	}
	if arithmHasUnknownData(s, index) {
		return nil, newUnknownValueError("array index")
	}
	if arithmHasHostUnknown(s, index) {
		return nil, fmt.Errorf("array index depends on host runtime state")
	}
	value, err := e.arithmeticValue(s, index)
	if err != nil {
		return nil, err
	}
	if value < 0 {
		return nil, fmt.Errorf("invalid array index")
	}
	reference.index = value
	return reference, nil
}

func indexedExpansionKeys(slots map[int]struct{}, length int) []string {
	if slots == nil {
		keys := make([]string, length)
		for index := range length {
			keys[index] = strconv.Itoa(index)
		}
		return keys
	}
	indices := sortedIndexedSlots(slots)
	keys := make([]string, len(indices))
	for index, value := range indices {
		keys[index] = strconv.Itoa(value)
	}
	return keys
}

func (e *ExecutionContext) sparseIndexedExpansionIndices(s *State, slots map[int]struct{}, slice *syntax.Slice) ([]int, error) {
	indices := sortedIndexedSlots(slots)
	if slice == nil || len(indices) == 0 {
		return indices, nil
	}
	offset := 0
	if slice.Offset != nil {
		value, err := e.arithmeticValue(s, slice.Offset)
		if err != nil {
			return nil, err
		}
		offset = value
	}
	if offset < 0 {
		offset += indices[len(indices)-1] + 1
		if offset < 0 {
			offset = 0
		}
	}
	start := sort.SearchInts(indices, offset)
	end := len(indices)
	if slice.Length != nil {
		length, err := e.arithmeticValue(s, slice.Length)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, fmt.Errorf("substring expression < 0")
		}
		if length < end-start {
			end = start + length
		}
	}
	return indices[start:end], nil
}

func sortedIndexedSlots(slots map[int]struct{}) []int {
	indices := make([]int, 0, len(slots))
	for index := range slots {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func maskInactiveParameterWords(s *State, nodes ...syntax.Node) func() {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		word, literalWord := node.(*syntax.Word)
		if !literalWord || word.Lit() == "" {
			return maskInactiveParameterWordsSlow(s, nodes...)
		}
	}
	return noopRestore
}

func maskInactiveParameterWordsSlow(s *State, nodes ...syntax.Node) func() {
	type maskedWord struct {
		expansion *syntax.Expansion
		word      *syntax.Word
	}
	masked := make([]maskedWord, 0)
	for _, node := range nodes {
		syntax.Walk(node, func(current syntax.Node) bool {
			switch current := current.(type) {
			case *syntax.CmdSubst:
				return false
			case *syntax.ParamExp:
				if current.Exp == nil || current.Exp.Word == nil || parameterExpansionWordNeeded(s, current) {
					return true
				}
				masked = append(masked, maskedWord{expansion: current.Exp, word: current.Exp.Word})
				current.Exp.Word = &syntax.Word{}
			}
			return true
		})
	}
	if len(masked) == 0 {
		return noopRestore
	}
	return func() {
		for index := len(masked) - 1; index >= 0; index-- {
			masked[index].expansion.Word = masked[index].word
		}
	}
}

func noopRestore() {}

func parameterExpansionWordNeeded(s *State, parameter *syntax.ParamExp) bool {
	if parameter.Param == nil || parameter.Index != nil {
		return true
	}
	variable := s.vars.Get(parameter.Param.Value)
	if slots := s.vars.indexedSlots(parameter.Param.Value); variable.Kind == expand.Indexed && slots != nil {
		variable = collectionElement(s, &collectionReference{name: parameter.Param.Value})
	}
	if name, resolved := variable.Resolve(&stateEnvironment{state: s}); name != "" {
		variable = resolved
	}
	set := variable.IsSet()
	nonempty := set && variable.String() != ""
	switch parameter.Exp.Op {
	case syntax.AlternateUnset:
		return set
	case syntax.AlternateUnsetOrNull:
		return nonempty
	case syntax.DefaultUnset, syntax.ErrorUnset, syntax.AssignUnset:
		return !set
	case syntax.DefaultUnsetOrNull, syntax.ErrorUnsetOrNull, syntax.AssignUnsetOrNull:
		return !nonempty
	default:
		return true
	}
}

func (e *ExecutionContext) associativeIndex(s *State, expression syntax.ArithmExpr) (string, error) {
	word, ok := expression.(*syntax.Word)
	if !ok {
		if arithmHasHostUnknown(s, expression) {
			return "", fmt.Errorf("associative index depends on host runtime state")
		}
		value, err := e.arithmeticValue(s, expression)
		return strconv.Itoa(value), err
	}
	if literal := word.Lit(); literal != "" {
		return literal, nil
	}
	return e.literalValue(s, word)
}

func appendVariable(current, addition expand.Variable) expand.Variable {
	if !current.IsSet() {
		return addition
	}
	switch addition.Kind {
	case expand.String:
		current.Set = true
		current.Kind = expand.String
		current.Str += addition.Str
	case expand.Indexed:
		if current.Kind == expand.String {
			current.List = []string{current.Str}
			current.Str = ""
		}
		current.Set = true
		current.Kind = expand.Indexed
		current.List = append(current.List, addition.List...)
	case expand.Associative:
		if current.Map == nil {
			current.Map = make(map[string]string)
		}
		for key, value := range addition.Map {
			current.Map[key] = value
		}
		current.Set = true
		current.Kind = expand.Associative
	}
	current.Exported = current.Exported || addition.Exported
	return current
}
