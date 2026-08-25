package main

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/nullptrpanic/libcommand"
)

//go:embed script.sh
var script string

func main() {
	simulator := libcommand.NewBuilder().
		Command("lark-cli", func(_ context.Context, command *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			fmt.Print(invocation.Name)
			for _, argument := range invocation.Args {
				fmt.Printf(" %q", argument.Value)
			}
			fmt.Print("\n")
			return command.Result(command.Output().Build()), nil
		}).
		Build()

	if err := simulator.Simulate(context.Background(), &libcommand.SimulationRequest{Source: script}); err != nil {
		fmt.Print(err)
	}
}
