package runtime

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

func (e *ExecutionContext) addMaterializedString(total int, value string) (int, error) {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	var ok bool
	total, ok = materialize.Add(total, materialize.EntryBytes, maximum)
	if ok {
		total, ok = materialize.Add(total, len(value), maximum)
	}
	if !ok {
		return 0, materialize.LimitError(maximum)
	}
	return total, nil
}

func (e *ExecutionContext) expandFieldsWithinLimit(config *expand.Config, word *syntax.Word, total int) (fields []string, next int, err error) {
	next = total
	defer func() {
		recovered := recover()
		message, patternPanic := recovered.(string)
		if recovered == nil {
			return
		}
		if !patternPanic || !strings.HasPrefix(message, "regexp: Compile(") {
			panic(recovered)
		}
		fields = nil
		next = total
		err = errors.New("invalid pathname expansion pattern")
	}()
	for field, expandErr := range expand.FieldsSeq(config, word) {
		if expandErr != nil {
			return nil, next, expandErr
		}
		if len(fields)%256 == 0 {
			if err := e.ctx.Err(); err != nil {
				return nil, next, err
			}
		}
		var addErr error
		next, addErr = e.addMaterializedString(next, field)
		if addErr != nil {
			return nil, next, addErr
		}
		fields = append(fields, field)
	}
	return fields, next, nil
}

func (e *ExecutionContext) checkWordMaterialization(s *State, word *syntax.Word, total int) error {
	if !wordNeedsMaterializationPreflight(word) {
		return nil
	}
	probe := s.clone()
	_, err := e.checkWordPartsMaterialization(probe, word.Parts, &total)
	return err
}

func wordNeedsMaterializationPreflight(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	return wordPartsNeedMaterializationPreflight(word.Parts)
}

func wordPartsNeedMaterializationPreflight(parts []syntax.WordPart) bool {
	if len(parts) > 1 {
		return true
	}
	if len(parts) == 0 {
		return false
	}
	switch part := parts[0].(type) {
	case *syntax.DblQuoted:
		return wordPartsNeedMaterializationPreflight(part.Parts)
	case *syntax.ParamExp:
		return part.Repl != nil || part.Exp != nil && wordNeedsMaterializationPreflight(part.Exp.Word)
	default:
		return false
	}
}

func (e *ExecutionContext) checkWordPartsMaterialization(s *State, parts []syntax.WordPart, total *int) (bool, error) {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	for index, part := range parts {
		if index%256 == 0 {
			if err := e.ctx.Err(); err != nil {
				return false, err
			}
		}
		if quoted, ok := part.(*syntax.DblQuoted); ok {
			complete, err := e.checkWordPartsMaterialization(s, quoted.Parts, total)
			if err != nil || !complete {
				return complete, err
			}
			continue
		}

		partBytes, complete, err := e.wordPartMaterialization(s, part)
		if err != nil || !complete {
			return complete, err
		}
		next, ok := materialize.Add(*total, partBytes, maximum)
		if !ok {
			return false, materialize.LimitError(maximum)
		}
		*total = next
	}
	return true, nil
}

func (e *ExecutionContext) wordPartMaterialization(s *State, part syntax.WordPart) (int, bool, error) {
	if parameter, ok := part.(*syntax.ParamExp); ok {
		if parameter.Repl != nil {
			return e.parameterReplacementMaterialization(s, parameter)
		}
		if parameter.Exp != nil && parameter.Exp.Word != nil && parameterExpansionWordNeeded(s, parameter) {
			operand := s.clone()
			total := 0
			complete, err := e.checkWordPartsMaterialization(operand, parameter.Exp.Word.Parts, &total)
			if err != nil || !complete {
				return 0, complete, err
			}
		}
	}

	word := &syntax.Word{Parts: []syntax.WordPart{part}}
	value, err := expand.Literal(e.expansionConfig(s), word)
	if err != nil {
		if contextErr := e.ctx.Err(); contextErr != nil {
			return 0, false, contextErr
		}
		return 0, false, nil
	}
	return len(value), true, nil
}

func (e *ExecutionContext) parameterReplacementMaterialization(s *State, parameter *syntax.ParamExp) (int, bool, error) {
	baseParameter := *parameter
	baseParameter.Repl = nil
	baseWord := &syntax.Word{Parts: []syntax.WordPart{&baseParameter}}
	value, err := expand.Literal(e.expansionConfig(s), baseWord)
	if err != nil {
		if contextErr := e.ctx.Err(); contextErr != nil {
			return 0, false, contextErr
		}
		return 0, false, nil
	}
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	if len(value) > maximum {
		return 0, false, materialize.LimitError(maximum)
	}

	original, complete, err := e.expandProbeWord(s, parameter.Repl.Orig, expand.Pattern)
	if err != nil || !complete {
		return 0, complete, err
	}
	if original == "" {
		return len(value), true, nil
	}
	replacement, complete, err := e.expandProbeWord(s, parameter.Repl.With, expand.Literal)
	if err != nil || !complete {
		return 0, complete, err
	}
	return e.replacementMaterialization(value, original, replacement, parameter.Repl.All)
}

func (e *ExecutionContext) expandProbeWord(s *State, word *syntax.Word, expandValue func(*expand.Config, *syntax.Word) (string, error)) (string, bool, error) {
	probe := s.clone()
	total := 0
	complete, err := e.checkWordPartsMaterialization(probe, wordParts(word), &total)
	if err != nil || !complete {
		return "", complete, err
	}
	value, err := expandValue(e.expansionConfig(s), word)
	if err != nil {
		if contextErr := e.ctx.Err(); contextErr != nil {
			return "", false, contextErr
		}
		return "", false, nil
	}
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	if len(value) > maximum {
		return "", false, materialize.LimitError(maximum)
	}
	return value, true, nil
}

func wordParts(word *syntax.Word) []syntax.WordPart {
	if word == nil {
		return nil
	}
	return word.Parts
}

func (e *ExecutionContext) replacementMaterialization(value, original, replacement string, all bool) (int, bool, error) {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	expression, err := pattern.Regexp(original, 0)
	if err != nil {
		return len(value), true, nil
	}
	regularExpression, err := regexp.Compile(expression)
	if err != nil {
		return len(value), true, nil
	}
	resultBytes := len(value)
	position := 0
	previousMatchEnd := -1
	matches := 0
	matchBytes := 0
	for position <= len(value) {
		if matches%256 == 0 {
			if err := e.ctx.Err(); err != nil {
				return 0, false, err
			}
		}
		location := regularExpression.FindStringIndex(value[position:])
		if location == nil {
			break
		}
		start := position + location[0]
		end := position + location[1]
		accept := true
		if end == position {
			if start == previousMatchEnd {
				accept = false
			}
			_, width := utf8.DecodeRuneInString(value[position:])
			if width > 0 {
				position += width
			} else {
				position = len(value) + 1
			}
		} else {
			position = end
		}
		previousMatchEnd = end
		if !accept {
			continue
		}
		matches++
		var ok bool
		matchBytes, ok = materialize.Add(matchBytes, materialize.EntryBytes, maximum)
		if !ok {
			return 0, false, materialize.LimitError(maximum)
		}
		resultBytes, ok = materialize.Add(resultBytes-(end-start), len(replacement), maximum)
		if ok {
			_, ok = materialize.Add(resultBytes, matchBytes, maximum)
		}
		if !ok {
			return 0, false, materialize.LimitError(maximum)
		}
		if !all {
			break
		}
	}
	return resultBytes, true, nil
}

func (e *ExecutionContext) checkVariableMaterialization(value *expand.Variable) error {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	total := 0
	var ok bool
	switch value.Kind {
	case expand.Indexed:
		if len(value.List) > maximum/materialize.EntryBytes {
			return materialize.LimitError(maximum)
		}
		total = len(value.List) * materialize.EntryBytes
		for _, item := range value.List {
			total, ok = materialize.Add(total, len(item), maximum)
			if !ok {
				return materialize.LimitError(maximum)
			}
		}
	case expand.Associative:
		for key, item := range value.Map {
			for _, size := range []int{materialize.EntryBytes, len(key), len(item)} {
				total, ok = materialize.Add(total, size, maximum)
				if !ok {
					return materialize.LimitError(maximum)
				}
			}
		}
	default:
		if _, ok := materialize.Add(0, len(value.Str), maximum); !ok {
			return materialize.LimitError(maximum)
		}
	}
	return nil
}

func stateMaterialization(s *State) (int, bool) {
	if s == nil {
		return 0, true
	}
	total := 0
	add := func(size int) bool {
		var ok bool
		total, ok = materialize.Add(total, size, math.MaxInt)
		return ok
	}
	if s.vars != nil && !add(s.vars.materializedBytes) {
		return 0, false
	}
	if s.fs != nil && !add(s.fs.materializedBytes) {
		return 0, false
	}
	directory, _ := s.dir.Data()
	stdin, _ := s.stdin.Data()
	stdout, _ := s.stdout.Data()
	stderr, _ := s.stderr.Data()
	for _, size := range []int{len(directory), len(stdin), len(stdout), len(stderr)} {
		if !add(size) {
			return 0, false
		}
	}
	pipelineStatuses, _ := s.pipelineStatuses.Data()
	if len(pipelineStatuses) != 0 {
		pipelineBytes, ok := multiplyMaterialization(len(pipelineStatuses), materialize.EntryBytes, math.MaxInt)
		if !ok || !add(pipelineBytes) {
			return 0, false
		}
		for _, status := range pipelineStatuses {
			if !add(len(status)) {
				return 0, false
			}
		}
	}
	if !add(s.functionsBytes) {
		return 0, false
	}
	if !add(s.localScopesBytes) {
		return 0, false
	}
	if !add(s.substitutionBytes) {
		return 0, false
	}
	if !add(s.trapsBytes) {
		return 0, false
	}
	for name, values := range s.commandStates {
		if !add(materialize.EntryBytes + len(name)) {
			return 0, false
		}
		bytes, ok := multiplyMaterialization(len(values), materialize.EntryBytes, math.MaxInt)
		if !ok || !add(bytes) {
			return 0, false
		}
	}
	loopBytes, ok := multiplyMaterialization(len(s.loopExitStatuses), 2*materialize.EntryBytes, math.MaxInt)
	if !ok || !add(loopBytes) {
		return 0, false
	}
	if s.issue != nil && !add(len(s.issue.Error())) {
		return 0, false
	}
	if s.exitFailure != nil {
		failureBytes, ok := stateMaterialization(s.exitFailure)
		if !ok || !add(materialize.EntryBytes+failureBytes) {
			return 0, false
		}
	}
	return total, true
}

func (e *ExecutionContext) traceMemory(s *State, budget *retainedPathBudget) *TraceMemory {
	aggregate, retainedPaths, auxiliary := e.traceAggregateMemory(budget)
	return e.traceMemoryWithValues(s, aggregate, retainedPaths, auxiliary)
}

func (e *ExecutionContext) traceMemoryWithAggregate(s *State, aggregate, retainedPaths int) *TraceMemory {
	auxiliary, _ := e.retainedAuxiliaryBytes(math.MaxInt)
	return e.traceMemoryWithValues(s, aggregate, retainedPaths, auxiliary)
}

func (e *ExecutionContext) traceMemoryWithValues(s *State, aggregate, retainedPaths, auxiliary int) *TraceMemory {
	stateBytes, _ := stateMaterialization(s)
	variablesBytes := 0
	virtualFileBytes := 0
	streamBytes := 0
	functionBytes := 0
	substitutionBytes := 0
	if s != nil {
		if s.vars != nil {
			variablesBytes = s.vars.materializedBytes
		}
		if s.fs != nil {
			virtualFileBytes = s.fs.materializedBytes
		}
		if s.stdin != nil {
			input, _ := s.stdin.Data()
			streamBytes += len(input)
		}
		if s.stdout != nil {
			output, _ := s.stdout.Data()
			streamBytes += len(output)
		}
		if s.stderr != nil {
			output, _ := s.stderr.Data()
			streamBytes += len(output)
		}
		functionBytes = s.functionsBytes
		substitutionBytes = s.substitutionBytes
	}
	otherBytes := stateBytes - variablesBytes - virtualFileBytes - streamBytes - functionBytes - substitutionBytes
	if otherBytes < 0 {
		otherBytes = 0
	}
	return &TraceMemory{
		StateBytes:        stateBytes,
		AggregateBytes:    aggregate,
		VariablesBytes:    variablesBytes,
		VirtualFileBytes:  virtualFileBytes,
		StreamBytes:       streamBytes,
		FunctionBytes:     functionBytes,
		SubstitutionBytes: substitutionBytes,
		OtherBytes:        otherBytes,
		AuxiliaryBytes:    auxiliary,
		RetainedPaths:     retainedPaths,
		MaximumBytes:      normalizedMaxMemoryBytes(e.config.MaxMemoryBytes),
	}
}

func (e *ExecutionContext) traceAggregateMemory(budget *retainedPathBudget) (total, retainedPaths, auxiliary int) {
	if budget == nil {
		return 0, 0, 0
	}
	auxiliary, _ = e.retainedAuxiliaryBytes(math.MaxInt)
	total = auxiliary
	pathBytes, ok := multiplyMaterialization(budget.pathCount, materialize.EntryBytes, math.MaxInt)
	if !ok {
		return math.MaxInt, budget.pathCount, auxiliary
	}
	total, ok = materialize.Add(total, pathBytes, math.MaxInt)
	if !ok {
		return math.MaxInt, budget.pathCount, auxiliary
	}
	stateBytes := budget.stateBytes
	if stateBytes > budget.baseline {
		stateBytes -= budget.baseline
	} else {
		stateBytes = 0
	}
	total, ok = materialize.Add(total, stateBytes, math.MaxInt)
	if !ok {
		total = math.MaxInt
	}
	return total, budget.pathCount, auxiliary
}

func (e *ExecutionContext) checkStateMaterialization(s *State) error {
	stateBytes, ok := stateMaterialization(s)
	return e.checkStateMaterializationBytes(s, stateBytes, ok)
}

func (e *ExecutionContext) checkStateMaterializationWithNonVariableBytes(s *State, nonVariableBytes int) error {
	stateBytes, ok := materialize.Add(nonVariableBytes, s.vars.materializedBytes, math.MaxInt)
	return e.checkStateMaterializationBytes(s, stateBytes, ok)
}

func (e *ExecutionContext) checkStateMaterializationBytes(s *State, stateBytes int, ok bool) error {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	total, auxiliaryOK := e.retainedAuxiliaryBytes(maximum)
	if auxiliaryOK {
		total, auxiliaryOK = materialize.Add(total, materialize.EntryBytes, maximum)
	}
	if !auxiliaryOK {
		return materialize.LimitError(maximum)
	}
	if !ok {
		return materialize.LimitError(maximum)
	}
	if stateBytes > s.initialBytes {
		stateBytes -= s.initialBytes
	} else {
		stateBytes = 0
	}
	if _, ok := materialize.Add(total, stateBytes, maximum); !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}

func (e *ExecutionContext) checkPathsMaterialization(paths []*pathResult, additional int, source *location) error {
	return e.checkPathGroupsMaterialization(additional, source, paths)
}

type retainedPathBudget struct {
	stateBytes  int
	pathCount   int
	baseline    int
	baselineSet bool
}

func (e *ExecutionContext) newRetainedPathBudget(groups ...[]*pathResult) (*retainedPathBudget, error) {
	budget := &retainedPathBudget{}
	index := 0
	for _, group := range groups {
		for _, path := range group {
			if index%256 == 0 {
				if err := e.ctx.Err(); err != nil {
					return nil, err
				}
			}
			index++
			stateBytes, retained, ok := retainedPathStateBytes(path)
			if !ok {
				return nil, materialize.LimitError(normalizedMaxMemoryBytes(e.config.MaxMemoryBytes))
			}
			if !retained {
				continue
			}
			if !budget.baselineSet {
				budget.baseline = path.state.initialBytes
				budget.baselineSet = true
			}
			budget.stateBytes, ok = materialize.Add(budget.stateBytes, stateBytes, math.MaxInt)
			if !ok || budget.pathCount == math.MaxInt {
				return nil, materialize.LimitError(normalizedMaxMemoryBytes(e.config.MaxMemoryBytes))
			}
			budget.pathCount++
		}
	}
	return budget, nil
}

func retainedPathStateBytes(path *pathResult) (int, bool, bool) {
	if path == nil || path.state == nil {
		return 0, false, true
	}
	stateBytes, ok := stateMaterialization(path.state)
	return stateBytes, true, ok
}

func (budget *retainedPathBudget) replace(previousBytes int, previousRetained bool, successors []*pathResult) bool {
	if previousRetained {
		if budget.pathCount == 0 || previousBytes > budget.stateBytes {
			return false
		}
		budget.pathCount--
		budget.stateBytes -= previousBytes
	}
	for _, successor := range successors {
		stateBytes, retained, ok := retainedPathStateBytes(successor)
		if !ok {
			return false
		}
		if !retained {
			continue
		}
		if !budget.baselineSet {
			budget.baseline = successor.state.initialBytes
			budget.baselineSet = true
		}
		budget.stateBytes, ok = materialize.Add(budget.stateBytes, stateBytes, math.MaxInt)
		if !ok || budget.pathCount == math.MaxInt {
			return false
		}
		budget.pathCount++
	}
	return true
}

func (e *ExecutionContext) checkRetainedPathBudget(budget *retainedPathBudget, additional int) error {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	total, ok := e.retainedAuxiliaryBytes(maximum)
	if !ok {
		return materialize.LimitError(maximum)
	}
	pathBytes, ok := multiplyMaterialization(budget.pathCount, materialize.EntryBytes, maximum)
	if ok {
		total, ok = materialize.Add(total, pathBytes, maximum)
	}
	stateBytes := budget.stateBytes
	if stateBytes > budget.baseline {
		stateBytes -= budget.baseline
	} else {
		stateBytes = 0
	}
	if ok {
		total, ok = materialize.Add(total, stateBytes, maximum)
	}
	if ok {
		_, ok = materialize.Add(total, additional, maximum)
	}
	if !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}

func (e *ExecutionContext) checkRepeatedPathMaterialization(path *pathResult, count int, source *location) error {
	if count <= 1 || path == nil || path.state == nil || path.status != StatusCompleted {
		return nil
	}
	if err := e.ctx.Err(); err != nil {
		return e.failPaths([]*pathResult{path}, err, source)
	}
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	stateBytes, ok := stateMaterialization(path.state)
	if !ok {
		return e.failMaterialization([]*pathResult{path}, source)
	}
	stateBytes, ok = multiplyMaterialization(stateBytes, count, math.MaxInt)
	if !ok {
		return e.failMaterialization([]*pathResult{path}, source)
	}
	if stateBytes > path.state.initialBytes {
		stateBytes -= path.state.initialBytes
	} else {
		stateBytes = 0
	}
	pathBytes, ok := multiplyMaterialization(materialize.EntryBytes, count, maximum)
	if ok {
		pathBytes, ok = materialize.Add(pathBytes, stateBytes, maximum)
	}
	if ok {
		auxiliary, auxiliaryOK := e.retainedAuxiliaryBytes(maximum)
		if auxiliaryOK {
			_, ok = materialize.Add(pathBytes, auxiliary, maximum)
		} else {
			ok = false
		}
	}
	if !ok {
		return e.failMaterialization([]*pathResult{path}, source)
	}
	return nil
}

func (e *ExecutionContext) retainedAuxiliaryBytes(maximum int) (int, bool) {
	total, ok := materialize.Add(0, e.variableRollbackBytes, maximum)
	if ok {
		total, ok = materialize.Add(total, e.nestedShellBytes, maximum)
	}
	if ok && e.candidates != nil {
		total, ok = materialize.Add(total, e.candidates.dynamicBytes, maximum)
	}
	return total, ok
}

func (e *ExecutionContext) checkPathGroupsMaterialization(additional int, source *location, groups ...[]*pathResult) error {
	if err := e.ctx.Err(); err != nil {
		return e.failPathGroups(groups, err, source)
	}
	budget, err := e.newRetainedPathBudget(groups...)
	if err != nil {
		return e.failPathGroups(groups, err, source)
	}
	if err = e.checkRetainedPathBudget(budget, additional); err != nil {
		return e.failPathGroups(groups, err, source)
	}
	return nil
}

func (e *ExecutionContext) failMaterializationGroups(groups [][]*pathResult, source *location) error {
	err := materialize.LimitError(normalizedMaxMemoryBytes(e.config.MaxMemoryBytes))
	return e.failPathGroups(groups, err, source)
}

func (e *ExecutionContext) failPathGroups(groups [][]*pathResult, err error, source *location) error {
	for _, paths := range groups {
		for _, path := range paths {
			if path == nil || path.state == nil || path.status != StatusCompleted {
				continue
			}
			e.setIssue(path.state, err, source)
			path.status = StatusIncomplete
		}
	}
	return err
}

func (e *ExecutionContext) failMaterialization(paths []*pathResult, source *location) error {
	return e.failMaterializationGroups([][]*pathResult{paths}, source)
}

func (e *ExecutionContext) failPaths(paths []*pathResult, err error, source *location) error {
	return e.failPathGroups([][]*pathResult{paths}, err, source)
}

func commandInvocationMaterialization(s *State, name string, args []*Argument, input []byte) (int, bool) {
	total := materialize.EntryBytes
	add := func(size int) bool {
		var ok bool
		total, ok = materialize.Add(total, size, math.MaxInt)
		return ok
	}
	directory, _ := s.dir.Data()
	if !add(len(name)) || !add(len(directory)) || !add(len(input)) {
		return 0, false
	}
	for _, argument := range args {
		if !add(materialize.EntryBytes + len(argument.Value)) {
			return 0, false
		}
	}
	for variableName, value := range s.vars.data {
		if !value.Exported || !value.IsSet() {
			continue
		}
		if !add(materialize.EntryBytes + len(variableName) + len(value.String())) {
			return 0, false
		}
		if s.vars.isUnknown(variableName) && !add(materialize.EntryBytes+len(variableName)) {
			return 0, false
		}
	}
	return total, true
}

func (e *ExecutionContext) checkBraceExpansion(word *syntax.Word, materializedBytes int) error {
	maximum := normalizedMaxMemoryBytes(e.config.MaxMemoryBytes)
	if materializedBytes < 0 || materializedBytes > maximum {
		return materialize.LimitError(maximum)
	}
	if word == nil {
		return nil
	}
	if _, ok := braceExpansionMaterialization(word, maximum-materializedBytes); !ok {
		return materialize.LimitError(maximum)
	}
	return nil
}

type braceMaterialization struct {
	count        int
	literalBytes int
}

func braceExpansionMaterialization(word *syntax.Word, maximum int) (*braceMaterialization, bool) {
	if maximum < 0 {
		return nil, false
	}
	if word == nil {
		return &braceMaterialization{}, true
	}
	copyWord := *word
	if !syntax.SplitBraces(&copyWord) {
		return &braceMaterialization{}, true
	}
	result, ok := splitBraceWordMaterialization(&copyWord, maximum)
	if !ok {
		return nil, false
	}
	entryBytes, ok := multiplyMaterialization(result.count, materialize.EntryBytes, maximum)
	if ok {
		_, ok = materialize.Add(entryBytes, result.literalBytes, maximum)
	}
	return result, ok
}

func splitBraceWordMaterialization(word *syntax.Word, maximum int) (*braceMaterialization, bool) {
	result := &braceMaterialization{count: 1}
	for _, part := range word.Parts {
		partResult := &braceMaterialization{count: 1}
		switch part := part.(type) {
		case *syntax.Lit:
			partResult.literalBytes = len(part.Value)
		case *syntax.BraceExp:
			var ok bool
			if part.Sequence {
				partResult, ok = braceSequenceMaterialization(part, maximum)
			} else {
				partResult = &braceMaterialization{}
				for _, element := range part.Elems {
					elementResult, elementOK := splitBraceWordMaterialization(element, maximum)
					if !elementOK {
						return nil, false
					}
					partResult, ok = addBraceAlternatives(partResult, elementResult, maximum)
					if !ok {
						return nil, false
					}
				}
			}
			if !ok {
				return nil, false
			}
		}
		var ok bool
		result, ok = multiplyBraceParts(result, partResult, maximum)
		if !ok {
			return nil, false
		}
	}
	return result, true
}

func addBraceAlternatives(left, right *braceMaterialization, maximum int) (*braceMaterialization, bool) {
	maximumEntries := maximum / materialize.EntryBytes
	count, ok := materialize.Add(left.count, right.count, maximumEntries)
	if !ok {
		return nil, false
	}
	literalBytes, ok := materialize.Add(left.literalBytes, right.literalBytes, maximum)
	if !ok {
		return nil, false
	}
	return &braceMaterialization{count: count, literalBytes: literalBytes}, true
}

func multiplyBraceParts(left, right *braceMaterialization, maximum int) (*braceMaterialization, bool) {
	maximumEntries := maximum / materialize.EntryBytes
	count, ok := multiplyMaterialization(left.count, right.count, maximumEntries)
	if !ok {
		return nil, false
	}
	leftBytes, ok := multiplyMaterialization(left.literalBytes, right.count, maximum)
	if !ok {
		return nil, false
	}
	rightBytes, ok := multiplyMaterialization(right.literalBytes, left.count, maximum)
	if !ok {
		return nil, false
	}
	literalBytes, ok := materialize.Add(leftBytes, rightBytes, maximum)
	if !ok {
		return nil, false
	}
	return &braceMaterialization{count: count, literalBytes: literalBytes}, true
}

func multiplyMaterialization(left, right, maximum int) (int, bool) {
	if left < 0 || right < 0 || maximum < 0 || left != 0 && right > maximum/left {
		return 0, false
	}
	return left * right, true
}

type braceSequence struct {
	first        int
	step         int
	count        int
	character    bool
	leadingZeros int
}

func parseBraceSequence(brace *syntax.BraceExp, maximum int) (*braceSequence, bool) {
	if len(brace.Elems) < 2 || maximum < 1 {
		return nil, false
	}
	fromText, toText := brace.Elems[0].Lit(), brace.Elems[1].Lit()
	from, fromErr := strconv.Atoi(fromText)
	to, toErr := strconv.Atoi(toText)
	character := fromErr != nil || toErr != nil
	if fromErr != nil || toErr != nil {
		if fromText == "" || toText == "" {
			return nil, false
		}
		from, to = int(fromText[0]), int(toText[0])
	}
	upward := from <= to
	step := 1
	if !upward {
		step = -1
	}
	if len(brace.Elems) > 2 {
		if parsed, err := strconv.Atoi(brace.Elems[2].Lit()); err == nil && parsed != 0 && parsed > 0 == upward {
			step = parsed
		}
	}
	distance := uint64(to) - uint64(from)
	if !upward {
		distance = uint64(from) - uint64(to)
	}
	stepSize := uint64(step)
	if step < 0 {
		stepSize = uint64(-(step + 1)) + 1
	}
	quotient := distance / stepSize
	if quotient >= uint64(maximum) {
		return nil, false
	}
	remainder := distance % stepSize
	maximumInt := int(^uint(0) >> 1)
	minimumInt := -maximumInt - 1
	if upward {
		room := uint64(maximumInt) - uint64(to) + remainder
		if stepSize > room {
			return nil, false
		}
	} else {
		room := uint64(to) - uint64(minimumInt) + remainder
		if stepSize > room {
			return nil, false
		}
	}
	return &braceSequence{
		first:        from,
		step:         step,
		count:        int(quotient) + 1,
		character:    character,
		leadingZeros: max(extraLeadingZeros(fromText), extraLeadingZeros(toText)),
	}, true
}

func braceSequenceMaterialization(brace *syntax.BraceExp, maximum int) (*braceMaterialization, bool) {
	sequence, ok := parseBraceSequence(brace, maximum/materialize.EntryBytes)
	if !ok {
		return nil, false
	}
	if sequence.character {
		return &braceMaterialization{count: sequence.count, literalBytes: sequence.count}, true
	}
	literalBytes := 0
	current := sequence.first
	for index := range sequence.count {
		valueBytes := sequence.leadingZeros + len(strconv.Itoa(current))
		literalBytes, ok = materialize.Add(literalBytes, valueBytes, maximum)
		if !ok {
			return nil, false
		}
		if index+1 < sequence.count {
			current += sequence.step
		}
	}
	return &braceMaterialization{count: sequence.count, literalBytes: literalBytes}, true
}

func extraLeadingZeros(value string) int {
	for index, character := range value {
		if character != '0' {
			return index
		}
	}
	return 0
}
