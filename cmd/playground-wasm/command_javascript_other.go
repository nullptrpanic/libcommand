//go:build !js || !wasm

package main

import (
	"errors"

	"github.com/nullptrpanic/libcommand"
)

func executeJavaScriptCommand(string, *libcommand.Invocation) (*libcommand.CommandResult, error) {
	return nil, errors.New("JavaScript command handlers require the browser Playground")
}
