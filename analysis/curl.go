package analysis

import (
	"context"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

// Curl reports curl invocations that upload local input.
func Curl(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return argumentRiskResult(ctx, shell, invocation, curlUploadsLocalInput, RiskTypeDataExfiltration)
}

func curlUploadsLocalInput(arguments []string) bool {
	risky := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			break
		}
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			continue
		}
		// Long options consume one value; clustered short options stop at the
		// first value-taking option. A value must never be scanned as an option.
		for offset := 1; offset < len(argument); offset++ {
			option, value, attached := "-"+argument[offset:offset+1], "", false
			if strings.HasPrefix(argument, "--") {
				option, value, attached = strings.Cut(argument, "=")
			} else if offset+1 < len(argument) {
				value, attached = argument[offset+1:], true
			}
			if option == "--help" || option == "--version" || option == "--manual" || option == "-h" || option == "-V" || option == "-M" {
				return false
			}
			takesValue := true
			switch option {
			case "--silent", "--show-error", "--fail", "--fail-with-body", "--location", "--insecure", "--compressed", "--verbose", "--get", "--head", "--globoff", "--http1.1", "--http2", "--http3", "--ipv4", "--ipv6", "--no-progress-meter", "--next":
				takesValue = false
			default:
				if len(option) == 2 && strings.ContainsRune("012346aBfGgIijkLlnNOpqRsSvZ#:JO", rune(option[1])) {
					takesValue = false
				}
			}
			if !takesValue {
				if strings.HasPrefix(argument, "--") {
					break
				}
				continue
			}
			if !attached {
				index++
				if index == len(arguments) {
					return false
				}
				value = arguments[index]
			}
			switch option {
			case "-T", "--upload-file":
				risky = risky || value != ""
			case "-d", "--data", "--data-ascii", "--data-binary", "--json":
				risky = risky || curlDataReadsFile(value)
			case "-F", "--form":
				risky = risky || curlFormReadsFile(value)
			case "--data-urlencode":
				risky = risky || curlURLEncodedDataReadsFile(value)
			}
			break
		}
	}
	return risky
}

func curlDataReadsFile(value string) bool {
	return len(value) > 1 && value[0] == '@'
}

func curlFormReadsFile(value string) bool {
	equals := strings.IndexByte(value, '=')
	if equals < 0 || equals+2 >= len(value) {
		return false
	}
	source := value[equals+1:]
	return source[0] == '@' || source[0] == '<'
}

func curlURLEncodedDataReadsFile(value string) bool {
	at := strings.IndexByte(value, '@')
	if at < 0 || at+1 >= len(value) {
		return false
	}
	equals := strings.IndexByte(value, '=')
	return equals < 0 || at < equals
}
