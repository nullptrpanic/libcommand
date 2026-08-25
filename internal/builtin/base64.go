package builtin

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	register("base64", executeBase64Command)
}

func executeBase64Command(ctx context.Context, command *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
	output, err := executeBase64(ctx, invocation, command.MaxMemoryBytes())
	if err != nil {
		return nil, err
	}
	return command.Result(output), nil
}

func executeBase64(ctx context.Context, invocation *runtime.Invocation, maximum int) (*runtime.CommandOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	decode := false
	options := true
	for _, argument := range invocation.Args {
		if argument.Kind != runtime.ArgumentString {
			return unresolvedCommandOutput(), nil
		}
		if options {
			switch argument.Value {
			case "--":
				options = false
				continue
			case "-d", "--decode", "-D":
				decode = true
				continue
			}
			if strings.HasPrefix(argument.Value, "-") {
				return commandOutput(nil, []byte(fmt.Sprintf("base64: unsupported option %q\n", argument.Value)), 1), nil
			}
		}
		return commandOutput(nil, []byte("base64: file operands are not supported\n"), 1), nil
	}
	if invocation.Unresolved != nil && invocation.Unresolved.Stdin {
		return unresolvedCommandOutput(), nil
	}

	if decode {
		decodedLength := base64DecodedLength(invocation.Stdin)
		if _, ok := materialize.Add(0, decodedLength, maximum); !ok {
			return nil, materialize.LimitError(maximum)
		}
		stdout := make([]byte, decodedLength)
		written, err := base64.StdEncoding.Decode(stdout, invocation.Stdin)
		if err != nil {
			return commandOutput(nil, []byte("base64: invalid input\n"), 1), nil
		}
		return commandOutput(stdout[:written], nil, 0), nil
	}
	if len(invocation.Stdin) == 0 {
		return commandOutput(nil, nil, 0), nil
	}
	encodedLength := base64.StdEncoding.EncodedLen(len(invocation.Stdin))
	resultLength, ok := materialize.Add(encodedLength, 1, maximum)
	if !ok {
		return nil, materialize.LimitError(maximum)
	}
	stdout := make([]byte, resultLength)
	base64.StdEncoding.Encode(stdout[:encodedLength], invocation.Stdin)
	stdout[encodedLength] = '\n'
	return commandOutput(stdout, nil, 0), nil
}

func base64DecodedLength(input []byte) int {
	length := 0
	for _, value := range input {
		if value != '\r' && value != '\n' {
			length++
		}
	}
	for index := len(input) - 1; index >= 0; index-- {
		switch input[index] {
		case '\r', '\n':
			continue
		case '=':
			length--
			continue
		}
		break
	}
	return base64.RawStdEncoding.DecodedLen(length)
}
