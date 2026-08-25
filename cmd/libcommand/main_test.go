package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand"
	"github.com/nullptrpanic/libcommand/internal/materialize"
)

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read failure")
}

type failingWriter struct{}

func (*failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failure")
}

func TestRunScript(t *testing.T) {
	script := filepath.Join(t.TempDir(), "single.sh")
	if err := os.WriteFile(script, []byte(`
chat_id=oc_1
for text in "hello world" done; do
  lark-cli im +messages-send --chat-id "$chat_id" --text "$text"
done
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"-script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
	want := "lark-cli im +messages-send --chat-id oc_1 --text 'hello world'\n" +
		"lark-cli im +messages-send --chat-id oc_1 --text 'done'\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRendersUnresolvedArgument(t *testing.T) {
	script := filepath.Join(t.TempDir(), "unknown.sh")
	if err := os.WriteFile(script, []byte(`for value in $(unknown); do lark-cli "$value"; done`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"-script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
	if want := "lark-cli '<unresolved>'\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunDirectoryContinuesAfterScriptError(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"01-first.sh":  `lark-cli first`,
		"02-broken.sh": `if true; then`,
		"03-last.sh":   `lark-cli last "two words"`,
		"ignored.txt":  `lark-cli ignored`,
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "04-nested.sh"), 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"-script", directory}, &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
	if want := "lark-cli first\nlark-cli last 'two words'\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if output := stderr.String(); !strings.Contains(output, "02-broken.sh") || !strings.Contains(output, "parse bash") {
		t.Fatalf("stderr = %q", output)
	}
}

func TestReadScriptStopsAtMaterializationLimit(t *testing.T) {
	source, err := readScript(strings.NewReader("1234"), 4)
	if err != nil || source != "1234" {
		t.Fatalf("exact limit source = %q, error = %v", source, err)
	}

	source, err = readScript(strings.NewReader("12345ignored"), 4)
	if source != "" || err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 4 reached") {
		t.Fatalf("over limit source = %q, error = %v", source, err)
	}
}

func TestScriptPathsStopsAtMaterializationLimit(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "script.sh"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := scriptPathsWithinLimit(directory, 1)
	if paths != nil || err == nil || !strings.Contains(err.Error(), "maximum materialized byte count 1 reached") {
		t.Fatalf("paths = %#v, error = %v", paths, err)
	}
}

func TestRunReportsUsageAndPathErrors(t *testing.T) {
	tests := []*struct {
		name     string
		args     []string
		wantCode int
		wantText string
	}{
		{name: "invalid flag", args: []string{"-unknown"}, wantCode: 2, wantText: "flag provided but not defined"},
		{name: "missing script", wantCode: 2, wantText: "-script is required"},
		{name: "missing path", args: []string{"-script", filepath.Join(t.TempDir(), "missing.sh")}, wantCode: 1, wantText: "inspect script path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(test.args, io.Discard, &stderr); code != test.wantCode || !strings.Contains(stderr.String(), test.wantText) {
				t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestRunReportsHandlerOutputFailure(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(script, []byte("lark-cli value"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run([]string{"-script", script}, &failingWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "write failure") {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
}

func TestScriptAndReaderErrorPaths(t *testing.T) {
	if source, err := readScript(&failingReader{}, 16); source != "" || err == nil || err.Error() != "read failure" {
		t.Fatalf("readScript() = %q, %v", source, err)
	}
	if err := simulateFile(libcommand.NewBuilder().Build(), filepath.Join(t.TempDir(), "missing.sh")); err == nil || !strings.Contains(err.Error(), "read script") {
		t.Fatalf("simulateFile() = %v", err)
	}

	directory := t.TempDir()
	name := "script.sh"
	if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	maximum := materialize.EntryBytes + len(name)
	if paths, err := scriptPathsWithinLimit(directory, maximum); paths != nil || err == nil || !strings.Contains(err.Error(), "maximum materialized byte count") {
		t.Fatalf("scriptPathsWithinLimit() = %#v, %v", paths, err)
	}
}
