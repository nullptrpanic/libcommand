package builtin

import (
	"context"
	"reflect"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestTestTruthTable(t *testing.T) {
	for _, test := range []struct {
		args []string
		want testTruthValue
	}{
		{nil, testFalse},
		{[]string{"value"}, testTrue},
		{[]string{"!", ""}, testTrue},
		{[]string{"-z", "value"}, testFalse},
		{[]string{"!", "-n", ""}, testTrue},
		{[]string{"x", "=", "x"}, testTrue},
		{[]string{"x", "!=", "y"}, testTrue},
		{[]string{"1", "-eq", "1"}, testTrue},
		{[]string{"1", "-ne", "2"}, testTrue},
		{[]string{"1", "-lt", "2"}, testTrue},
		{[]string{"1", "-le", "1"}, testTrue},
		{[]string{"2", "-gt", "1"}, testTrue},
		{[]string{"2", "-ge", "2"}, testTrue},
		{[]string{"anything", "unsupported", "value"}, testUnknown},
	} {
		if got, failure := testTruth(nil, test.args); failure != nil || got != test.want {
			t.Fatalf("test %#v = %v, %#v; want %v", test.args, got, failure, test.want)
		}
	}
	if got, failure := testTruth(nil, []string{"bad", "-eq", "1"}); got != testFalse || failure == nil || failure.exitCode != 2 {
		t.Fatalf("invalid numeric test = %v, %#v", got, failure)
	}
}

func TestTestFileOperators(t *testing.T) {
	var calls [][]string
	prepare := &runtime.CommandDefinition{
		Candidate: true,
		Command: func(_ context.Context, execution *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
			if err := execution.EnsureDirectory("/dir"); err != nil {
				return nil, err
			}
			return execution.Result(execution.Output().Build()), nil
		},
	}
	err := executeBuiltinScript(t,
		`printf x > /file; prepare; test -e /file && record file; test -d /dir && record dir; test -f /dir || record not-file; test -e /missing || record missing`,
		&runtime.Request{},
		map[string]*runtime.CommandDefinition{
			"prepare": prepare,
			"record":  recordingCommand(&calls, nil),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"file"}, {"dir"}, {"not-file"}, {"missing"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
