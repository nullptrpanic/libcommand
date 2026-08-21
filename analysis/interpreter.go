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
