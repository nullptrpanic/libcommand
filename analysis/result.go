package analysis

import (
	"context"
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand"
)

func commandResult(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation, reason string) (*libcommand.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, redirect := range shell.Redirects() {
		target := path.Clean(redirect.Target)
		if strings.HasPrefix(target, "/dev/tcp/") || strings.HasPrefix(target, "/dev/udp/") {
			reason = "network device redirection"
			break
		}
	}
	if reason != "" {
		return nil, &DetectionError{Command: invocation.Name, Reason: reason}
	}
	return &libcommand.CommandResult{Unresolved: true}, nil
}

func argumentRiskResult(
	ctx context.Context,
	shell *libcommand.CommandContext,
	invocation *libcommand.Invocation,
	risky func([]string) bool,
	reason string,
) (*libcommand.CommandResult, error) {
	arguments, concrete := concreteArguments(invocation.Args)
	if !concrete || !risky(arguments) {
		reason = ""
	}
	return commandResult(ctx, shell, invocation, reason)
}

func concreteArguments(arguments []*libcommand.Argument) ([]string, bool) {
	values := make([]string, len(arguments))
	for index, argument := range arguments {
		if argument.Kind != libcommand.ArgumentString {
			return nil, false
		}
		values[index] = argument.Value
	}
	return values, true
}
