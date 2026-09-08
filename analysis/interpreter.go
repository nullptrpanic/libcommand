package analysis

import "strings"

func shortOptionPayload(arguments []string, option string) (string, bool) {
	for index, argument := range arguments {
		if argument == "--" {
			return "", false
		}
		if argument == option {
			if index+1 == len(arguments) {
				return "", false
			}
			return arguments[index+1], true
		}
		if strings.HasPrefix(argument, option) && len(argument) > len(option) {
			return argument[len(option):], true
		}
	}
	return "", false
}

func containsShellExecutable(payload string) bool {
	payload = strings.ToLower(payload)
	for _, name := range []string{"/bin/bash", "/bin/sh", "cmd.exe", "powershell"} {
		if strings.Contains(payload, name) {
			return true
		}
	}
	return false
}

// Mask literals and line comments without changing byte offsets. A detector
// can then match call sites in code and inspect only that call's original
// argument text. Interpolation, aliases and arbitrary interpreter execution
// remain outside this deliberately conservative lexical detector.
func executableInterpreterText(payload string) string {
	code := []byte(payload)
	for index, value := range code {
		if value >= 'A' && value <= 'Z' {
			code[index] = value + ('a' - 'A')
		}
	}
	for index := 0; index < len(code); {
		start := index
		if code[index] == '#' {
			for index < len(code) && code[index] != '\n' {
				index++
			}
		} else if code[index] == '\'' || code[index] == '"' {
			quote := code[index]
			width := 1
			if index+2 < len(code) && code[index+1] == quote && code[index+2] == quote {
				width = 3
			}
			index += width
			for index < len(code) {
				if code[index] == '\\' {
					index += 2
					continue
				}
				if code[index] == quote && (width == 1 || index+2 < len(code) && code[index+1] == quote && code[index+2] == quote) {
					index += width
					break
				}
				index++
			}
		} else {
			index++
			continue
		}
		index = min(index, len(code))
		for offset := start; offset < index; offset++ {
			if code[offset] != '\n' {
				code[offset] = ' '
			}
		}
	}
	return string(code)
}

func interpreterCallsShell(payload, code, call string) bool {
	for offset := 0; offset < len(code); {
		index := strings.Index(code[offset:], call)
		if index < 0 {
			return false
		}
		start := offset + index + len(call)
		depth := 1
		end := start
		for end < len(code) && depth != 0 {
			switch code[end] {
			case '(':
				depth++
			case ')':
				depth--
			}
			end++
		}
		if depth == 0 && containsShellExecutable(payload[start:end-1]) {
			return true
		}
		offset = start
	}
	return false
}
