package builtin

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nullptrpanic/libcommand/internal/runtime"
)

func TestEchoOptionAndEscapeMatrix(t *testing.T) {
	newline, escapes, remaining := parseEchoOptions([]string{"-ne", "-E", "value"})
	if newline || escapes || !reflect.DeepEqual(remaining, []string{"value"}) {
		t.Fatalf("parseEchoOptions() = %v, %v, %#v", newline, escapes, remaining)
	}
	newline, escapes, remaining = parseEchoOptions([]string{"-invalid", "value"})
	if !newline || escapes || !reflect.DeepEqual(remaining, []string{"-invalid", "value"}) {
		t.Fatalf("invalid options = %v, %v, %#v", newline, escapes, remaining)
	}

	input := `\a\b\e\E\f\n\r\t\v\\\x41\xZ\0101\09\qtail\`
	value, stop := decodeEchoEscapes(input)
	want := "\a\b\x1b\x1b\f\n\r\t\v\\A\\xZA\x009\\qtail\\"
	if stop || value != want {
		t.Fatalf("decodeEchoEscapes() = %q, %v; want %q", value, stop, want)
	}
	value, stop = decodeEchoEscapes(`before\cafter`)
	if value != "before" || !stop {
		t.Fatalf("escape c = %q, %v", value, stop)
	}
}

func TestEchoCancellationUnresolvedAndSeparatorLimits(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := executeEcho(cancelled, &runtime.Invocation{}, 64); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled echo = %#v, %v", output, err)
	}
	output, err := executeEcho(context.Background(), &runtime.Invocation{Args: []*runtime.Argument{{Kind: runtime.ArgumentUnresolved}}}, 64)
	if err != nil || output == nil {
		t.Fatalf("unresolved echo = %#v, %v", output, err)
	}
	if _, unresolved := output.Stdout.Data(); !unresolved {
		t.Fatal("unresolved echo stdout was marked resolved")
	}
	if output, err := executeEcho(context.Background(), &runtime.Invocation{Args: []*runtime.Argument{{Kind: runtime.ArgumentString, Value: "a"}, {Kind: runtime.ArgumentString, Value: "b"}}}, 2); output != nil || err == nil {
		t.Fatalf("separator-limited echo = %#v, %v", output, err)
	}
}

func TestPrintfWidthAndStringHelpers(t *testing.T) {
	for _, test := range []*struct {
		format  string
		maximum int
		want    bool
	}{
		{format: `\%999s`, maximum: 8, want: true},
		{format: `%% %8s`, maximum: 8, want: true},
		{format: `%9s`, maximum: 8},
		{format: `%999999999999999999999s`, maximum: 8},
		{format: `%4s%5c`, maximum: 8},
		{format: `%4d%4x`, maximum: 8, want: true},
		{format: `%`, maximum: 8, want: true},
	} {
		if got := printfWidthWithinLimit(test.format, test.maximum); got != test.want {
			t.Fatalf("printfWidthWithinLimit(%q, %d) = %v, want %v", test.format, test.maximum, got, test.want)
		}
	}

	if got := adjustedPrintfStringWidth("", "value", false); got != "" {
		t.Fatalf("empty adjusted width = %q", got)
	}
	if got := adjustedPrintfStringWidth("invalid", "value", false); got != "invalid" {
		t.Fatalf("invalid adjusted width = %q", got)
	}
	if got := adjustedPrintfStringWidth("1", "你", false); got != "0" {
		t.Fatalf("small UTF-8 adjusted width = %q", got)
	}
	if got := adjustedPrintfStringWidth("5", "你", true); got != "03" {
		t.Fatalf("zero-padded UTF-8 adjusted width = %q", got)
	}
	if got, stop := truncatePrintfBEscapeC(`a\\b`); got != `a\\b` || stop {
		t.Fatalf("ordinary %%b value = %q, %v", got, stop)
	}
	if got, stop := truncatePrintfBEscapeC(`a\cb`); got != "a" || !stop {
		t.Fatalf("truncated %%b value = %q, %v", got, stop)
	}
	if got, err := bashPrintfQuote(""); got != "''" || err != nil {
		t.Fatalf("empty quote = %q, %v", got, err)
	}
	if got, err := bashPrintfQuote("safe/value"); got != "safe/value" || err != nil {
		t.Fatalf("safe quote = %q, %v", got, err)
	}
	if got, err := bashPrintfQuote("space value"); got != `space\ value` || err != nil {
		t.Fatalf("escaped quote = %q, %v", got, err)
	}
	if got, err := bashPrintfQuote(string([]byte{0xff})); got == "" || err != nil {
		t.Fatalf("non-printable quote = %q, %v", got, err)
	}
}

func TestPreparePrintfFormatMatrix(t *testing.T) {
	var checked bool
	probe := &runtime.CommandDefinition{
		Command: func(_ context.Context, shell *runtime.CommandContext, _ *runtime.Invocation) (*runtime.CommandResult, error) {
			prepared, args, consumed, warnings, stop, err := preparePrintfFormat(shell, `%*s|%.*s|%q|%c|%b|%%|%z|%`, []string{"-5", "value", "2", "你好", "a b", "char", `before\cafter`})
			if err != nil {
				t.Fatal(err)
			}
			if consumed != 7 || len(warnings) != 0 || !stop || prepared != "%-5s|%s|%s|%s|%s" {
				t.Fatalf("prepared format = %q, args %#v, consumed %d, warnings %#v, stop %v", prepared, args, consumed, warnings, stop)
			}
			if len(args) != 5 || args[2] != `a\ b` || args[3] != "c" || args[4] != "before" {
				t.Fatalf("prepared args = %#v", args)
			}

			_, _, _, warnings, _, err = preparePrintfFormat(shell, `%*s`, []string{"bad", "value"})
			if err != nil || !reflect.DeepEqual(warnings, []string{"bad"}) {
				t.Fatalf("invalid width = %#v, %v", warnings, err)
			}
			_, _, _, _, _, err = preparePrintfFormat(shell, `%.2d`, []string{"1"})
			if err == nil {
				t.Fatal("numeric precision was accepted")
			}
			checked = true
			return shell.Result(shell.Output().Build()), nil
		},
	}
	if err := executeBuiltinScript(t, "probe", &runtime.Request{}, map[string]*runtime.CommandDefinition{"probe": probe}); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("printf probe was not invoked")
	}
}

func TestReadEscapeAndSeparatorHelpers(t *testing.T) {
	value, escaped := decodeReadBackslashes(`one\ two\\three`)
	if value != `one two\three` || len(escaped) != len(value) || !escaped[3] || !escaped[7] {
		t.Fatalf("decodeReadBackslashes() = %q, %#v", value, escaped)
	}
	set, err := newReadSeparatorSet(context.Background(), "éé,", 128)
	if err != nil || len(set) != 2 {
		t.Fatalf("separator set = %#v, %v", set, err)
	}
	if _, err := newReadSeparatorSet(context.Background(), "é", 1); err == nil {
		t.Fatal("oversized separator set was accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newReadSeparatorSet(cancelled, "x", 128); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled separator set = %v", err)
	}
}
