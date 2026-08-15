package builtin

import (
	"context"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func init() {
	for _, name := range []string{
		"alias", "bind", "caller", "compgen", "complete", "compopt", "dirs",
		"disown", "enable", "fc", "hash", "help", "history", "jobs", "kill",
		"logout", "popd", "pushd", "suspend", "times", "ulimit", "umask", "unalias",
	} {
		registerCommand(name, executeUnsupported)
	}
}

func executeUnsupported(_ context.Context, execution *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
	return nil, nil
}
