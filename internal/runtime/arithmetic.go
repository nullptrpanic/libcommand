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
	if err := validateParameterExpansions(s, expression, false); err != nil {
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
	value, err = evaluateBashArithmetic(config, environment, expression)
	if environment.err != nil {
		return 0, environment.err
	}
	return value, err
}

type bashArithmeticEvaluator struct {
	config      *expand.Config
	environment *bashArithmeticEnvironment
	resolving   map[string]struct{}
}

func evaluateBashArithmetic(config *expand.Config, environment *bashArithmeticEnvironment, expression syntax.ArithmExpr) (int, error) {
	evaluator := &bashArithmeticEvaluator{
		config:      config,
		environment: environment,
		resolving:   make(map[string]struct{}),
	}
	value, err := evaluator.evaluate(expression)
	return int(value), err
}

func (evaluator *bashArithmeticEvaluator) evaluate(expression syntax.ArithmExpr) (int64, error) {
	if err := evaluator.environment.ExecutionContext.ctx.Err(); err != nil {
		return 0, err
	}
	switch expression := expression.(type) {
	case *syntax.Word:
		value, err := expand.Literal(evaluator.config, expression)
		if err != nil {
			return 0, err
		}
		return evaluator.evaluateText(value)
	case *syntax.ParenArithm:
		return evaluator.evaluate(expression.X)
	case *syntax.UnaryArithm:
		return evaluator.evaluateUnary(expression)
	case *syntax.BinaryArithm:
		return evaluator.evaluateBinary(expression)
	case *syntax.FlagsArithm:
		return 0, errors.New("unsupported arithmetic flags")
	default:
		return 0, fmt.Errorf("unsupported arithmetic expression %T", expression)
	}
}

func (evaluator *bashArithmeticEvaluator) evaluateText(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if literal, recognized, err := parseBashIntegerLiteral(value); recognized {
		return literal, err
	}
	_, syntheticReference := evaluator.environment.references[value]
	_, _, arrayReference := splitArrayReferenceName(value)
	if syntax.ValidName(value) || syntheticReference || arrayReference {
		return evaluator.evaluateReference(value)
	}
	// Expansion can return the same expression text, not only a variable
	// reference. Re-parsing it must share the existing recursion bound.
	if _, resolving := evaluator.resolving[value]; resolving || len(evaluator.resolving) >= 1024 {
		return 0, errors.New("arithmetic expression recursion limit reached")
	}
	evaluator.resolving[value] = struct{}{}
	defer delete(evaluator.resolving, value)
	expression, err := ParseArithmetic(evaluator.environment.ExecutionContext.ctx, value)
	if err != nil {
		return 0, err
	}
	return evaluator.evaluate(expression)
}

func (evaluator *bashArithmeticEvaluator) evaluateReference(name string) (int64, error) {
	if _, resolving := evaluator.resolving[name]; resolving {
		return 0, fmt.Errorf("expression recursion level exceeded for %s", name)
	}
	if len(evaluator.resolving) >= 1024 {
		return 0, errors.New("arithmetic expression recursion limit reached")
	}
	evaluator.resolving[name] = struct{}{}
	defer delete(evaluator.resolving, name)
	value := evaluator.environment.Get(name)
	if evaluator.environment.err != nil {
		return 0, evaluator.environment.err
	}
	if !value.IsSet() {
		return 0, nil
	}
	return evaluator.evaluateText(value.String())
}

func (evaluator *bashArithmeticEvaluator) evaluateUnary(expression *syntax.UnaryArithm) (int64, error) {
	if expression.Op == syntax.Inc || expression.Op == syntax.Dec {
		name, err := evaluator.assignmentName(expression.X)
		if err != nil {
			return 0, err
		}
		old, err := evaluator.evaluateReference(name)
		if err != nil {
			return 0, err
		}
		value := old
		if expression.Op == syntax.Inc {
			value++
		} else {
			value--
		}
		if err := evaluator.assign(name, value); err != nil {
			return 0, err
		}
		if expression.Post {
			return old, nil
		}
		return value, nil
	}
	value, err := evaluator.evaluate(expression.X)
	if err != nil {
		return 0, err
	}
	switch expression.Op {
	case syntax.Not:
		return arithmeticBoolean(value == 0), nil
	case syntax.BitNegation:
		return ^value, nil
	case syntax.Plus:
		return value, nil
	case syntax.Minus:
		return -value, nil
	default:
		return 0, fmt.Errorf("unsupported unary arithmetic operator %q", expression.Op)
	}
}

func (evaluator *bashArithmeticEvaluator) evaluateBinary(expression *syntax.BinaryArithm) (int64, error) {
	switch expression.Op {
	case syntax.TernQuest:
		condition, err := evaluator.evaluate(expression.X)
		if err != nil {
			return 0, err
		}
		branches, ok := expression.Y.(*syntax.BinaryArithm)
		if !ok || branches.Op != syntax.TernColon {
			return 0, errors.New("invalid ternary arithmetic expression")
		}
		if condition != 0 {
			return evaluator.evaluate(branches.X)
		}
		return evaluator.evaluate(branches.Y)
	case syntax.AndArit:
		left, err := evaluator.evaluate(expression.X)
		if err != nil || left == 0 {
			return 0, err
		}
		right, err := evaluator.evaluate(expression.Y)
		return arithmeticBoolean(right != 0), err
	case syntax.OrArit:
		left, err := evaluator.evaluate(expression.X)
		if err != nil {
			return 0, err
		}
		if left != 0 {
			return 1, nil
		}
		right, err := evaluator.evaluate(expression.Y)
		return arithmeticBoolean(right != 0), err
	case syntax.Comma:
		if _, err := evaluator.evaluate(expression.X); err != nil {
			return 0, err
		}
		return evaluator.evaluate(expression.Y)
	case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn,
		syntax.QuoAssgn, syntax.RemAssgn, syntax.AndAssgn, syntax.OrAssgn,
		syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn, syntax.AndBoolAssgn,
		syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
		return evaluator.evaluateAssignment(expression)
	}
	left, err := evaluator.evaluate(expression.X)
	if err != nil {
		return 0, err
	}
	right, err := evaluator.evaluate(expression.Y)
	if err != nil {
		return 0, err
	}
	return applyBashArithmeticOperator(expression.Op, left, right)
}

func (evaluator *bashArithmeticEvaluator) evaluateAssignment(expression *syntax.BinaryArithm) (int64, error) {
	name, err := evaluator.assignmentName(expression.X)
	if err != nil {
		return 0, err
	}
	value := int64(0)
	if expression.Op == syntax.Assgn {
		value, err = evaluator.evaluate(expression.Y)
	} else {
		left, leftErr := evaluator.evaluateReference(name)
		if leftErr != nil {
			return 0, leftErr
		}
		right, rightErr := evaluator.evaluate(expression.Y)
		if rightErr != nil {
			return 0, rightErr
		}
		operator, ok := arithmeticAssignmentOperator(expression.Op)
		if !ok {
			return 0, fmt.Errorf("unsupported arithmetic assignment operator %q", expression.Op)
		}
		value, err = applyBashArithmeticOperator(operator, left, right)
	}
	if err != nil {
		return 0, err
	}
	if err := evaluator.assign(name, value); err != nil {
		return 0, err
	}
	return value, nil
}

func (evaluator *bashArithmeticEvaluator) assignmentName(expression syntax.ArithmExpr) (string, error) {
	word, ok := expression.(*syntax.Word)
	if !ok {
		return "", errors.New("arithmetic assignment target is not a variable")
	}
	name := word.Lit()
	if name == "" {
		return "", errors.New("arithmetic assignment target is empty")
	}
	return name, nil
}

func (evaluator *bashArithmeticEvaluator) assign(name string, value int64) error {
	return evaluator.environment.Set(name, expand.Variable{
		Set:  true,
		Kind: expand.String,
		Str:  strconv.FormatInt(value, 10),
	})
}

func arithmeticAssignmentOperator(operator syntax.BinAritOperator) (syntax.BinAritOperator, bool) {
	switch operator {
	case syntax.AddAssgn:
		return syntax.Add, true
	case syntax.SubAssgn:
		return syntax.Sub, true
	case syntax.MulAssgn:
		return syntax.Mul, true
	case syntax.QuoAssgn:
		return syntax.Quo, true
	case syntax.RemAssgn:
		return syntax.Rem, true
	case syntax.AndAssgn:
		return syntax.And, true
	case syntax.OrAssgn:
		return syntax.Or, true
	case syntax.XorAssgn:
		return syntax.Xor, true
	case syntax.ShlAssgn:
		return syntax.Shl, true
	case syntax.ShrAssgn:
		return syntax.Shr, true
	case syntax.AndBoolAssgn:
		return syntax.AndArit, true
	case syntax.OrBoolAssgn:
		return syntax.OrArit, true
	case syntax.XorBoolAssgn:
		return syntax.XorBool, true
	case syntax.PowAssgn:
		return syntax.Pow, true
	default:
		return 0, false
	}
}

func applyBashArithmeticOperator(operator syntax.BinAritOperator, left, right int64) (int64, error) {
	switch operator {
	case syntax.Add:
		return left + right, nil
	case syntax.Sub:
		return left - right, nil
	case syntax.Mul:
		return left * right, nil
	case syntax.Quo:
		if right == 0 {
			return 0, errors.New("division by zero")
		}
		return left / right, nil
	case syntax.Rem:
		if right == 0 {
			return 0, errors.New("division by zero")
		}
		return left % right, nil
	case syntax.Pow:
		if right < 0 {
			return 0, errors.New("exponent less than 0")
		}
		value := int64(1)
		for right != 0 {
			if right&1 != 0 {
				value *= left
			}
			right >>= 1
			left *= left
		}
		return value, nil
	case syntax.Eql:
		return arithmeticBoolean(left == right), nil
	case syntax.Gtr:
		return arithmeticBoolean(left > right), nil
	case syntax.Lss:
		return arithmeticBoolean(left < right), nil
	case syntax.Neq:
		return arithmeticBoolean(left != right), nil
	case syntax.Leq:
		return arithmeticBoolean(left <= right), nil
	case syntax.Geq:
		return arithmeticBoolean(left >= right), nil
	case syntax.And:
		return left & right, nil
	case syntax.Or:
		return left | right, nil
	case syntax.Xor:
		return left ^ right, nil
	case syntax.Shr:
		return left >> (uint64(right) & 63), nil
	case syntax.Shl:
		return left << (uint64(right) & 63), nil
	case syntax.AndArit:
		return arithmeticBoolean(left != 0 && right != 0), nil
	case syntax.OrArit:
		return arithmeticBoolean(left != 0 || right != 0), nil
	case syntax.XorBool:
		return arithmeticBoolean(left != 0 != (right != 0)), nil
	default:
		return 0, fmt.Errorf("unsupported binary arithmetic operator %q", operator)
	}
}

func arithmeticBoolean(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

type arithmeticExpansionRewrite struct {
	expansion  *syntax.ArithmExp
	expression syntax.ArithmExpr
}

func (e *ExecutionContext) prepareArithmeticExpansions(s *State, word *syntax.Word) (func(), error) {
	rewrites := make([]*arithmeticExpansionRewrite, 0)
	var evaluationErr error
	syntax.Walk(word, func(node syntax.Node) bool {
		if evaluationErr != nil {
			return false
		}
		if _, substitution := node.(*syntax.CmdSubst); substitution {
			return false
		}
		expansion, ok := node.(*syntax.ArithmExp)
		if !ok {
			return true
		}
		value, err := e.arithmeticValue(s, expansion.X)
		if err != nil {
			evaluationErr = err
			return false
		}
		rewrites = append(rewrites, &arithmeticExpansionRewrite{expansion: expansion, expression: expansion.X})
		expansion.X = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{
			ValuePos: expansion.X.Pos(),
			ValueEnd: expansion.X.End(),
			Value:    strconv.Itoa(value),
		}}}
		return false
	})
	restore := func() {
		for index := len(rewrites) - 1; index >= 0; index-- {
			rewrite := rewrites[index]
			rewrite.expansion.X = rewrite.expression
		}
	}
	if evaluationErr != nil {
		restore()
		return noopRestore, evaluationErr
	}
	return restore, nil
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
