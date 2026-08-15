//go:build js && wasm

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall/js"

	"github.com/nullptrpanic/libcommand"
)

type javascriptCommandResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error"`
}

func executeJavaScriptCommand(registrationName string, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
	encodedInvocation, err := json.Marshal(invocation)
	if err != nil {
		return nil, fmt.Errorf("encode JavaScript command invocation: %w", err)
	}
	encodedResult := js.Global().Get("libcommandExecuteCommand").Invoke(registrationName, string(encodedInvocation)).String()
	if len(encodedResult) > maximumPlaygroundOutputBytes {
		return nil, fmt.Errorf("JavaScript command result exceeds %d bytes", maximumPlaygroundOutputBytes)
	}
	response := &javascriptCommandResponse{}
	if err = json.Unmarshal([]byte(encodedResult), response); err != nil {
		return nil, fmt.Errorf("decode JavaScript command result: %w", err)
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	if response.ExitCode < 0 || response.ExitCode > 255 {
		return nil, fmt.Errorf("JavaScript command exit code must be between 0 and 255")
	}
	return &libcommand.CommandResult{
		Stdout:   []byte(response.Stdout),
		Stderr:   []byte(response.Stderr),
		ExitCode: response.ExitCode,
	}, nil
}
