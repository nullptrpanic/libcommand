package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func init() {
	registerCommand("printf", executePrintf)
}

func executePrintf(ctx context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	args, concrete := concreteArguments(invocation)
	if !concrete {
		arguments := invocation.Args
		if len(arguments) == 0 || arguments[0].Kind != runtime.ArgumentString || arguments[0].Value != "-v" {
			return unresolvedCommandResult(shell), nil
		}
		return unresolvedStderrCommandResult(shell, 1), nil
	}
	variableName := ""
	if len(args) >= 2 && args[0] == "-v" {
		variableName = args[1]
		args = args[2:]
	} else if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if variableName != "" {
		if !syntax.ValidName(variableName) {
			return commandResult(shell, nil, []byte(fmt.Sprintf("printf: `%s': not a valid identifier\n", variableName)), 2), nil
		}
		if shell.Variable(variableName).ReadOnly {
			return commandResult(shell, nil, []byte(fmt.Sprintf("printf: %s: readonly variable\n", variableName)), 1), nil
		}
	}
	if len(args) == 0 {
		return commandResult(shell, nil, []byte("printf: missing format\n"), 2), nil
	}
	format, remaining := args[0], args[1:]
	var output strings.Builder
	var diagnostics strings.Builder
	exitCode := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		preparedFormat, preparedArgs, consumed, warnings, stop, err := preparePrintfFormat(shell, format, remaining)
		if err != nil {
			return commandResult(shell, nil, []byte(fmt.Sprintf("printf: %v\n", err)), 1), nil
		}
		for _, warning := range warnings {
			diagnostic := fmt.Sprintf("printf: %s: invalid number\n", warning)
			total, ok := materialize.Add(output.Len(), diagnostics.Len(), shell.MaxMemoryBytes())
			if ok {
				_, ok = materialize.Add(total, len(diagnostic), shell.MaxMemoryBytes())
			}
			if !ok {
				return nil, materialize.LimitError(shell.MaxMemoryBytes())
			}
			diagnostics.WriteString(diagnostic)
			exitCode = 1
		}
		if !printfWidthWithinLimit(preparedFormat, shell.MaxMemoryBytes()) {
			return nil, materialize.LimitError(shell.MaxMemoryBytes())
		}
		value, err := shell.Format(preparedFormat, preparedArgs)
		if err != nil {
			return commandResult(shell, nil, []byte(fmt.Sprintf("printf: %v\n", err)), 1), nil
		}
		if total, ok := materialize.Add(output.Len(), diagnostics.Len(), shell.MaxMemoryBytes()); !ok {
			return nil, materialize.LimitError(shell.MaxMemoryBytes())
		} else if _, ok := materialize.Add(total, len(value), shell.MaxMemoryBytes()); !ok {
			return nil, materialize.LimitError(shell.MaxMemoryBytes())
		}
		output.WriteString(value)
		remaining = remaining[consumed:]
		if stop || consumed == 0 || len(remaining) == 0 {
			break
		}
	}
	if variableName != "" {
		shell.RecordVariableRollback(variableName)
		value := &expand.Variable{Set: true, Kind: expand.String, Str: output.String()}
		if err := shell.AssignVariable(variableName, value, false); err != nil {
			// Identifier and readonly failures were handled before formatting.
			// A materialization failure must not become an ordinary exit 1.
			return nil, err
		}
		return commandResult(shell, nil, []byte(diagnostics.String()), exitCode), nil
	}
	return commandResult(shell, []byte(output.String()), []byte(diagnostics.String()), exitCode), nil
}

func printfWidthWithinLimit(format string, maximum int) bool {
	totalWidth := 0
	for index := 0; index < len(format); index++ {
		if format[index] == '\\' {
			index++
			continue
		}
		if format[index] != '%' {
			continue
		}
		index++
		if index >= len(format) || format[index] == '%' {
			continue
		}
		for index < len(format) && (format[index] == '+' || format[index] == '-' || format[index] == ' ') {
			index++
		}
		width := 0
		for index < len(format) && format[index] >= '0' && format[index] <= '9' {
			digit := int(format[index] - '0')
			if width > (maximum-digit)/10 {
				return false
			}
			width = width*10 + digit
			index++
		}
		if width > maximum {
			return false
		}
		if index < len(format) && strings.ContainsRune("csbdiuox", rune(format[index])) {
			var ok bool
			totalWidth, ok = materialize.Add(totalWidth, width, maximum)
			if !ok {
				return false
			}
		}
	}
	return true
}

func preparePrintfFormat(shell *runtime.CommandContext, format string, args []string) (string, []string, int, []string, bool, error) {
	var prepared strings.Builder
	prepared.Grow(len(format))
	preparedArgs := make([]string, 0, len(args))
	var warnings []string
	stop := false
	argumentIndex := 0
	for index := 0; index < len(format); {
		if format[index] == '\\' && index+1 < len(format) {
			prepared.WriteString(format[index : index+2])
			index += 2
			continue
		}
		if format[index] != '%' {
			prepared.WriteByte(format[index])
			index++
			continue
		}

		index++
		if index >= len(format) {
			prepared.WriteByte('%')
			break
		}
		if format[index] == '%' {
			prepared.WriteString("%%")
			index++
			continue
		}

		flagsStart := index
		for index < len(format) && (format[index] == '+' || format[index] == '-' || format[index] == ' ') {
			index++
		}
		flags := format[flagsStart:index]
		widthText := ""
		if index < len(format) && format[index] == '*' {
			width := int64(0)
			if argumentIndex < len(args) {
				argument := args[argumentIndex]
				argumentIndex++
				if argument != "" {
					parsed, recognized, err := shell.ParseInteger(argument)
					if err != nil || !recognized {
						warnings = append(warnings, argument)
					} else {
						width = parsed
					}
				}
			}
			widthText = strconv.FormatInt(width, 10)
			if width < 0 {
				widthText = strconv.FormatUint(uint64(-(width+1))+1, 10)
			}
			if width < 0 && !strings.ContainsRune(flags, '-') {
				flags += "-"
			}
			index++
		} else {
			widthStart := index
			for index < len(format) && format[index] >= '0' && format[index] <= '9' {
				index++
			}
			widthText = format[widthStart:index]
		}
		precision := -1
		if index < len(format) && format[index] == '.' {
			index++
			if index < len(format) && format[index] == '*' {
				precision = 0
				if argumentIndex < len(args) {
					argument := args[argumentIndex]
					argumentIndex++
					parsed, recognized, err := shell.ParseInteger(argument)
					if err != nil || !recognized {
						warnings = append(warnings, argument)
					} else if parsed >= 0 && parsed <= int64(^uint(0)>>1) {
						precision = int(parsed)
					} else if parsed < 0 {
						precision = -1
					}
				}
				index++
			} else {
				precisionStart := index
				for index < len(format) && format[index] >= '0' && format[index] <= '9' {
					index++
				}
				precision = 0
				if text := format[precisionStart:index]; text != "" {
					parsed, err := strconv.ParseUint(text, 10, 0)
					if err == nil {
						precision = int(parsed)
					}
				}
			}
		}
		if index >= len(format) {
			prepared.WriteByte('%')
			prepared.WriteString(flags)
			prepared.WriteString(widthText)
			break
		}
		conversion := format[index]
		index++
		if !strings.ContainsRune("qcsbdiuox", rune(conversion)) {
			prepared.WriteByte('%')
			prepared.WriteString(flags)
			prepared.WriteString(widthText)
			prepared.WriteByte(conversion)
			continue
		}
		argument := ""
		if argumentIndex < len(args) {
			argument = args[argumentIndex]
			argumentIndex++
		}
		if strings.ContainsRune("diuox", rune(conversion)) {
			if precision >= 0 {
				return "", nil, 0, nil, false, fmt.Errorf("precision for %%%c is not supported", conversion)
			}
			normalized, valid := normalizePrintfNumber(shell, argument)
			if !valid {
				warnings = append(warnings, argument)
			}
			argument = normalized
		} else {
			switch conversion {
			case 'q':
				var err error
				argument, err = bashPrintfQuote(argument)
				if err != nil {
					return "", nil, 0, nil, false, err
				}
			case 'c':
				if argument != "" {
					argument = argument[:1]
				}
			case 'b':
				var err error
				argument, stop = truncatePrintfBEscapeC(argument)
				argument, err = shell.Format("%b", []string{argument})
				if err != nil {
					return "", nil, 0, nil, false, err
				}
			}
			if precision >= 0 && precision < len(argument) {
				argument = argument[:precision]
			}
			widthText = adjustedPrintfStringWidth(widthText, argument, conversion == 'c' && strings.HasPrefix(widthText, "0"))
			conversion = 's'
		}
		if !stop || argument != "" {
			prepared.WriteByte('%')
			prepared.WriteString(flags)
			prepared.WriteString(widthText)
			prepared.WriteByte(conversion)
			preparedArgs = append(preparedArgs, argument)
		}
		if stop {
			break
		}
	}
	return prepared.String(), preparedArgs, argumentIndex, warnings, stop, nil
}

func normalizePrintfNumber(shell *runtime.CommandContext, value string) (string, bool) {
	if value == "" {
		return "0", true
	}
	if (value[0] == '\'' || value[0] == '"') && len(value) > 1 {
		return strconv.FormatInt(int64(value[1]), 10), true
	}
	number, recognized, err := shell.ParseInteger(value)
	if err != nil || !recognized {
		return "0", false
	}
	return strconv.FormatInt(number, 10), true
}

func adjustedPrintfStringWidth(widthText, value string, zeroPad bool) string {
	if widthText == "" {
		return ""
	}
	width, err := strconv.ParseUint(widthText, 10, 0)
	if err != nil {
		return widthText
	}
	extraBytes := len(value) - utf8.RuneCountInString(value)
	if width <= uint64(extraBytes) {
		return "0"
	}
	adjusted := strconv.FormatUint(width-uint64(extraBytes), 10)
	if zeroPad {
		return "0" + adjusted
	}
	return adjusted
}

func bashPrintfQuote(value string) (string, error) {
	if value == "" {
		return "''", nil
	}
	var quoted strings.Builder
	for _, character := range value {
		if !unicode.IsPrint(character) || character == utf8.RuneError {
			return syntax.Quote(value, syntax.LangBash)
		}
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_@%+=:,./-", character) {
			quoted.WriteRune(character)
			continue
		}
		quoted.WriteByte('\\')
		quoted.WriteRune(character)
	}
	return quoted.String(), nil
}

func truncatePrintfBEscapeC(value string) (string, bool) {
	for index := 0; index+1 < len(value); index++ {
		if value[index] != '\\' {
			continue
		}
		if value[index+1] == 'c' {
			return value[:index], true
		}
		index++
	}
	return value, false
}
