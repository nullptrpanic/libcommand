//go:build js && wasm

package main

import (
	"encoding/json"
	"syscall/js"
)

func main() {
	parseFunction := js.FuncOf(func(_ js.Value, arguments []js.Value) any {
		if len(arguments) != 1 || arguments[0].Type() != js.TypeString {
			return `{"error":"libcommandParse expects one JSON string"}`
		}
		return parseSourceJSON(arguments[0].String())
	})
	analyzeFunction := js.FuncOf(func(_ js.Value, arguments []js.Value) any {
		if len(arguments) < 1 || len(arguments) > 2 || arguments[0].Type() != js.TypeString {
			return `{"error":"libcommandAnalyze expects a JSON string and optional stream callback"}`
		}
		var stream func(*playgroundStreamItem)
		if len(arguments) == 2 {
			if arguments[1].Type() != js.TypeFunction {
				return `{"error":"libcommandAnalyze stream callback must be a function"}`
			}
			callback := arguments[1]
			stream = func(item *playgroundStreamItem) {
				encoded, err := json.Marshal(item)
				if err == nil {
					callback.Invoke(string(encoded))
				}
			}
		}
		return analyzeJSONWithStream(arguments[0].String(), stream)
	})
	js.Global().Set("libcommandParse", parseFunction)
	js.Global().Set("libcommandAnalyze", analyzeFunction)
	select {}
}
