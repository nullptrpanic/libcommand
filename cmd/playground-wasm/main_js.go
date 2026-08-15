//go:build js && wasm

package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/nullptrpanic/libcommand"
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
			return `{"error":"libcommandAnalyze expects a JSON string and optional trace callback"}`
		}
		var stream func(*libcommand.TraceEvent)
		if len(arguments) == 2 {
			if arguments[1].Type() != js.TypeFunction {
				return `{"error":"libcommandAnalyze trace callback must be a function"}`
			}
			callback := arguments[1]
			stream = func(event *libcommand.TraceEvent) {
				encoded, err := json.Marshal(event)
				if err == nil {
					callback.Invoke(string(encoded))
				}
			}
		}
		return analyzeJSONWithTrace(arguments[0].String(), stream)
	})
	js.Global().Set("libcommandParse", parseFunction)
	js.Global().Set("libcommandAnalyze", analyzeFunction)
	select {}
}
