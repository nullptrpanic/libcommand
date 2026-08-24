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
	for index, argument := range arguments {
		switch {
		case argument == "-T" || argument == "--upload-file":
			if index+1 < len(arguments) && arguments[index+1] != "" {
				return true
			}
		case strings.HasPrefix(argument, "--upload-file="):
			if strings.TrimPrefix(argument, "--upload-file=") != "" {
				return true
			}
		case strings.HasPrefix(argument, "-T") && len(argument) > len("-T"):
			return true
		case argument == "-d" || argument == "--data" || argument == "--data-ascii" || argument == "--data-binary" || argument == "--json":
			if index+1 < len(arguments) && curlDataReadsFile(arguments[index+1]) {
				return true
			}
		case strings.HasPrefix(argument, "-d") && len(argument) > len("-d"):
			if curlDataReadsFile(strings.TrimPrefix(argument, "-d")) {
				return true
			}
		case curlLongOptionReadsFile(argument, "--data") ||
			curlLongOptionReadsFile(argument, "--data-ascii") ||
			curlLongOptionReadsFile(argument, "--data-binary") ||
			curlLongOptionReadsFile(argument, "--json"):
			return true
		case argument == "-F" || argument == "--form":
			if index+1 < len(arguments) && curlFormReadsFile(arguments[index+1]) {
				return true
			}
		case strings.HasPrefix(argument, "-F") && len(argument) > len("-F"):
			if curlFormReadsFile(strings.TrimPrefix(argument, "-F")) {
				return true
			}
		case strings.HasPrefix(argument, "--form="):
			if curlFormReadsFile(strings.TrimPrefix(argument, "--form=")) {
				return true
			}
		case argument == "--data-urlencode":
			if index+1 < len(arguments) && curlURLEncodedDataReadsFile(arguments[index+1]) {
				return true
			}
		case strings.HasPrefix(argument, "--data-urlencode="):
			if curlURLEncodedDataReadsFile(strings.TrimPrefix(argument, "--data-urlencode=")) {
				return true
			}
		}
	}
	return false
}

func curlLongOptionReadsFile(argument, option string) bool {
	prefix := option + "="
	return strings.HasPrefix(argument, prefix) && curlDataReadsFile(strings.TrimPrefix(argument, prefix))
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
