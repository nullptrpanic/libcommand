package builtin

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestReadSupportsDocumentedInputModes(t *testing.T) {
	var calls [][]string
	source := `
IFS=, read -r first rest <<< 'one,two,three'
record "$first" "$rest"
read -a values <<< 'zero one two'
record "${values[0]}" "${values[2]}"
read -d : token <<< 'prefix:suffix'
record "$token"
read -n 3 token <<< 'abcdef'
record "$token"
read -u 0 token <<< 'descriptor-zero'
record "$token"
read -p prompt token <<< 'prompt-value'
record "$token"
read -i initial token <<< 'initial-value'
record "$token"
read -u 2 token 2>/dev/null || record u-error
read -t 1 token 2>/dev/null || record timeout-error
read -Z token 2>/dev/null || record option-error
read -a 2>/dev/null || record array-usage
read -d 2>/dev/null || record delimiter-usage
read -n nope token 2>/dev/null || record numeric-usage
read bad-name <<< value 2>/dev/null || record invalid-name
read -a bad-name <<< value 2>/dev/null || record invalid-array
readonly fixed=old
read fixed <<< replacement 2>/dev/null
record readonly-name "$fixed" "$?"
`
	err := executeBuiltinScript(t, source, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"record": recordingCommand(&calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"one", "two,three"},
		{"zero", "two"},
		{"prefix"},
		{"abc"},
		{"descriptor-zero"},
		{"prompt-value"},
		{"initial-value"},
		{"u-error"},
		{"timeout-error"},
		{"option-error"},
		{"array-usage"},
		{"delimiter-usage"},
		{"numeric-usage"},
		{"invalid-name"},
		{"invalid-array"},
		{"readonly-name", "old", "0"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestGetoptsHandlesGroupedOptionsAndFailures(t *testing.T) {
	var calls [][]string
	source := `
set -- -abvalue -x -- tail
while getopts 'ab:' option; do
  record "$option" "${OPTARG-}" "$OPTIND"
done
OPTIND=1
getopts ':c:' option -c
record "$option" "${OPTARG-}" "$OPTIND"
OPTIND=1
getopts 'c:' option -c 2>/dev/null
record "$option" "${OPTARG-}" "$OPTIND"
OPTIND=1
getopts 'a' option -- || record end-marker "$OPTIND"
OPTIND=1
getopts 'a' option operand || record non-option "$OPTIND"
OPTIND=invalid
getopts 'a' option -a
record invalid-optind "$option" "$OPTIND"
getopts 'a' bad-name -a 2>/dev/null || record invalid-name
readonly fixed
getopts 'a' fixed -a 2>/dev/null || record readonly-name
getopts 2>/dev/null || record usage
`
	err := executeBuiltinScript(t, source, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"record": recordingCommand(&calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefixes := []string{"a", "b", "?", ":", "?", "end-marker", "non-option", "invalid-optind", "invalid-name", "readonly-name", "usage"}
	if len(calls) != len(wantPrefixes) {
		t.Fatalf("calls = %#v", calls)
	}
	for index, prefix := range wantPrefixes {
		if len(calls[index]) == 0 || calls[index][0] != prefix {
			t.Fatalf("call %d = %#v, want prefix %q; all calls=%#v", index, calls[index], prefix, calls)
		}
	}
}

func TestMapfileSupportsOffsetsLimitsAndValidation(t *testing.T) {
	var calls [][]string
	source := `
mapfile -t values <<< $'zero\none\ntwo'
record "${values[0]}:${values[2]}"
mapfile -t -s 1 -n 1 -O 3 values <<< $'skip\nplaced\nignored'
record "${values[0]}:${values[3]}"
mapfile -d : -t delimited <<< 'left:right:'
record "${delimited[0]}:${delimited[1]}"
mapfile -n 2>/dev/null || record missing-count
mapfile -O invalid 2>/dev/null || record bad-origin
mapfile -d 2>/dev/null || record missing-delimiter
mapfile -Z 2>/dev/null || record bad-option
mapfile bad-name 2>/dev/null || record invalid-name
readonly fixed
mapfile fixed 2>/dev/null || record readonly-name
`
	err := executeBuiltinScript(t, source, &runtime.Request{}, map[string]*runtime.CommandDefinition{
		"record": recordingCommand(&calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"zero:two"},
		{"zero:placed"},
		{"left:right"},
		{"missing-count"},
		{"bad-origin"},
		{"missing-delimiter"},
		{"bad-option"},
		{"invalid-name"},
		{"readonly-name"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestExternalWrappersPreserveNestedCommandSemantics(t *testing.T) {
	var calls []string
	probe := &runtime.CommandDefinition{
		Candidate: true,
		Command: func(_ context.Context, shell *runtime.CommandContext, invocation *runtime.Invocation) (*runtime.CommandResult, error) {
			argument := ""
			if len(invocation.Args) != 0 {
				argument = invocation.Args[0].Value
			}
			calls = append(calls, fmt.Sprintf("%s:%s:%s", shell.State().User(), invocation.Env["TOKEN"], argument))
			return shell.Result(shell.Output().Build()), nil
		},
	}
	source := `
sudo -u alice TOKEN=one probe sudo-short
sudo --user=bob TOKEN=two probe sudo-long
sudo -ucarol probe sudo-combined
sudo -AEnPS -- probe sudo-flags
probe restored
timeout --foreground -s TERM -k 1 5 probe timeout-separated
timeout --signal=KILL --kill-after=2 5 probe timeout-long
timeout -sTERM -k2 5 probe timeout-short
nice -n 5 probe nice-separated
nice --adjustment=3 probe nice-long
nice -4 probe nice-short
stdbuf -i 0 -oL --error=0 probe stdbuf
taskset --cpu-list 0 probe taskset
chrt --fifo 1 probe chrt
ionice --class 2 --classdata=4 probe ionice
setsid -fcw probe setsid
nohup -- probe nohup
`
	err := executeBuiltinScript(t, source, &runtime.Request{User: "user"}, map[string]*runtime.CommandDefinition{"probe": probe})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"alice:one:sudo-short",
		"bob:two:sudo-long",
		"carol::sudo-combined",
		"root::sudo-flags",
		"user::restored",
		"user::timeout-separated",
		"user::timeout-long",
		"user::timeout-short",
		"user::nice-separated",
		"user::nice-long",
		"user::nice-short",
		"user::stdbuf",
		"user::taskset",
		"user::chrt",
		"user::ionice",
		"user::setsid",
		"user::nohup",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestWrapperUnsupportedModesDoNotInvokeNestedCommands(t *testing.T) {
	tests := []string{
		"sudo --login record sudo",
		"timeout --unknown 1 record timeout",
		"nice --unknown record nice",
		"stdbuf --unknown record stdbuf",
		"taskset --pid 1 record taskset",
		"chrt --pid 1 record chrt",
		"ionice --pid 1 record ionice",
		"setsid --unknown record setsid",
		"nohup --unknown record nohup",
	}
	for _, source := range tests {
		t.Run(source, func(t *testing.T) {
			var calls [][]string
			_ = executeBuiltinScript(t, source, &runtime.Request{}, map[string]*runtime.CommandDefinition{
				"record": recordingCommand(&calls, nil),
			})
			if len(calls) != 0 {
				t.Fatalf("unsupported wrapper mode invoked nested commands: %#v", calls)
			}
		})
	}
}
