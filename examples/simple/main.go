package main

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/nullptrpanic/libcommand"
	"github.com/nullptrpanic/libcommand/internal/runtime"
)

//go:embed script.sh
var script string

func main() {
	simulator := libcommand.NewBuilder().
		Command("lark-cli", func(ctx context.Context, commandContext *libcommand.CommandContext, invocation *libcommand.Invocation) (*runtime.CommandResult, error) {
			fmt.Print(invocation.Name)
			for _, argument := range invocation.Args {
				fmt.Printf(" %q", argument.Value)
			}
			fmt.Print("\n")
			return &libcommand.CommandResult{}, nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: script}); err != nil {
		fmt.Print(err)
	}
}
