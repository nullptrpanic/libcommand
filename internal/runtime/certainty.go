package runtime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type valueCertainty uint8

const (
	certaintyHostUnknown valueCertainty = 1 << iota
	certaintyDataUnknown
)

func (certainty valueCertainty) hostUnknown() bool {
	return certainty&certaintyHostUnknown != 0
}

func (certainty valueCertainty) dataUnknown() bool {
	return certainty&certaintyDataUnknown != 0
}

func wordCertainty(s *State, word *syntax.Word) valueCertainty {
	if word == nil {
		return 0
	}
	certainty := implicitWordCertainty(s, word)
	if word.Lit() != "" {
		return certainty
	}
	restore := maskInactiveParameterWords(s, word)
	defer restore()
	return certainty | unmaskedWordCertainty(s, word, stateHasUnknownData(s))
}

func implicitWordCertainty(s *State, word *syntax.Word) valueCertainty {
	var certainty valueCertainty
	if s.vars.isUnknown("IFS") && wordDependsOnIFS(word) {
		certainty |= certaintyDataUnknown
	}
	if name, tilde := leadingTildeName(word); tilde {
		if name == "" {
			if s.vars.isUnknown("HOME") {
				certainty |= certaintyDataUnknown
			}
		} else {
			certainty |= certaintyHostUnknown
		}
	}
	_, directoryUnresolved := s.dir.Data()
	if directoryUnresolved && !s.options.noGlob && wordHasRelativeGlob(s, word) {
		certainty |= certaintyHostUnknown
	}
	globIgnore := s.vars.Get("GLOBIGNORE")
	if wordMayExpandGlob(s, word) && (s.vars.isUnknown("GLOBIGNORE") || globIgnore.IsSet() && globIgnore.String() != "") {
		certainty |= certaintyDataUnknown
	}
	return certainty
}

func wordDependsOnIFS(word *syntax.Word) bool {
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp:
			return true
		case *syntax.DblQuoted:
			depends := false
			syntax.Walk(part, func(node syntax.Node) bool {
				parameter, ok := node.(*syntax.ParamExp)
				if !ok || parameter.Param == nil {
					return true
				}
				if parameter.Param.Value == "*" {
					depends = true
					return false
				}
				index, ok := parameter.Index.(*syntax.Word)
				if ok && index.Lit() == "*" {
					depends = true
					return false
				}
				return true
			})
			if depends {
				return true
			}
		}
	}
	return false
}

func leadingTildeName(word *syntax.Word) (string, bool) {
	if len(word.Parts) == 0 {
		return "", false
	}
	literal, ok := word.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(literal.Value, "~") {
		return "", false
	}
	name := strings.TrimPrefix(literal.Value, "~")
	if slash := strings.IndexByte(name, '/'); slash >= 0 {
		name = name[:slash]
	}
	return name, true
}

func wordHasRelativeGlob(s *State, word *syntax.Word) bool {
	if len(word.Parts) == 0 {
		return false
	}
	if literal, ok := word.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(literal.Value, "/") {
		return false
	}
	return wordMayExpandGlob(s, word)
}

func wordMayExpandGlob(s *State, word *syntax.Word) bool {
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, "*?[") {
				return true
			}
		case *syntax.ExtGlob:
			return true
		case *syntax.CmdSubst:
			result, exists := s.substitution(part)
			if exists {
				stdout, unresolved := result.stdout.Data()
				if !unresolved && strings.ContainsAny(string(stdout), "*?[") {
					return true
				}
			}
		case *syntax.ParamExp:
			if parameterMayExpandGlob(s, part) {
				return true
			}
		}
	}
	return false
}

func parameterMayExpandGlob(s *State, parameter *syntax.ParamExp) bool {
	if parameter == nil || parameter.Param == nil {
		return false
	}
	name := parameter.Param.Value
	if hostVariableUnknown(s, name) || variableHasUnknownData(s, name) {
		return false
	}
	if parameter.Excl || parameter.Index != nil || parameter.Slice != nil || parameter.Exp != nil {
		return false
	}
	value := (&stateEnvironment{state: s}).Get(name)
	if value.Kind == expand.Indexed && (name == "@" || name == "*") {
		for _, item := range value.List {
			if strings.ContainsAny(item, "*?[") {
				return true
			}
		}
		return false
	}
	return strings.ContainsAny(value.String(), "*?[")
}

func unmaskedWordCertainty(s *State, word *syntax.Word, trackData bool) valueCertainty {
	var certainty valueCertainty
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.CmdSubst:
			if trackData {
				if result, exists := s.substitution(node); exists {
					_, unresolved := result.stdout.Data()
					if unresolved {
						certainty |= certaintyDataUnknown
					}
				}
			}
			return false
		case *syntax.ProcSubst:
			if nodeHasHostUnknown(s, node) {
				certainty |= certaintyHostUnknown
			}
			return false
		case *syntax.ArithmExp:
			certainty |= unmaskedArithmeticCertainty(s, node.X, trackData)
			return false
		case *syntax.ParamExp:
			if node.Param != nil && hostVariableUnknown(s, node.Param.Value) {
				certainty |= certaintyHostUnknown
			}
			if trackData && parameterValueUnknown(s, node) {
				certainty |= certaintyDataUnknown
			}
			certainty |= unmaskedArithmeticCertainty(s, node.Index, trackData)
			if node.Slice != nil {
				certainty |= unmaskedArithmeticCertainty(s, node.Slice.Offset, trackData)
				certainty |= unmaskedArithmeticCertainty(s, node.Slice.Length, trackData)
			}
		}
		return certainty != (certaintyHostUnknown | certaintyDataUnknown)
	})
	return certainty
}

func arithmeticCertainty(s *State, expression syntax.ArithmExpr) valueCertainty {
	if expression == nil {
		return 0
	}
	restore := maskInactiveParameterWords(s, expression)
	defer restore()
	return unmaskedArithmeticCertainty(s, expression, stateHasUnknownData(s))
}

func unmaskedArithmeticCertainty(s *State, expression syntax.ArithmExpr, trackData bool) valueCertainty {
	if expression == nil {
		return 0
	}
	var certainty valueCertainty
	syntax.Walk(expression, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.CmdSubst:
			if trackData {
				if result, exists := s.substitution(node); exists {
					_, unresolved := result.stdout.Data()
					if unresolved {
						certainty |= certaintyDataUnknown
					}
				}
			}
			return false
		case *syntax.ProcSubst:
			if nodeHasHostUnknown(s, node) {
				certainty |= certaintyHostUnknown
			}
			return false
		case *syntax.ParamExp:
			if node.Param != nil && hostVariableUnknown(s, node.Param.Value) {
				certainty |= certaintyHostUnknown
			}
			if trackData && parameterValueUnknown(s, node) {
				certainty |= certaintyDataUnknown
			}
		case *syntax.Word:
			name := node.Lit()
			if base, _, arrayReference := splitArrayReferenceName(name); arrayReference {
				name = base
			}
			if name != "" && hostVariableUnknown(s, name) {
				certainty |= certaintyHostUnknown
			}
			if trackData && name != "" && variableHasUnknownData(s, name) {
				certainty |= certaintyDataUnknown
			}
		}
		return certainty != (certaintyHostUnknown | certaintyDataUnknown)
	})
	return certainty
}

// Process substitutions execute in a separate evaluation path. Preserve the
// existing host-dependency scan without treating their data as part of the
// containing word.
func nodeHasHostUnknown(s *State, root syntax.Node) bool {
	unknown := false
	syntax.Walk(root, func(node syntax.Node) bool {
		if _, substitution := node.(*syntax.CmdSubst); substitution {
			return false
		}
		if arithmetic, ok := node.(*syntax.ArithmExp); ok {
			if unmaskedArithmeticCertainty(s, arithmetic.X, false).hostUnknown() {
				unknown = true
			}
			return false
		}
		parameter, ok := node.(*syntax.ParamExp)
		if !ok || parameter.Param == nil {
			return !unknown
		}
		if hostVariableUnknown(s, parameter.Param.Value) ||
			unmaskedArithmeticCertainty(s, parameter.Index, false).hostUnknown() ||
			parameter.Slice != nil && (unmaskedArithmeticCertainty(s, parameter.Slice.Offset, false).hostUnknown() ||
				unmaskedArithmeticCertainty(s, parameter.Slice.Length, false).hostUnknown()) {
			unknown = true
			return false
		}
		return true
	})
	return unknown
}

type unknownValueError struct {
	description string
}

func (err *unknownValueError) Error() string {
	if err.description == "" {
		return "value depends on unresolved command output"
	}
	return fmt.Sprintf("%s depends on unresolved command output", err.description)
}

func newUnknownValueError(description string) error {
	return &unknownValueError{description: description}
}

func isUnknownValueError(err error) bool {
	var unknown *unknownValueError
	return errors.As(err, &unknown)
}

func (e *ExecutionContext) literalValueWithCertainty(s *State, word *syntax.Word) (string, bool, error) {
	if word == nil {
		return "", false, nil
	}
	certainty := wordCertainty(s, word)
	value, err := e.expandWordValue(s, word, expand.Literal)
	if err == nil && certainty.hostUnknown() {
		err = fmt.Errorf("word depends on host runtime state")
	}
	return value, certainty.dataUnknown(), err
}

func wordHasUnknownData(s *State, word *syntax.Word) bool {
	if word == nil || !stateHasUnknownData(s) {
		return false
	}
	return wordCertainty(s, word).dataUnknown()
}

func parameterValueUnknown(s *State, parameter *syntax.ParamExp) bool {
	if parameter == nil || parameter.Param == nil {
		return false
	}
	unknown, variable := parameterBaseUnknown(s, parameter)
	if !unknown || parameter.Exp == nil {
		return unknown
	}
	set := variable.IsSet()
	switch parameter.Exp.Op {
	case syntax.AlternateUnset:
		return false
	case syntax.DefaultUnset, syntax.ErrorUnset, syntax.AssignUnset:
		return set
	default:
		return true
	}
}

func parameterBaseUnknown(s *State, parameter *syntax.ParamExp) (bool, expand.Variable) {
	if parameter == nil || parameter.Param == nil {
		return false, expand.Variable{}
	}
	name := parameter.Param.Value
	environment := &stateEnvironment{state: s}
	variable := environment.Get(name)
	if resolvedName, resolved := variable.Resolve(environment); resolvedName != "" {
		name = resolvedName
		variable = resolved
	}
	return variableHasUnknownData(s, name), variable
}

func arithmHasUnknownData(s *State, expression syntax.ArithmExpr) bool {
	if expression == nil || !stateHasUnknownData(s) {
		return false
	}
	return arithmeticCertainty(s, expression).dataUnknown()
}

func arithmHasHostUnknown(s *State, expression syntax.ArithmExpr) bool {
	return arithmeticCertainty(s, expression).hostUnknown()
}

func wordHasHostUnknown(s *State, word *syntax.Word) bool {
	return wordCertainty(s, word).hostUnknown()
}

func hostVariableUnknown(s *State, name string) bool {
	switch name {
	case "?":
		_, unresolved := s.exitStatus.Data()
		return unresolved
	case "$":
		return true
	case "!":
		return s.backgroundPIDSet
	case "RANDOM", "SECONDS", "EPOCHSECONDS", "EPOCHREALTIME", "BASHPID", "PPID":
		return true
	case "UID", "EUID", "GID":
		return !s.vars.Get(name).IsSet()
	case "BASH_ARGC", "BASH_ARGV", "BASH_COMMAND", "BASH_EXECUTION_STRING", "BASH_LINENO", "BASH_SOURCE",
		"BASH_SUBSHELL", "BASH_VERSION", "BASH_VERSINFO", "BASHOPTS", "DIRSTACK", "FUNCNAME", "GROUPS",
		"HOSTNAME", "HOSTTYPE", "LINENO", "MACHTYPE", "OSTYPE", "SHLVL", "SRANDOM", "_":
		return !s.vars.Get(name).IsSet()
	default:
		return false
	}
}

func variableHasUnknownData(s *State, name string) bool {
	_, exitUnresolved := s.exitStatus.Data()
	_, pipelineUnresolved := s.pipelineStatuses.Data()
	return s.vars.isUnknown(name) || name == "?" && exitUnresolved || name == "PIPESTATUS" && pipelineUnresolved
}

func firstUnknownWord(s *State, words []*syntax.Word) (int, bool) {
	for index, word := range words {
		if wordHasUnknownData(s, word) {
			return index, true
		}
	}
	return 0, false
}

func unknownExportedVariables(s *State) []string {
	if len(s.vars.unknown) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.vars.unknown))
	for name := range s.vars.unknown {
		names = append(names, name)
	}
	sort.Strings(names)
	unknown := make([]string, 0, len(names))
	for _, name := range names {
		if s.vars.isUnknown(name) {
			value := s.vars.Get(name)
			if value.Exported && value.IsSet() {
				unknown = append(unknown, name)
			}
		}
	}
	return unknown
}

func validateUnknownParameterExpansions(s *State, node syntax.Node) error {
	if node == nil || !stateHasUnknownData(s) {
		return nil
	}
	restore := maskInactiveParameterWords(s, node)
	defer restore()
	var result error
	syntax.Walk(node, func(current syntax.Node) bool {
		if result != nil {
			return false
		}
		if _, substitution := current.(*syntax.CmdSubst); substitution {
			return false
		}
		parameter, ok := current.(*syntax.ParamExp)
		if !ok || parameter.Exp == nil {
			return true
		}
		switch parameter.Exp.Op {
		case syntax.DefaultUnsetOrNull, syntax.AlternateUnsetOrNull, syntax.AssignUnsetOrNull, syntax.ErrorUnsetOrNull:
			if unknown, _ := parameterBaseUnknown(s, parameter); unknown {
				result = newUnknownValueError("parameter expansion")
				return false
			}
		}
		return true
	})
	return result
}

func stateHasUnknownData(s *State) bool {
	_, exitUnresolved := s.exitStatus.Data()
	_, pipelineUnresolved := s.pipelineStatuses.Data()
	if len(s.vars.unknown) != 0 || exitUnresolved || pipelineUnresolved {
		return true
	}
	for _, frame := range s.substitutionFrames {
		for _, result := range frame.values {
			_, unresolved := result.stdout.Data()
			if unresolved {
				return true
			}
		}
	}
	return false
}
