package runtime

import (
	"errors"
	iofs "io/fs"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestRedirectionFileHelpers(t *testing.T) {
	state := newState(&Request{}, defaultMaxMemoryBytes)
	if err := prepareOutputRedirection(state, "/nested/file", false); err != nil {
		t.Fatalf("prepare nested output: %v", err)
	}
	if contents, unresolved, exists := state.fs.readFile("/nested/file"); !exists || unresolved || len(contents) != 0 {
		t.Fatalf("nested output = %q, unresolved=%v, exists=%v", contents, unresolved, exists)
	}
	if err := state.fs.ensureDir("/directory"); err != nil {
		t.Fatal(err)
	}
	if err := prepareOutputRedirection(state, "/directory", false); err == nil || err.Error() != "/directory: Is a directory" {
		t.Fatalf("directory redirection error = %v", err)
	}

	input := &descriptorTarget{input: newCertain([]byte("contents")), inputFile: "/nested/file"}
	truncateDescriptorInput(nil, "/nested/file")
	truncateDescriptorInput(input, "/other")
	if contents, _ := input.input.Data(); string(contents) != "contents" {
		t.Fatalf("unrelated truncate changed input to %q", contents)
	}
	truncateDescriptorInput(input, "/nested/file")
	if contents, unresolved := input.input.Data(); unresolved || len(contents) != 0 {
		t.Fatalf("matching truncate = %q, unresolved=%v", contents, unresolved)
	}

	for _, filename := range []string{"/dev/tcp/host/80", "/dev/udp/host/53"} {
		if !isExternalDevicePath(filename) {
			t.Fatalf("%q is not external", filename)
		}
	}
	if isExternalDevicePath("/dev/null") {
		t.Fatal("/dev/null is external")
	}
}

func TestOutputRedirectionErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "missing", err: &iofs.PathError{Op: "open", Path: "/path", Err: iofs.ErrNotExist}, want: "/path: No such file or directory"},
		{name: "directory", err: &iofs.PathError{Op: "open", Path: "/path", Err: errIsDirectory}, want: "/path: Is a directory"},
		{name: "not directory", err: &iofs.PathError{Op: "open", Path: "/path", Err: errNotDirectory}, want: "/path: Not a directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outputRedirectionError("/path", test.err); got == nil || got.Error() != test.want {
				t.Fatalf("outputRedirectionError() = %v, want %q", got, test.want)
			}
		})
	}
	original := errors.New("write failed")
	if got := outputRedirectionError("/path", original); got != original {
		t.Fatalf("unclassified error = %v", got)
	}
}

func TestRedirectDescriptorDefaultsAndLiteral(t *testing.T) {
	tests := []struct {
		name        string
		redirection *syntax.Redirect
		want        int
		wantErr     bool
	}{
		{name: "input", redirection: &syntax.Redirect{Op: syntax.RdrIn}, want: 0},
		{name: "here string", redirection: &syntax.Redirect{Op: syntax.WordHdoc}, want: 0},
		{name: "output", redirection: &syntax.Redirect{Op: syntax.RdrOut}, want: 1},
		{name: "explicit", redirection: &syntax.Redirect{N: &syntax.Lit{Value: "7"}, Op: syntax.RdrOut}, want: 7},
		{name: "named", redirection: &syntax.Redirect{N: &syntax.Lit{Value: "fd"}, Op: syntax.RdrOut}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := redirectFD(test.redirection)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("redirectFD() = %d, %v", got, err)
			}
		})
	}

	execution, state := newNoOpExecutor(t.Context(), 10, &Request{})
	value, unresolved, err := execution.redirectLiteralValue(state, nil)
	if err != nil || unresolved || value != "" {
		t.Fatalf("nil literal = %q, unresolved=%v, err=%v", value, unresolved, err)
	}
}
