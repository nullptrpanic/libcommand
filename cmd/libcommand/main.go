package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nullptrpanic/libcommand"
	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/syntax"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("libcommand", flag.ContinueOnError)
	flags.SetOutput(stderr)
	script := flags.String("script", "", "Bash script file or directory of .sh files")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *script == "" {
		fmt.Fprintln(stderr, "-script is required")
		return 2
	}

	simulator := libcommand.NewBuilder().Command("lark-cli",
		func(_ context.Context, _ *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
			words := make([]string, 1, len(invocation.Args)+1)
			words[0] = invocation.Name
			for _, argument := range invocation.Args {
				if argument.Kind == libcommand.ArgumentString {
					words = append(words, argument.Value)
					continue
				}
				words = append(words, "<unresolved>")
			}
			quoted := make([]string, len(words))
			for index, word := range words {
				value, quoteErr := syntax.Quote(word, syntax.LangBash)
				if quoteErr != nil {
					return nil, fmt.Errorf("quote argument %d: %w", index, quoteErr)
				}
				quoted[index] = value
			}
			if _, writeErr := fmt.Fprintln(stdout, strings.Join(quoted, " ")); writeErr != nil {
				return nil, writeErr
			}
			return &libcommand.CommandResult{}, nil
		},
	).Build()

	paths, err := scriptPaths(*script)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	exitCode := 0
	for _, path := range paths {
		if err := simulateFile(simulator, path); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			exitCode = 1
		}
	}
	return exitCode
}

func scriptPaths(name string) ([]string, error) {
	return scriptPathsWithinLimit(name, materialize.DefaultMaxBytes)
}

func scriptPathsWithinLimit(name string, maximum int) ([]string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, fmt.Errorf("resolve script path %q: %w", name, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect script path %q: %w", name, err)
	}
	if !info.IsDir() {
		return []string{absolute}, nil
	}

	directory, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("read script directory %q: %w", name, err)
	}
	defer directory.Close()

	paths := make([]string, 0)
	materializedBytes := 0
	for {
		entries, readErr := directory.Readdir(128)
		for _, entry := range entries {
			var ok bool
			materializedBytes, ok = materialize.Add(materializedBytes, materialize.EntryBytes+len(entry.Name()), maximum)
			if !ok {
				return nil, materialize.LimitError(maximum)
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".sh" {
				continue
			}
			materializedBytes, ok = materialize.Add(materializedBytes, len(absolute)+1, maximum)
			if !ok {
				return nil, materialize.LimitError(maximum)
			}
			paths = append(paths, filepath.Join(absolute, entry.Name()))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read script directory %q: %w", name, readErr)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func simulateFile(simulator *libcommand.Simulator, name string) error {
	file, err := os.Open(name)
	if err != nil {
		return fmt.Errorf("read script: %w", err)
	}
	defer file.Close()

	source, err := readScript(file, materialize.DefaultMaxBytes)
	if err != nil {
		return fmt.Errorf("read script: %w", err)
	}
	err = simulator.Simulate(context.Background(), &libcommand.SimulationRequest{
		Source: source,
	})
	return err
}

func readScript(reader io.Reader, maximum int) (string, error) {
	source, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return "", err
	}
	if len(source) > maximum {
		return "", materialize.LimitError(maximum)
	}
	return string(source), nil
}
