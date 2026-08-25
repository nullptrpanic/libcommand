package runtime

import (
	"reflect"
	"testing"
)

func TestLoopVariantsPreserveIterationAndControlTransfers(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		request  *Request
		wantArgs [][]string
	}{
		{
			name:     "arithmetic for continue and break",
			source:   `for ((i=0; i<5; i++)); do if ((i == 1)); then continue; fi; cmd "$i"; if ((i == 3)); then break; fi; done`,
			request:  &Request{},
			wantArgs: [][]string{{"0"}, {"2"}, {"3"}},
		},
		{
			name:     "arithmetic for without condition or post",
			source:   `for ((i=0;;)); do cmd once; break; done`,
			request:  &Request{},
			wantArgs: [][]string{{"once"}},
		},
		{
			name:     "while continue and break",
			source:   `i=0; while ((i < 5)); do ((i++)); if ((i == 2)); then continue; fi; cmd "$i"; if ((i == 4)); then break; fi; done`,
			request:  &Request{},
			wantArgs: [][]string{{"1"}, {"3"}, {"4"}},
		},
		{
			name:     "until enters until condition succeeds",
			source:   `i=0; until ((i == 2)); do cmd "$i"; ((i++)); done`,
			request:  &Request{},
			wantArgs: [][]string{{"0"}, {"1"}},
		},
		{
			name:     "select retries invalid input",
			source:   `select value in red green blue; do [[ -n "$value" ]] || continue; cmd "$REPLY" "$value"; break; done`,
			request:  &Request{Stdin: []byte("invalid\n2\n")},
			wantArgs: [][]string{{"2", "green"}},
		},
		{
			name:     "select exits at eof",
			source:   `select value in red green; do cmd never; done; cmd after`,
			request:  &Request{},
			wantArgs: [][]string{{"after"}},
		},
		{
			name:     "empty word loop skips body",
			source:   `for value in; do cmd never; done; cmd after`,
			request:  &Request{},
			wantArgs: [][]string{{"after"}},
		},
		{
			name:     "readonly loop variable rejects iteration",
			source:   `readonly value=old; for value in new; do cmd never; done; cmd "$value"`,
			request:  &Request{},
			wantArgs: [][]string{{"old"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, calls, err := runBash(t, test.source, test.request, nil)
			if err != nil {
				t.Fatal(err)
			}
			requirePathStatus(t, paths, StatusCompleted)
			got := make([][]string, len(calls))
			for index, call := range calls {
				got[index] = call.args
			}
			if !reflect.DeepEqual(got, test.wantArgs) {
				t.Fatalf("arguments = %#v, want %#v", got, test.wantArgs)
			}
		})
	}
}

func TestCaseFallthroughModesMatchBashOrdering(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		wantArgs [][]string
	}{
		{
			name: "forced fallthrough executes next body",
			source: `case x in
x) cmd first ;&
y) cmd second ;;
esac`,
			wantArgs: [][]string{{"first"}, {"second"}},
		},
		{
			name: "test-next fallthrough evaluates later patterns",
			source: `case xy in
x*) cmd first ;;&
z*) cmd never ;;
*y) cmd second ;;
esac`,
			wantArgs: [][]string{{"first"}, {"second"}},
		},
		{
			name: "multiple patterns and default",
			source: `case other in
one|two) cmd never ;;
*) cmd default ;;
esac`,
			wantArgs: [][]string{{"default"}},
		},
		{
			name:     "no case match continues successfully",
			source:   `case none in one) cmd never;; esac; cmd after`,
			wantArgs: [][]string{{"after"}},
		},
		{
			name:     "command substitution in pattern",
			source:   `case value in "$(echo value)") cmd matched;; esac`,
			wantArgs: [][]string{{"matched"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, calls, err := runBash(t, test.source, &Request{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			requirePathStatus(t, paths, StatusCompleted)
			got := make([][]string, len(calls))
			for index, call := range calls {
				got[index] = call.args
			}
			if !reflect.DeepEqual(got, test.wantArgs) {
				t.Fatalf("arguments = %#v, want %#v", got, test.wantArgs)
			}
		})
	}
}

func TestRedirectionVariantsPreserveStreamsAndVirtualFiles(t *testing.T) {
	source := `
cmd input < missing
cmd here <<'EOF'
document
EOF
cmd string <<< 'text'
cmd first > output
cmd second >> output
cmd errors 2> errors
cmd merged > merged 2>&1
cmd both &> both
cmd append &>> both
cmd null > /dev/null 2>&1
cmd external > /proc/example
`
	paths, calls, err := runBash(t, source, &Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := mustPath(t, paths)
	if len(calls) != 11 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].stdin != "" || calls[1].stdin != "document\n" || calls[2].stdin != "text\n" {
		t.Fatalf("stdin = %#v, %#v, %#v", calls[0].stdin, calls[1].stdin, calls[2].stdin)
	}
	checks := map[string]string{
		"/output": "cmd:first\ncmd:second\n",
		"/errors": "",
		"/merged": "cmd:merged\n",
		"/both":   "cmd:both\ncmd:append\n",
	}
	for name, want := range checks {
		contents, unknown, exists := path.state.fs.readFile(name)
		if !exists || unknown || string(contents) != want {
			t.Fatalf("file %s = %q, unknown=%v, exists=%v; want %q", name, contents, unknown, exists, want)
		}
	}
	if contents, unknown, exists := path.state.fs.readFile("/dev/null"); !exists || unknown || len(contents) != 0 {
		t.Fatalf("/dev/null = %q, unknown=%v, exists=%v", contents, unknown, exists)
	}
}

func TestProcessSubstitutionConnectsNestedStreams(t *testing.T) {
	paths, calls, err := runBash(t,
		`cmd source > >(cmd sink); cmd input < <(echo generated); cmd "$(echo substitution)"`,
		&Request{}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	requirePathStatus(t, paths, StatusCompleted)
	if len(calls) != 4 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[1].args[0] != "sink" || calls[1].stdin != "cmd:source\n" || calls[2].stdin != "generated\n" || !reflect.DeepEqual(calls[3].args, []string{"substitution"}) {
		t.Fatalf("calls = %#v", calls)
	}
}
