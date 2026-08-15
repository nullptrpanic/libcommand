package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nullptrpanic/libcommand/internal/materialize"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func TestBraceExpansionMaterialization(t *testing.T) {
	word := func(value string) *syntax.Word {
		return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
	}
	plain, ok := braceExpansionMaterialization(word("plain"), 0)
	if !ok || plain.count != 0 || plain.literalBytes != 0 {
		t.Fatalf("plain materialization = (%#v, %t)", plain, ok)
	}
	product, ok := braceExpansionMaterialization(word("{a,b}{1..3}"), 108)
	if !ok || product.count != 6 || product.literalBytes != 12 {
		t.Fatalf("product materialization = (%#v, %t)", product, ok)
	}
	if _, ok := braceExpansionMaterialization(word("{a,b}{1..3}"), 107); ok {
		t.Fatal("one byte over materialization was accepted")
	}
}

func TestAssignmentGroupChecksCancellationDuringProcessing(t *testing.T) {
	assignments := make([]*syntax.Assign, 257)
	for index := range assignments {
		assignments[index] = &syntax.Assign{
			Name:  &syntax.Lit{Value: "value" + strconv.Itoa(index)},
			Value: &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "x"}}},
		}
	}
	ctx := &cancelDuringCommandContext{}
	e, s := newNoOpExecutor(ctx, 10, &Request{})
	err := e.applyAssignments(s, assignments, expand.Unknown, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("applyAssignments() error = %v, want context.Canceled", err)
	}
	if s.vars.Get("value0").IsSet() {
		t.Fatal("canceled assignment group was partially committed")
	}
}

func TestPathMaterializationChecksCancellationDuringAccounting(t *testing.T) {
	ctx := &cancelDuringCommandContext{}
	e, s := newNoOpExecutor(ctx, 10, &Request{})
	path := &pathResult{state: s, status: StatusCompleted}
	err := e.checkPathsMaterialization([]*pathResult{path}, 0, unknownLocation)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("checkPathsMaterialization() error = %v, want context.Canceled", err)
	}
	if path.status != StatusIncomplete {
		t.Fatalf("path status = %d, want StatusIncomplete", path.status)
	}
}

func TestBraceExpansionMaterializationRejectsIntegerOverflow(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	minimumInt := -maximumInt - 1
	for _, value := range []string{
		"{" + strconv.Itoa(maximumInt-1) + ".." + strconv.Itoa(maximumInt) + "}",
		"{" + strconv.Itoa(minimumInt+1) + ".." + strconv.Itoa(minimumInt) + "}",
	} {
		word := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
		if result, ok := braceExpansionMaterialization(word, 128); ok || result != nil {
			t.Fatalf("braceExpansionMaterialization(%q) = (%#v, %t)", value, result, ok)
		}
	}
}

func TestBraceExpansionPreflightCountsStaticBytes(t *testing.T) {
	for _, test := range []struct {
		name              string
		value             string
		materializedBytes int
	}{
		{name: "literal prefix", value: "1234567890{a,b}", materializedBytes: 54},
		{name: "zero padded sequence", value: "{0000000001..0000000002}", materializedBytes: 52},
	} {
		t.Run(test.name, func(t *testing.T) {
			word := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: test.value}}}
			below := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: test.materializedBytes - 1}}
			if err := below.checkBraceExpansion(word, 0); err == nil {
				t.Fatal("brace expansion below its materialized size was accepted")
			}
			exact := &ExecutionContext{ctx: context.Background(), config: Config{MaxMemoryBytes: test.materializedBytes}}
			if err := exact.checkBraceExpansion(word, 0); err != nil {
				t.Fatalf("exact materialized size was rejected: %v", err)
			}
		})
	}
}

func TestMaterializedExpansionStopsBeforeDispatch(t *testing.T) {
	dispatches := 0
	err := Execute(context.Background(), parseForTest(t, `record {1..100}`, "fields.sh"), &Request{}, &Config{
		MaxExecutionSteps: 10,
		MaxMemoryBytes:    64, LookupCommand: lookupCommands(func(name string) bool {
			return name == "record"
		},
			func(context.Context, *State, *Invocation) (*CommandResult, error) {
				dispatches++
				return &CommandResult{}, nil
			}),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("Execute() error = %v", err)
	}
	if dispatches != 0 {
		t.Fatalf("dispatches = %d, want 0", dispatches)
	}
}

func TestMaterializedSparseArrayDoesNotCommit(t *testing.T) {
	paths, _, err := evaluateForTest(context.Background(), parseForTest(t, `value=old; value[10000]=x`, "array.sh"), &Request{}, &Config{
		MaxExecutionSteps: 10,
		MaxMemoryBytes:    256, LookupCommand: lookupAllCommands(noOpDispatch),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].status == StatusCompleted {
		t.Fatalf("paths = %#v", paths)
	}
	if got := paths[0].state.vars.Get("value"); got.Kind != expand.String || got.String() != "old" {
		t.Fatalf("value committed after failure: %#v", got)
	}
	if issue := paths[0].state.issue; issue == nil || !strings.Contains(issue.Error(), "maximum materialized byte count 256 reached") {
		t.Fatalf("issue = %v", issue)
	}
}

func TestAssignmentGroupStopsAtAggregateMaterializationLimit(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	e.config.MaxMemoryBytes = 128
	file := parseForTest(t, `first=1234567890123456789012345678901234567890 second=1234567890123456789012345678901234567890`, "assignments.sh")
	call := file.Stmts[0].Cmd.(*syntax.CallExpr)

	err := e.applyAssignments(s, call.Assigns, expand.Unknown, false)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 128 reached") {
		t.Fatalf("applyAssignments() error = %v", err)
	}
	for _, name := range []string{"first", "second"} {
		if s.vars.Get(name).IsSet() {
			t.Fatalf("%s committed after aggregate materialization failure", name)
		}
	}
}

func TestAggregateMaterializationFailureRestoresPrintfVariable(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	s.vars.put("value", expand.Variable{Set: true, Kind: expand.String, Str: "old"})
	s.vars.put("ballast", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("x", 80)})
	stateBytes, ok := stateMaterialization(s)
	if !ok {
		t.Fatal("initial state materialization overflowed")
	}
	e.config.MaxMemoryBytes = materialize.EntryBytes + stateBytes - s.initialBytes + 8

	file := parseForTest(t, `printf -v value '%40s' x`, "printf-aggregate.sh")
	paths, err := e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, file.Stmts)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count") {
		t.Fatalf("evaluateStatements() error = %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("path count = %d, want 1", len(paths))
	}
	if paths[0].status != StatusIncomplete {
		t.Fatalf("path status = %v, issue = %v, want %v", paths[0].status, paths[0].state.issue, StatusIncomplete)
	}
	if got := paths[0].state.vars.Get("value").String(); got != "old" {
		t.Fatalf("value = %q, want old", got)
	}
}

func TestRollbackLogCountsTowardMaterializationLimit(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 100, &Request{})
	e.config.MaxMemoryBytes = 512
	source := "value=old\n{\n" + strings.Repeat("printf -v value '%80s' x\n", 10) + "}\n"

	paths, err := e.evaluateStatements(
		[]*pathResult{{state: s, status: StatusCompleted}},
		parseForTest(t, source, "rollback-budget.sh").Stmts,
	)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 512 reached") {
		t.Fatalf("evaluateStatements() error = %v", err)
	}
	if len(paths) != 1 || paths[0].status != StatusIncomplete {
		t.Fatalf("paths = %#v, want one incomplete path", paths)
	}
}

func TestDeepBlocksAvoidQuadraticMaterialization(t *testing.T) {
	const depth = 15_000
	source := strings.Repeat("{ ", depth) + ":;" + strings.Repeat(" }", depth)
	file := parseForTest(t, source, "deep-blocks.sh")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e, s := newNoOpExecutor(ctx, 10, &Request{})

	paths, err := e.evaluateStatements([]*pathResult{{state: s, status: StatusCompleted}}, file.Stmts)
	if err != nil {
		t.Fatalf("evaluateStatements() error = %v", err)
	}
	if len(paths) != 1 || paths[0].status != StatusCompleted {
		t.Fatalf("paths = %#v, want one completed path", paths)
	}
}

func TestSubstitutionFrameMaterializationCache(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	assertSubstitutionMaterializationCache(t, s)

	s.pushSubstitutionFrame()
	assertSubstitutionMaterializationCache(t, s)
	substitution := &syntax.CmdSubst{}
	s.setSubstitution(substitution, &substitutionResult{stdout: newCertain([]byte("one")), exitStatus: newCertain(0)})
	assertSubstitutionMaterializationCache(t, s)
	s.setSubstitution(substitution, &substitutionResult{stdout: newCertain([]byte("replacement")), exitStatus: newCertain(0)})
	assertSubstitutionMaterializationCache(t, s)
	process := &syntax.ProcSubst{}
	s.setProcessSubstitution(process, "/.libcommand-process-1", true)
	assertSubstitutionMaterializationCache(t, s)

	cloned := s.clone()
	assertSubstitutionMaterializationCache(t, cloned)
	if outputs := s.takeProcessOutputs(); len(outputs) != 1 {
		t.Fatalf("process outputs = %#v, want one entry", outputs)
	}
	assertSubstitutionMaterializationCache(t, s)
	s.popSubstitutionFrame()
	assertSubstitutionMaterializationCache(t, s)
}

func TestStateMetadataMaterializationCaches(t *testing.T) {
	s := newState(&Request{Env: map[string]string{"VALUE": "original"}}, defaultMaxMemoryBytes)
	s.pushLocalScope()
	s.saveLocal("VALUE")
	s.saveLocal("MISSING")
	s.setTrap("ERR", "echo first")
	s.setTrap("EXIT", "echo exit")
	assertStateMetadataMaterializationCaches(t, s)

	s.setTrap("ERR", "echo replacement")
	s.deleteTrap("EXIT")
	clone := s.clone()
	assertStateMetadataMaterializationCaches(t, s)
	assertStateMetadataMaterializationCaches(t, clone)

	clone.popLocalScope()
	clone.deleteTrap("ERR")
	assertStateMetadataMaterializationCaches(t, clone)
}

func assertStateMetadataMaterializationCaches(t testing.TB, s *State) {
	t.Helper()
	wantScopes := 0
	for _, scope := range s.localScopes {
		wantScopes += materialize.EntryBytes
		for name, saved := range scope {
			wantScopes += variableEntryBytes(name, &saved.value)
			if saved.indexedSlots != nil {
				wantScopes += indexedSlotsBytes(name, saved.indexedSlots)
			}
		}
	}
	if s.localScopesBytes != wantScopes {
		t.Fatalf("local scope materialization = %d, want %d", s.localScopesBytes, wantScopes)
	}
	wantTraps := 0
	for signal, command := range s.traps {
		wantTraps += materialize.EntryBytes + len(signal) + len(command)
	}
	if s.trapsBytes != wantTraps {
		t.Fatalf("trap materialization = %d, want %d", s.trapsBytes, wantTraps)
	}
}

func TestCorrelatedFailureStateCountsTowardMaterializationLimit(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	failure := s.snapshotForUnknownFailure()
	failure.vars.put("retained", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("x", 256)})
	s.setUnknownExitCodeWithFailure(failure)
	e.config.MaxMemoryBytes = 128

	if err := e.checkStateMaterialization(s); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 128 reached") {
		t.Fatalf("checkStateMaterialization() error = %v", err)
	}
}

func assertSubstitutionMaterializationCache(t testing.TB, s *State) {
	t.Helper()
	total := 0
	for _, frame := range s.substitutionFrames {
		frameBytes := materialize.EntryBytes
		for _, result := range frame.values {
			frameBytes += substitutionResultBytes(result)
		}
		for _, path := range frame.processPaths {
			frameBytes += materialize.EntryBytes + len(path)
		}
		for path := range frame.processOutputs {
			frameBytes += materialize.EntryBytes + len(path)
		}
		if frame.materializedBytes != frameBytes {
			t.Fatalf("frame materialization = %d, want %d", frame.materializedBytes, frameBytes)
		}
		total += frameBytes
	}
	if s.substitutionBytes != total {
		t.Fatalf("substitution materialization = %d, want %d", s.substitutionBytes, total)
	}
}

func TestLiteralValueRejectsOversizedGlobalParameterReplacement(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	e.config.MaxMemoryBytes = 64
	s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("a", 32)})
	s.vars.put("Y", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("b", 32)})

	file := parseForTest(t, `value=${X//a/$Y}`, "replacement.sh")
	assignment := file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns[0]
	value, err := e.literalValue(s, assignment.Value)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("literalValue() = %q, %v", value, err)
	}
	if value != "" {
		t.Fatalf("literalValue() returned partial value %q", value)
	}
}

func TestLiteralValueRejectsOversizedQuotedPatternReplacement(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	e.config.MaxMemoryBytes = 64
	s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("*", 32)})
	s.vars.put("Y", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("b", 32)})

	file := parseForTest(t, `value=${X//\*/$Y}`, "quoted-pattern.sh")
	assignment := file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns[0]
	value, err := e.literalValue(s, assignment.Value)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("literalValue() = %q, %v", value, err)
	}
}

func TestLiteralValueRejectsOversizedReplacementMatchCollection(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	e.config.MaxMemoryBytes = 64
	s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: "aaaaa"})

	file := parseForTest(t, `value=${X//a/}`, "replacement-matches.sh")
	assignment := file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns[0]
	value, err := e.literalValue(s, assignment.Value)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("literalValue() = %q, %v", value, err)
	}
}

func TestReplacementMaterializationMatchesExpansion(t *testing.T) {
	tests := []struct {
		name   string
		source string
		value  string
	}{
		{name: "literal all", source: `value=${X//a/bb}`, value: "aaaa"},
		{name: "quoted metacharacter", source: `value=${X//\*/zz}`, value: "*x**"},
		{name: "glob all", source: `value=${X//a*/z}`, value: "abcabc"},
		{name: "empty-capable glob", source: `value=${X//*/z}`, value: "bbb"},
		{name: "unicode question", source: `value=${X//?/zz}`, value: "你好"},
		{name: "first only", source: `value=${X/a/bb}`, value: "aaaa"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e, s := newNoOpExecutor(context.Background(), 10, &Request{})
			e.config.MaxMemoryBytes = 1024
			s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: test.value})
			file := parseForTest(t, test.source, "replacement-size.sh")
			word := file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns[0].Value
			parameter := word.Parts[0].(*syntax.ParamExp)
			calculated, complete, err := e.parameterReplacementMaterialization(s, parameter)
			if err != nil || !complete {
				t.Fatalf("parameterReplacementMaterialization() = %d, %t, %v", calculated, complete, err)
			}
			actual, err := expand.Literal(e.expansionConfig(s), word)
			if err != nil {
				t.Fatal(err)
			}
			if calculated != len(actual) {
				t.Fatalf("calculated bytes = %d, actual %q (%d bytes)", calculated, actual, len(actual))
			}
		})
	}
}

func TestVariablesTrackRetainedMaterialization(t *testing.T) {
	values := newVariables(nil)
	initial := values.materializedBytes
	value := expand.Variable{Set: true, Kind: expand.String, Str: "abc"}

	values.put("x", value)
	if got, want := values.materializedBytes-initial, 37; got != want {
		t.Fatalf("known variable bytes = %d, want %d", got, want)
	}
	values.putUnknown("x", value)
	if got, want := values.materializedBytes-initial, 54; got != want {
		t.Fatalf("unknown variable bytes = %d, want %d", got, want)
	}

	clone := values.clone()
	clone.put("x", value)
	if got, want := clone.materializedBytes-initial, 37; got != want {
		t.Fatalf("known clone bytes = %d, want %d", got, want)
	}
	if got, want := values.materializedBytes-initial, 54; got != want {
		t.Fatalf("source bytes after clone mutation = %d, want %d", got, want)
	}
	clone.delete("x")
	if got, want := clone.materializedBytes-initial, 17; got != want {
		t.Fatalf("deleted variable version bytes = %d, want %d", got, want)
	}
}

func TestLiteralValueRejectsOversizedMultiPartWord(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	e.config.MaxMemoryBytes = 64
	s.vars.put("X", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("x", 40)})
	s.vars.put("Y", expand.Variable{Set: true, Kind: expand.String, Str: strings.Repeat("y", 40)})

	file := parseForTest(t, `value=$X$Y`, "word.sh")
	assignment := file.Stmts[0].Cmd.(*syntax.CallExpr).Assigns[0]
	value, err := e.literalValue(s, assignment.Value)
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 64 reached") {
		t.Fatalf("literalValue() = %q, %v", value, err)
	}
	if value != "" {
		t.Fatalf("literalValue() returned partial value %q", value)
	}
}

func TestAggregateActivePathsStopBeforeDispatch(t *testing.T) {
	dispatches := 0
	err := Execute(context.Background(), parseForTest(t, `
	printf -v value '%40s' x
	if unknown-command; then :; else :; fi
	record
	`, "paths.sh"), &Request{}, &Config{
		MaxExecutionSteps: 20,
		MaxMemoryBytes:    160, LookupCommand: lookupCommands(func(name string) bool {
			return name == "record"
		},
			func(context.Context, *State, *Invocation) (*CommandResult, error) {
				dispatches++
				return &CommandResult{}, nil
			}),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 160 reached") {
		t.Fatalf("Execute() error = %v", err)
	}
	if dispatches != 0 {
		t.Fatalf("dispatches = %d, want 0", dispatches)
	}
}

func TestAggregateActivePathsStopDuringPathGrowth(t *testing.T) {
	dispatches := 0
	stdout := []byte(strings.Repeat("x", 2048))
	err := Execute(context.Background(), parseForTest(t, `
if unknown-one; then :; else :; fi
if unknown-two; then :; else :; fi
if unknown-three; then :; else :; fi
inflate
`, "paths.sh"), &Request{}, &Config{
		MaxExecutionSteps: 100,
		MaxMemoryBytes:    2048, LookupCommand: lookupCommands(func(name string) bool {
			return name == "inflate"
		},
			func(context.Context, *State, *Invocation) (*CommandResult, error) {
				dispatches++
				return &CommandResult{Stdout: stdout}, nil
			}),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 2048 reached") {
		t.Fatalf("Execute() error = %v", err)
	}
	if dispatches != 1 {
		t.Fatalf("dispatches = %d, want 1", dispatches)
	}
}

func TestPathMaterializationCountsRetainedUnresolvedState(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	stateBytes, ok := stateMaterialization(s)
	if !ok || stateBytes < s.initialBytes {
		t.Fatalf("state materialization = %d, %t; initial = %d", stateBytes, ok, s.initialBytes)
	}
	e.config.MaxMemoryBytes = materialize.EntryBytes + stateBytes - s.initialBytes

	completed := &pathResult{state: s, status: StatusCompleted}
	if err := e.checkPathsMaterialization([]*pathResult{completed}, 0, unknownLocation); err != nil {
		t.Fatalf("one retained path: %v", err)
	}
	unresolved := &pathResult{state: s.clone(), status: StatusUnresolved}
	paths := []*pathResult{completed, unresolved}
	if err := e.checkPathsMaterialization(paths, 0, unknownLocation); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count") {
		t.Fatalf("two retained paths error = %v", err)
	}
}

func TestPathMaterializationCountsOnlyRetainedUnresolvedStates(t *testing.T) {
	e, s := newNoOpExecutor(context.Background(), 10, &Request{})
	stateBytes, ok := stateMaterialization(s)
	if !ok || stateBytes < s.initialBytes {
		t.Fatalf("state materialization = %d, %t; initial = %d", stateBytes, ok, s.initialBytes)
	}
	e.config.MaxMemoryBytes = materialize.EntryBytes + stateBytes - s.initialBytes

	first := &pathResult{state: s, status: StatusUnresolved}
	if err := e.checkPathsMaterialization([]*pathResult{first}, 0, unknownLocation); err != nil {
		t.Fatalf("one retained unresolved path: %v", err)
	}
	second := &pathResult{state: s.clone(), status: StatusUnresolved}
	paths := []*pathResult{first, second}
	if err := e.checkPathsMaterialization(paths, 0, unknownLocation); err == nil || !strings.Contains(err.Error(), "maximum materialized byte count") {
		t.Fatalf("two retained unresolved paths error = %v", err)
	}
}

func TestFunctionMaterializationTracksMutationsIncrementally(t *testing.T) {
	s := newState(&Request{}, defaultMaxMemoryBytes)
	before, ok := stateMaterialization(s)
	if !ok {
		t.Fatal("initial state materialization overflowed")
	}

	s.setFunction("alpha", &syntax.FuncDecl{})
	alphaBytes := materialize.EntryBytes + len("alpha")
	if s.functionsBytes != alphaBytes {
		t.Fatalf("function bytes after add = %d, want %d", s.functionsBytes, alphaBytes)
	}
	after, ok := stateMaterialization(s)
	if !ok || after-before != alphaBytes {
		t.Fatalf("state growth after function add = %d, %t; want %d", after-before, ok, alphaBytes)
	}

	s.setFunction("alpha", &syntax.FuncDecl{})
	s.deleteFunction("missing")
	if s.functionsBytes != alphaBytes {
		t.Fatalf("function bytes after overwrite and missing delete = %d, want %d", s.functionsBytes, alphaBytes)
	}

	clone := s.clone()
	clone.setFunction("beta", &syntax.FuncDecl{})
	betaBytes := materialize.EntryBytes + len("beta")
	if clone.functionsBytes != alphaBytes+betaBytes {
		t.Fatalf("clone function bytes after add = %d, want %d", clone.functionsBytes, alphaBytes+betaBytes)
	}
	if s.functionsBytes != alphaBytes {
		t.Fatalf("source function bytes changed through clone = %d, want %d", s.functionsBytes, alphaBytes)
	}
	clone.deleteFunction("alpha")
	if clone.functionsBytes != betaBytes {
		t.Fatalf("clone function bytes after delete = %d, want %d", clone.functionsBytes, betaBytes)
	}
}

func TestDynamicCandidateASTStopsAtAggregateMaterializationLimit(t *testing.T) {
	commandExists := func(name string) bool { return name == "lark-cli" }
	e, s := newNoOpExecutor(context.Background(), 100, &Request{})
	e.config.MaxMemoryBytes = 1024
	e.config.LookupCommand = lookupCommands(commandExists, noOpDispatch)
	index, err := buildCandidateIndex(context.Background(), parseForTest(t, `:`, "initial.sh"), commandExists)
	if err != nil {
		t.Fatal(err)
	}
	e.candidates = index

	var limitErr error
	successfulParses := 0
	for range 64 {
		previousBytes := index.dynamicBytes
		paths, evaluateErr := e.evaluateSourceText(s, `retained() { lark-cli; }`, "eval")
		if evaluateErr != nil {
			limitErr = evaluateErr
			break
		}
		if len(paths) != 1 || paths[0].status != StatusCompleted {
			t.Fatalf("paths = %#v", paths)
		}
		if index.dynamicBytes <= previousBytes {
			t.Fatalf("dynamic candidate bytes did not accumulate: before=%d after=%d", previousBytes, index.dynamicBytes)
		}
		successfulParses++
		s = paths[0].state
	}
	if successfulParses == 0 {
		t.Fatal("first dynamic parse did not fit within the configured limit")
	}
	if limitErr == nil || !strings.Contains(limitErr.Error(), "maximum materialized byte count 1024 reached") {
		t.Fatalf("dynamic candidate error = %v", limitErr)
	}
}
