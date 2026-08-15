package runtime

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type arithmeticResult struct {
	path         *pathResult
	value        int
	expansionErr error
	dataUnknown  bool
}

func (e *ExecutionContext) arithmeticValue(s *State, expression syntax.ArithmExpr) (value int, err error) {
	if expression == nil {
		return 0, errors.New("missing arithmetic expression")
	}
	if err := validateUnknownParameterExpansions(s, expression); err != nil {
		return 0, err
	}
	if err := normalizeArithmeticLiterals(expression); err != nil {
		return 0, err
	}
	restore := maskInactiveParameterWords(s, expression)
	defer restore()
	originalVars := s.vars
	s.vars = s.vars.clone()
	defer func() {
		if expansionRequested(err) {
			s.vars = originalVars
		}
	}()
	config := e.expansionConfig(s)
	environment := &bashArithmeticEnvironment{
		stateEnvironment: &stateEnvironment{state: s, maximum: e.config.MaxMemoryBytes},
		ExecutionContext: e,
		references:       make(map[string]*collectionReference),
	}
	restoreCollections, err := e.prepareArithmeticCollectionMutations(s, expression, environment)
	if err != nil {
		return 0, err
	}
	defer restoreCollections()
	config.Env = environment
	value, err = expand.Arithm(config, expression)
	if environment.err != nil {
		return 0, environment.err
	}
	return value, err
}

type bashArithmeticEnvironment struct {
	*stateEnvironment
	ExecutionContext *ExecutionContext
	references       map[string]*collectionReference
	err              error
}

func (environment *bashArithmeticEnvironment) Get(name string) expand.Variable {
	if reference, ok := environment.arrayReference(name); ok {
		return collectionElement(environment.state, reference)
	}
	value := environment.stateEnvironment.Get(name)
	if environment.err != nil || value.Kind != expand.String {
		return value
	}
	literal, recognized, err := parseBashIntegerLiteral(strings.TrimSpace(value.Str))
	if !recognized {
		return value
	}
	if err != nil {
		environment.err = err
		value.Str = "0"
		return value
	}
	value.Str = strconv.FormatInt(literal, 10)
	return value
}

func (environment *bashArithmeticEnvironment) Set(name string, value expand.Variable) error {
	if reference, ok := environment.arrayReference(name); ok {
		return setCollectionElement(environment.state, reference, value, environment.ExecutionContext.config.MaxMemoryBytes)
	}
	return environment.stateEnvironment.Set(name, value)
}

func (environment *bashArithmeticEnvironment) arrayReference(name string) (*collectionReference, bool) {
	if reference, exists := environment.references[name]; exists {
		return reference, true
	}
	base, subscript, ok := splitArrayReferenceName(name)
	if !ok {
		return nil, false
	}
	reference := &collectionReference{name: base}
	if environment.state.vars.Get(base).Kind == expand.Associative {
		reference.associative = true
		reference.key = subscript
	} else if index, err := strconv.Atoi(subscript); err == nil {
		reference.index = index
	} else {
		expression, err := ParseArithmetic(environment.ExecutionContext.ctx, subscript)
		if err == nil {
			reference.index, err = environment.ExecutionContext.arithmeticValue(environment.state, expression)
		}
		if err != nil {
			environment.err = fmt.Errorf("evaluate array index %q: %w", subscript, err)
			return nil, false
		}
	}
	if reference.index < 0 {
		environment.err = fmt.Errorf("invalid array index")
		return nil, false
	}
	environment.references[name] = reference
	return reference, true
}

func splitArrayReferenceName(name string) (string, string, bool) {
	open := strings.IndexByte(name, '[')
	if open <= 0 || !strings.HasSuffix(name, "]") || !syntax.ValidName(name[:open]) {
		return "", "", false
	}
	return name[:open], name[open+1 : len(name)-1], true
}

type arithmeticCollectionRewrite struct {
	word  *syntax.Word
	parts []syntax.WordPart
}

func (e *ExecutionContext) prepareArithmeticCollectionMutations(s *State, expression syntax.ArithmExpr, environment *bashArithmeticEnvironment) (func(), error) {
	rewrites := make([]*arithmeticCollectionRewrite, 0)
	var prepareErr error
	syntax.Walk(expression, func(node syntax.Node) bool {
		if prepareErr != nil {
			return false
		}
		word := arithmeticMutationWord(node)
		parameter := nakedArrayParameter(word)
		if parameter == nil {
			return true
		}
		reference, err := e.collectionReference(s, parameter.Param.Value, parameter.Index)
		if err != nil {
			prepareErr = err
			return false
		}
		name := fmt.Sprintf("\x00libcommand_arithmetic_%d", len(rewrites))
		environment.references[name] = reference
		parts := word.Parts
		word.Parts = []syntax.WordPart{&syntax.Lit{
			ValuePos: parts[0].Pos(),
			ValueEnd: parts[len(parts)-1].End(),
			Value:    name,
		}}
		rewrites = append(rewrites, &arithmeticCollectionRewrite{word: word, parts: parts})
		return true
	})
	restore := func() {
		for index := len(rewrites) - 1; index >= 0; index-- {
			rewrite := rewrites[index]
			rewrite.word.Parts = rewrite.parts
		}
	}
	if prepareErr != nil {
		restore()
		return noopRestore, prepareErr
	}
	return restore, nil
}

func arithmeticMutationWord(node syntax.Node) *syntax.Word {
	switch node := node.(type) {
	case *syntax.BinaryArithm:
		switch node.Op {
		case syntax.Assgn,
			syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn,
			syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn,
			syntax.AndBoolAssgn, syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
			word, _ := node.X.(*syntax.Word)
			return word
		}
	case *syntax.UnaryArithm:
		if node.Op == syntax.Inc || node.Op == syntax.Dec {
			word, _ := node.X.(*syntax.Word)
			return word
		}
	}
	return nil
}

func nakedArrayParameter(word *syntax.Word) *syntax.ParamExp {
	if word == nil || len(word.Parts) != 1 {
		return nil
	}
	parameter, ok := word.Parts[0].(*syntax.ParamExp)
	if !ok || parameter.Param == nil || parameter.Index == nil || parameter.Dollar.IsValid() {
		return nil
	}
	return parameter
}

func normalizeArithmeticLiterals(expression syntax.ArithmExpr) error {
	if expression == nil {
		return nil
	}
	type replacement struct {
		word  *syntax.Word
		value string
	}
	var replacements []replacement
	var normalizationErr error
	syntax.Walk(expression, func(node syntax.Node) bool {
		if normalizationErr != nil {
			return false
		}
		word, ok := node.(*syntax.Word)
		if !ok {
			return true
		}
		value := word.Lit()
		literal, recognized, err := parseBashIntegerLiteral(value)
		if !recognized {
			return true
		}
		if err != nil {
			normalizationErr = err
			return false
		}
		replacements = append(replacements, replacement{word: word, value: strconv.FormatInt(literal, 10)})
		return false
	})
	if normalizationErr != nil {
		return normalizationErr
	}
	for _, replacement := range replacements {
		parts := replacement.word.Parts
		replacement.word.Parts = []syntax.WordPart{&syntax.Lit{
			ValuePos: parts[0].Pos(),
			ValueEnd: parts[len(parts)-1].End(),
			Value:    replacement.value,
		}}
	}
	return nil
}

func normalizeWordArithmeticLiterals(word *syntax.Word) error {
	if word == nil || word.Lit() != "" {
		return nil
	}
	var normalizationErr error
	syntax.Walk(word, func(node syntax.Node) bool {
		if normalizationErr != nil {
			return false
		}
		switch node := node.(type) {
		case *syntax.ArithmExp:
			normalizationErr = normalizeArithmeticLiterals(node.X)
		case *syntax.ParamExp:
			if node.Index != nil {
				normalizationErr = normalizeArithmeticLiterals(node.Index)
			}
			if normalizationErr == nil && node.Slice != nil {
				normalizationErr = normalizeArithmeticLiterals(node.Slice.Offset)
				if normalizationErr == nil && node.Slice.Length != nil {
					normalizationErr = normalizeArithmeticLiterals(node.Slice.Length)
				}
			}
		}
		return normalizationErr == nil
	})
	return normalizationErr
}

func parseBashIntegerLiteral(value string) (int64, bool, error) {
	original := value
	negative := false
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		negative = value[0] == '-'
		value = value[1:]
	}
	if value == "" {
		return 0, false, nil
	}
	base := 10
	digits := value
	if baseText, number, found := strings.Cut(value, "#"); found {
		parsedBase, err := strconv.Atoi(baseText)
		if err != nil || parsedBase < 2 || parsedBase > 64 || number == "" {
			return 0, true, fmt.Errorf("%s: invalid arithmetic constant", original)
		}
		base, digits = parsedBase, number
	} else if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		base, digits = 16, value[2:]
		if digits == "" {
			return 0, true, fmt.Errorf("%s: invalid arithmetic constant", original)
		}
	} else {
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return 0, false, nil
			}
		}
		if len(value) > 1 && value[0] == '0' {
			base = 8
		}
	}

	var number uint64
	for _, digit := range digits {
		value := bashArithmeticDigit(digit, base)
		if value < 0 || value >= base {
			return 0, true, fmt.Errorf("%s: value too great for base", original)
		}
		number = number*uint64(base) + uint64(value)
	}
	if negative {
		return int64(0 - number), true, nil
	}
	return int64(number), true, nil
}

func bashArithmeticDigit(digit rune, base int) int {
	switch {
	case digit >= '0' && digit <= '9':
		return int(digit - '0')
	case digit >= 'a' && digit <= 'z':
		return int(digit-'a') + 10
	case digit >= 'A' && digit <= 'Z':
		if base <= 36 {
			return int(digit-'A') + 10
		}
		return int(digit-'A') + 36
	case digit == '@':
		return 62
	case digit == '_':
		return 63
	default:
		return -1
	}
}

func arithmeticPathResults(results []*arithmeticResult) []*pathResult {
	paths := make([]*pathResult, 0, len(results))
	for _, result := range results {
		paths = append(paths, result.path)
	}
	return paths
}

func (e *ExecutionContext) evaluateArithmeticEffect(s *State, expression syntax.ArithmExpr, description string) ([]*pathResult, bool, error) {
	hostUnknown := arithmHasHostUnknown(s, expression)
	originalVars := s.vars
	values, err := e.evaluateArithmeticExpression(s, expression)
	if err != nil {
		return arithmeticPathResults(values), hostUnknown, err
	}
	paths := make([]*pathResult, 0, len(values))
	for _, value := range values {
		path := value.path
		if value.expansionErr != nil && path.status == StatusCompleted {
			result := e.unresolved(path.state, fmt.Sprintf("evaluate %s: %v", description, value.expansionErr), sourceLocation(expression))
			path.status = result.status
		}
		if (hostUnknown || value.dataUnknown) && path.status == StatusCompleted {
			path.state.vars = originalVars.clone()
			message := description + " depends on host runtime state"
			if value.dataUnknown {
				message = newUnknownValueError(description).Error()
			}
			result := e.unresolved(path.state, message, sourceLocation(expression))
			path.status = result.status
		}
		paths = append(paths, path)
	}
	dataUnknown := false
	for _, value := range values {
		dataUnknown = dataUnknown || value.dataUnknown
	}
	return paths, hostUnknown || dataUnknown, nil
}

func (e *ExecutionContext) evaluateArithmeticExpression(s *State, expression syntax.ArithmExpr) ([]*arithmeticResult, error) {
	s.pushSubstitutionFrame()
	results, err := e.resumeArithmeticExpression(s, expression)
	if len(results) == 0 {
		s.popSubstitutionFrame()
	}
	for _, result := range results {
		result.path.state.popSubstitutionFrame()
	}
	return results, err
}

func (e *ExecutionContext) resumeArithmeticExpression(s *State, expression syntax.ArithmExpr) ([]*arithmeticResult, error) {
	value, err := e.arithmeticValue(s, expression)
	request, requested := requestedSubstitution(err)
	if !requested {
		return []*arithmeticResult{{
			path:         &pathResult{state: s, status: StatusCompleted},
			value:        value,
			expansionErr: err,
			dataUnknown:  arithmHasUnknownData(s, expression),
		}}, nil
	}
	substitutionPaths, substitutionErr := e.evaluateSubstitutionPaths(request.state, request.substitution)
	results := make([]*arithmeticResult, 0, len(substitutionPaths))
	for _, substitutionPath := range substitutionPaths {
		if substitutionPath.status != StatusCompleted {
			results = append(results, &arithmeticResult{path: substitutionPath})
			continue
		}
		resumed, resumeErr := e.resumeArithmeticExpression(substitutionPath.state, expression)
		results = append(results, resumed...)
		if resumeErr != nil {
			return results, resumeErr
		}
	}
	return results, substitutionErr
}

func (e *ExecutionContext) executeArithmetic(s *State, command *syntax.ArithmCmd) (*outcome, error) {
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(command)); status != StatusCompleted {
		return &outcome{status: status}, nil
	}
	value, err := e.arithmeticValue(s, command.X)
	if err != nil {
		if !expansionRequested(err) && !isUnknownValueError(err) {
			if result, resultErr, incomplete := e.incompleteFromEvaluationError(s, err, sourceLocation(command)); incomplete {
				return result, resultErr
			}
			s.setExitCode(1)
			return &outcome{stderr: []byte(fmt.Sprintf("arithmetic: %v\n", err)), status: StatusCompleted}, nil
		}
		return e.outcomeFromExpansionError(s, err, fmt.Sprintf("evaluate arithmetic command: %v", err), sourceLocation(command))
	}
	if value == 0 {
		s.setExitCode(1)
	} else {
		s.setExitCode(0)
	}
	return &outcome{status: StatusCompleted}, nil
}

func (e *ExecutionContext) arithmeticExpressionValue(s *State, expression syntax.ArithmExpr) (int, error) {
	word, dynamicallyParsed := expression.(*syntax.Word)
	if !dynamicallyParsed || word.Lit() != "" {
		return e.arithmeticValue(s, expression)
	}
	value, err := e.literalValue(s, word)
	if err != nil {
		return 0, err
	}
	parsed, err := ParseArithmetic(e.ctx, value)
	if err != nil {
		return 0, fmt.Errorf("parse arithmetic expression: %w", err)
	}
	if arithmHasUnknownData(s, parsed) {
		return 0, newUnknownValueError("let expression")
	}
	return e.arithmeticValue(s, parsed)
}

func (e *ExecutionContext) evaluateArithmetic(s *State, command *syntax.ArithmCmd) ([]*pathResult, error) {
	certainty := arithmeticCertainty(s, command.X)
	hostUnknown := certainty.hostUnknown()
	dataUnknown := certainty.dataUnknown()
	if !hostUnknown && !dataUnknown {
		result, err := e.executeArithmetic(s, command)
		if _, requested := requestedSubstitution(err); requested {
			return nil, err
		}
		return []*pathResult{{state: s, status: e.appendOutcome(s, result, sourceLocation(command))}}, err
	}
	if status := e.reserveExecutionSteps(s, 1, sourceLocation(command)); status != StatusCompleted {
		return []*pathResult{{state: s, status: status}}, nil
	}
	originalVars := s.vars
	values, err := e.evaluateArithmeticExpression(s, command.X)
	if err != nil {
		return arithmeticPathResults(values), err
	}
	results := make([]*pathResult, 0, len(values)*2)
	for _, value := range values {
		path := value.path
		if path.status != StatusCompleted {
			results = append(results, path)
			continue
		}
		path.state.vars = originalVars.clone()
		if value.expansionErr != nil {
			result := e.unresolved(path.state, fmt.Sprintf("evaluate arithmetic command: %v", value.expansionErr), sourceLocation(command))
			path.status = result.status
			results = append(results, path)
			continue
		}
		if arithmMayMutate(command.X) {
			message := "arithmetic mutation depends on host runtime state"
			if dataUnknown || value.dataUnknown {
				message = newUnknownValueError("arithmetic mutation").Error()
			}
			result := e.unresolved(path.state, message, sourceLocation(command))
			path.status = result.status
			results = append(results, path)
			continue
		}
		path.state.setUnknownExitCode()
		resolved, resolveErr := e.resolveExitStatus(path, sourceLocation(command))
		results = append(results, resolved...)
		if resolveErr != nil {
			return results, resolveErr
		}
	}
	return results, nil
}
