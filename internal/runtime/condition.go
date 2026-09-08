package runtime

import (
	"fmt"
	"regexp"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

type testFailure struct {
	exitCode int
	message  string
}

func (failure *testFailure) Error() string {
	return failure.message
}

func (e *ExecutionContext) testTruth(s *State, expression syntax.TestExpr) (truthValue, error) {
	switch expression := expression.(type) {
	case *syntax.Word:
		value, unknown, err := e.testWord(s, expression)
		if unknown {
			return truthUnknown, err
		}
		return truthFromBool(value != ""), err
	case *syntax.ParenTest:
		return e.testTruth(s, expression.X)
	case *syntax.UnaryTest:
		if expression.Op == syntax.TsNot {
			value, err := e.testTruth(s, expression.X)
			return invertTruth(value), err
		}
		word, ok := expression.X.(*syntax.Word)
		if !ok {
			return truthUnknown, nil
		}
		value, unknown, err := e.testWord(s, word)
		if unknown || err != nil {
			return truthUnknown, err
		}
		switch expression.Op {
		case syntax.TsEmpStr:
			return truthFromBool(value == ""), nil
		case syntax.TsNempStr:
			return truthFromBool(value != ""), nil
		case syntax.TsVarSet:
			return e.variableSetTruth(s, value)
		case syntax.TsExists, syntax.TsRegFile, syntax.TsDirect, syntax.TsCharSp, syntax.TsRead, syntax.TsWrite, syntax.TsExec, syntax.TsNoEmpty:
			return e.virtualFileTestTruth(s, expression.Op, value)
		default:
			return truthUnknown, nil
		}
	case *syntax.BinaryTest:
		if expression.Op == syntax.AndTest || expression.Op == syntax.OrTest {
			left, err := e.testTruth(s, expression.X)
			if err != nil {
				return truthUnknown, err
			}
			if expression.Op == syntax.AndTest && left == truthFalse {
				return truthFalse, nil
			}
			if expression.Op == syntax.OrTest && left == truthTrue {
				return truthTrue, nil
			}
			right, err := e.testTruth(s, expression.Y)
			if err != nil {
				return truthUnknown, err
			}
			if expression.Op == syntax.AndTest {
				return andTruth(left, right), nil
			}
			return orTruth(left, right), nil
		}
		leftWord, leftOK := expression.X.(*syntax.Word)
		rightWord, rightOK := expression.Y.(*syntax.Word)
		if !leftOK || !rightOK {
			return truthUnknown, nil
		}
		left, leftUnknown, err := e.testWord(s, leftWord)
		if err != nil {
			return truthUnknown, err
		}
		switch expression.Op {
		case syntax.TsReMatch:
			right, rightUnknown, err := e.testWord(s, rightWord)
			if err != nil {
				return truthUnknown, err
			}
			if leftUnknown || rightUnknown {
				s.vars.assignWithCertainty("BASH_REMATCH", expand.Variable{Set: true, Kind: expand.Indexed}, true)
				return truthUnknown, nil
			}
			regularExpression, err := regexp.CompilePOSIX(normalizeBashRegularExpression(right))
			if err != nil {
				s.vars.assignWithCertainty("BASH_REMATCH", expand.Variable{Set: true, Kind: expand.Indexed}, false)
				return truthFalse, &testFailure{exitCode: 2, message: fmt.Sprintf("invalid regular expression: %v", err)}
			}
			matches := regularExpression.FindStringSubmatch(left)
			s.vars.assignWithCertainty("BASH_REMATCH", expand.Variable{Set: true, Kind: expand.Indexed, List: matches}, false)
			return truthFromBool(matches != nil), nil
		case syntax.TsMatchShort, syntax.TsMatch:
			rightUnknown := literalWordCertainty(s, rightWord) != 0
			right, err := e.patternValue(s, rightWord)
			if err != nil {
				return truthUnknown, err
			}
			if leftUnknown || rightUnknown {
				return truthUnknown, nil
			}
			regularExpression, err := pattern.Regexp(right, pattern.EntireString)
			if err != nil {
				return truthUnknown, err
			}
			return truthFromBool(regexp.MustCompile(regularExpression).MatchString(left)), nil
		case syntax.TsNoMatch:
			rightUnknown := literalWordCertainty(s, rightWord) != 0
			right, err := e.patternValue(s, rightWord)
			if err != nil {
				return truthUnknown, err
			}
			if leftUnknown || rightUnknown {
				return truthUnknown, nil
			}
			regularExpression, err := pattern.Regexp(right, pattern.EntireString)
			if err != nil {
				return truthUnknown, err
			}
			return truthFromBool(!regexp.MustCompile(regularExpression).MatchString(left)), nil
		}
		right, rightUnknown, err := e.testWord(s, rightWord)
		if err != nil || leftUnknown || rightUnknown {
			return truthUnknown, err
		}
		switch expression.Op {
		case syntax.TsBefore:
			return truthFromBool(left < right), nil
		case syntax.TsAfter:
			return truthFromBool(left > right), nil
		case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
			leftNumber, leftUnknown, leftErr := e.arithmeticTestValue(s, left)
			if leftErr != nil {
				return truthFalse, e.arithmeticTestError(leftErr)
			}
			rightNumber, rightUnknown, rightErr := e.arithmeticTestValue(s, right)
			if rightErr != nil {
				return truthFalse, e.arithmeticTestError(rightErr)
			}
			if leftUnknown || rightUnknown {
				return truthUnknown, nil
			}
			return numericTruth(expression.Op, leftNumber, rightNumber), nil
		default:
			return truthUnknown, nil
		}
	default:
		return truthUnknown, nil
	}
}

func (e *ExecutionContext) variableSetTruth(s *State, value string) (truthValue, error) {
	name, index, arrayReference := splitArrayReferenceName(value)
	if !arrayReference {
		if variableHasUnknownData(s, value) {
			return truthUnknown, nil
		}
		return truthFromBool(s.vars.Get(value).IsSet()), nil
	}
	if s.vars.isUnknown(name) {
		return truthUnknown, nil
	}
	reference, err := e.collectionReference(s, name, &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: index}}})
	if err != nil {
		if isUnknownValueError(err) {
			return truthUnknown, nil
		}
		return truthFalse, err
	}
	return truthFromBool(collectionElement(s, reference).IsSet()), nil
}

func normalizeBashRegularExpression(value string) string {
	normalized := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) && isASCIIWordByte(value[index+1]) {
			index++
		}
		normalized = append(normalized, value[index])
	}
	return string(normalized)
}

func isASCIIWordByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value == '_'
}

func (e *ExecutionContext) arithmeticTestValue(s *State, value string) (int64, bool, error) {
	if value == "" {
		value = "0"
	}
	if literal, recognized, err := parseBashIntegerLiteral(value); recognized {
		return literal, false, err
	}
	expression, err := ParseArithmetic(e.ctx, value)
	if err != nil {
		return 0, false, err
	}
	if arithmeticCertainty(s, expression) != 0 {
		return 0, true, nil
	}
	result, err := e.arithmeticValue(s, expression)
	return int64(result), false, err
}

func (e *ExecutionContext) arithmeticTestError(err error) error {
	if _, incomplete := e.incompleteEvaluationIssue(err); incomplete {
		return err
	}
	return &testFailure{exitCode: 1, message: err.Error()}
}

func (e *ExecutionContext) virtualFileTestTruth(s *State, operator syntax.UnTestOperator, name string) (truthValue, error) {
	directory, _ := s.dir.Data()
	resolved := s.fs.resolve(directory, name)
	if s.fs.pathKind(resolved) == PathDevice {
		switch operator {
		case syntax.TsExists, syntax.TsCharSp, syntax.TsRead, syntax.TsWrite:
			return truthTrue, nil
		case syntax.TsRegFile, syntax.TsDirect, syntax.TsNoEmpty, syntax.TsExec:
			return truthFalse, nil
		default:
			return truthUnknown, nil
		}
	}
	if contents, exists := s.fs.files[resolved]; exists {
		switch operator {
		case syntax.TsDirect, syntax.TsExec, syntax.TsCharSp:
			return truthFalse, nil
		case syntax.TsNoEmpty:
			if s.fs.fileUnknown(resolved) {
				return truthUnknown, nil
			}
			return truthFromBool(len(contents) != 0), nil
		case syntax.TsExists, syntax.TsRegFile, syntax.TsRead, syntax.TsWrite:
			return truthTrue, nil
		default:
			return truthUnknown, nil
		}
	}
	if _, exists := s.fs.dirs[resolved]; exists {
		switch operator {
		case syntax.TsExists, syntax.TsDirect, syntax.TsRead, syntax.TsWrite, syntax.TsExec:
			return truthTrue, nil
		case syntax.TsRegFile, syntax.TsNoEmpty, syntax.TsCharSp:
			return truthFalse, nil
		default:
			return truthUnknown, nil
		}
	}
	return truthFalse, nil
}

func (e *ExecutionContext) testWord(s *State, word *syntax.Word) (string, bool, error) {
	certainty := literalWordCertainty(s, word)
	value, err := e.literalValue(s, word)
	if certainty.hostUnknown() {
		if expansionRequested(err) {
			return "", true, err
		}
		return "", true, nil
	}
	return value, certainty.dataUnknown(), err
}

func arithmMayMutate(expression syntax.ArithmExpr) bool {
	mutates := false
	syntax.Walk(expression, func(node syntax.Node) bool {
		if arithmeticNodeMayMutate(node) {
			mutates = true
			return false
		}
		return !mutates
	})
	return mutates
}

func wordMayMutateVariables(word *syntax.Word) bool {
	if word == nil || word.Lit() != "" {
		return false
	}
	mutates := false
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.ArithmExp:
			mutates = true
			return false
		case *syntax.ParamExp:
			if node.Exp != nil && (node.Exp.Op == syntax.AssignUnset || node.Exp.Op == syntax.AssignUnsetOrNull) {
				mutates = true
				return false
			}
		}
		if arithmeticNodeMayMutate(node) {
			mutates = true
			return false
		}
		return !mutates
	})
	return mutates
}

func arithmeticNodeMayMutate(node syntax.Node) bool {
	switch node := node.(type) {
	case *syntax.BinaryArithm:
		switch node.Op {
		case syntax.Assgn,
			syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn,
			syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn,
			syntax.AndBoolAssgn, syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
			return true
		}
	case *syntax.UnaryArithm:
		return node.Op == syntax.Inc || node.Op == syntax.Dec
	}
	return false
}

func truthFromBool(value bool) truthValue {
	if value {
		return truthTrue
	}
	return truthFalse
}

func invertTruth(value truthValue) truthValue {
	switch value {
	case truthTrue:
		return truthFalse
	case truthFalse:
		return truthTrue
	default:
		return truthUnknown
	}
}

func andTruth(left, right truthValue) truthValue {
	return combineTruth(left, right, truthFalse, truthTrue)
}

func orTruth(left, right truthValue) truthValue {
	return combineTruth(left, right, truthTrue, truthFalse)
}

func combineTruth(left, right, decisive, otherwise truthValue) truthValue {
	if left == decisive || right == decisive {
		return decisive
	}
	if left == truthUnknown || right == truthUnknown {
		return truthUnknown
	}
	return otherwise
}

func boolExitCode(value bool) int {
	if value {
		return 0
	}
	return 1
}

func numericTruth(operator syntax.BinTestOperator, left, right int64) truthValue {
	switch operator {
	case syntax.TsEql:
		return truthFromBool(left == right)
	case syntax.TsNeq:
		return truthFromBool(left != right)
	case syntax.TsLeq:
		return truthFromBool(left <= right)
	case syntax.TsGeq:
		return truthFromBool(left >= right)
	case syntax.TsLss:
		return truthFromBool(left < right)
	case syntax.TsGtr:
		return truthFromBool(left > right)
	default:
		return truthUnknown
	}
}
