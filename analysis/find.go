package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Find detects deletion rooted at the filesystem root.
func Find(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	riskType := RiskType("")
	if concrete && findDeletesRoot(arguments, invocation.Dir, directoryUnresolved(invocation)) {
		riskType = RiskTypeDestructiveOperation
	}
	return commandResult(ctx, shell, invocation, riskType)
}

func findDeletesRoot(arguments []string, directory string, directoryUnknown bool) bool {
	searchPaths, expression := findSearchPaths(arguments)
	if !findReachableDelete(expression) {
		return false
	}
	if len(searchPaths) == 0 {
		searchPaths = []string{"."}
	}
	for _, searchPath := range searchPaths {
		resolved, known := resolvedPath(directory, searchPath, directoryUnknown)
		if known && resolved == "/" {
			return true
		}
	}
	return false
}

func findSearchPaths(arguments []string) ([]string, []string) {
	paths := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--":
			continue
		case argument == "-D":
			index++
			continue
		case argument == "-H" || argument == "-L" || argument == "-P" || strings.HasPrefix(argument, "-O"):
			continue
		case argument == "!" || argument == "(" || argument == ")" || argument == "," || strings.HasPrefix(argument, "-"):
			return paths, arguments[index:]
		default:
			paths = append(paths, argument)
		}
	}
	return paths, nil
}

// Predicates retain both possible truth values; a deletion is charged only if
// evaluation can reach it. The iterative operator stack also bounds Go stack
// usage for caller-supplied nested expressions. This is not a filesystem walk.
type findOutcome struct{ yes, no, deletion bool }

func findReachableDelete(arguments []string) bool {
	var values []*findOutcome
	var operators []string
	precedence := func(operator string) int {
		switch operator {
		case "!":
			return 4
		case "-a":
			return 3
		case "-o":
			return 2
		case ",":
			return 1
		}
		return 0
	}
	apply := func() bool {
		operator := operators[len(operators)-1]
		operators = operators[:len(operators)-1]
		if len(values) == 0 {
			return false
		}
		right := values[len(values)-1]
		if operator == "!" {
			right.yes, right.no = right.no, right.yes
			return true
		}
		if len(values) < 2 {
			return false
		}
		left := values[len(values)-2]
		values = values[:len(values)-1]
		switch operator {
		case "-a":
			left.deletion = left.deletion || left.yes && right.deletion
			left.no, left.yes = left.no || left.yes && right.no, left.yes && right.yes
		case "-o":
			left.deletion = left.deletion || left.no && right.deletion
			left.yes, left.no = left.yes || left.no && right.yes, left.no && right.no
		case ",":
			continues := left.yes || left.no
			left.deletion = left.deletion || continues && right.deletion
			left.yes, left.no = continues && right.yes, continues && right.no
		default:
			return false
		}
		return true
	}
	pushOperator := func(operator string) bool {
		for len(operators) > 0 && precedence(operators[len(operators)-1]) >= precedence(operator) {
			if !apply() {
				return false
			}
		}
		operators = append(operators, operator)
		return true
	}
	operand := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch argument {
		case "-and":
			argument = "-a"
		case "-or":
			argument = "-o"
		case "-not":
			argument = "!"
		}
		switch argument {
		case "-a", "-o", ",":
			if !operand || !pushOperator(argument) {
				return false
			}
			operand = false
			continue
		case ")":
			if !operand {
				return false
			}
			for len(operators) > 0 && operators[len(operators)-1] != "(" {
				if !apply() {
					return false
				}
			}
			if len(operators) == 0 {
				return false
			}
			operators = operators[:len(operators)-1]
			continue
		}
		if operand && !pushOperator("-a") {
			return false
		}
		operand = false
		if argument == "(" || argument == "!" {
			operators = append(operators, argument)
			continue
		}
		value := &findOutcome{yes: true, no: true}
		switch argument {
		case "-true", "-print", "-print0", "-prune", "-depth", "-xdev", "-mount", "-noleaf", "-ignore_readdir_race":
			value.no = false
		case "-false":
			value.yes = false
		case "-quit":
			value.yes, value.no = false, false
		case "-delete":
			value.deletion = true
		case "-exec", "-execdir", "-ok", "-okdir":
			for index++; index < len(arguments) && arguments[index] != ";" && arguments[index] != "+"; index++ {
			}
			if index == len(arguments) {
				return false
			}
			if arguments[index] == "+" && (argument == "-exec" || argument == "-execdir") {
				value.no = false
			}
		case "-name", "-iname", "-path", "-ipath", "-regex", "-iregex", "-type", "-xtype", "-size", "-user", "-group", "-uid", "-gid", "-perm", "-links", "-inum", "-samefile", "-newer", "-mtime", "-mmin", "-atime", "-amin", "-ctime", "-cmin", "-printf", "-fprintf", "-fprint", "-fprint0", "-maxdepth", "-mindepth", "-regextype":
			switch argument {
			case "-printf", "-fprintf", "-fprint", "-fprint0", "-maxdepth", "-mindepth", "-regextype":
				value.no = false
			}
			index++
			if index == len(arguments) {
				return false
			}
			if argument == "-fprintf" {
				index++
				if index == len(arguments) {
					return false
				}
			}
		case "-empty", "-readable", "-writable", "-executable", "-nouser", "-nogroup":
		default:
			return false // Unknown grammar is not evidence of a reachable delete.
		}
		values = append(values, value)
		operand = true
	}
	if !operand {
		return false
	}
	for len(operators) > 0 {
		if !apply() {
			return false
		}
	}
	return len(values) == 1 && values[0].deletion
}
