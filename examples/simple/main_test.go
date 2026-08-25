package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestExampleMainRunsEmbeddedScript(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	main()
	os.Stdout = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "lark-cli") {
		t.Fatalf("example output = %q", output)
	}
}
