package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nullptrpanic/libcommand"
)

func TestNetworkRedirectionStateMachine(t *testing.T) {
	tests := []*struct {
		name      string
		redirects []*libcommand.Redirect
		want      bool
	}{
		{
			name:      "network input",
			redirects: []*libcommand.Redirect{{FD: 0, Operator: "<", Target: "/dev/tcp/host/80"}},
			want:      true,
		},
		{
			name: "copied network descriptor",
			redirects: []*libcommand.Redirect{
				{FD: 3, Operator: "<", Target: "/dev/udp/host/53"},
				{FD: 0, Operator: "<&", Target: "3"},
			},
			want: true,
		},
		{
			name: "combined output does not feed input",
			redirects: []*libcommand.Redirect{
				{FD: 1, Operator: "&>", Target: "/dev/tcp/host/80"},
			},
		},
		{
			name: "invalid output descriptor clears aliases",
			redirects: []*libcommand.Redirect{
				{FD: 1, Operator: ">", Target: "/dev/tcp/host/80"},
				{FD: 1, Operator: ">&", Target: "closed"},
			},
		},
		{
			name: "unresolved target",
			redirects: []*libcommand.Redirect{
				{FD: 0, Operator: "<&", Target: "/dev/tcp/host/80", Unresolved: true},
			},
		},
		{
			name:      "direct duplicated network input",
			redirects: []*libcommand.Redirect{{FD: 0, Operator: "<&", Target: "/dev/tcp/host/80"}},
			want:      true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := networkFeedsStdin(test.redirects); got != test.want {
				t.Fatalf("networkFeedsStdin() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDetectorHelperNonRiskAndCancellationPaths(t *testing.T) {
	if systemctlPowerAction([]string{"status"}) || pythonReverseShellRisk(nil) || perlReverseShellRisk(nil) {
		t.Fatal("benign or empty invocations were classified as risky")
	}
	if !netcatExecRisk([]string{"--exec=/bin/sh"}) || !informationalRequest([]string{"--version"}) {
		t.Fatal("explicit detector option was not recognized")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := commandResult(cancelled, nil, &libcommand.Invocation{}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled commandResult() = %v", err)
	}
}

func TestObservationSkipsUnusablePaths(t *testing.T) {
	current := &observation{facts: &pathFacts{}}
	ctx := context.WithValue(context.Background(), observationContextKey{}, current)
	argument := func(value string) *libcommand.Argument {
		return &libcommand.Argument{Kind: libcommand.ArgumentString, Value: value}
	}
	observeMkfifo(ctx, nil, &libcommand.Invocation{Args: []*libcommand.Argument{argument(strings.Repeat("x", maximumTrackedPathSize+1))}})
	observeMkfifo(ctx, nil, &libcommand.Invocation{
		Args:       []*libcommand.Argument{argument("relative")},
		Unresolved: &libcommand.InvocationUnresolved{Dir: true},
	})
	observeCat(ctx, nil, &libcommand.Invocation{Args: []*libcommand.Argument{argument("one"), argument("two")}})
	observeCat(ctx, nil, &libcommand.Invocation{
		Args:       []*libcommand.Argument{argument("relative")},
		Unresolved: &libcommand.InvocationUnresolved{Dir: true},
	})
	if current.facts.fifos != nil || current.facts.stream.stage != streamNone {
		t.Fatalf("unusable paths changed facts: %#v", current.facts)
	}
}

func TestInteractiveShellOptionBoundaries(t *testing.T) {
	for _, arguments := range [][]string{{"-i"}, {"--norc", "-i"}, {"-ix"}} {
		if !interactiveShellReadsInput(arguments) {
			t.Fatalf("interactiveShellReadsInput(%#v) = false", arguments)
		}
	}
	for _, arguments := range [][]string{{"-ic", "command"}, {"--", "-i"}, {"-", "-i"}, {"script", "-i"}} {
		if interactiveShellReadsInput(arguments) {
			t.Fatalf("interactiveShellReadsInput(%#v) = true", arguments)
		}
	}
}

func TestCurlAttachedFileInputs(t *testing.T) {
	for _, arguments := range [][]string{
		{"-d@data"},
		{"-Fattachment=@file"},
		{"--form=attachment=<file"},
		{"--data-urlencode=field@file"},
	} {
		if !curlUploadsLocalInput(arguments) {
			t.Fatalf("curlUploadsLocalInput(%#v) = false", arguments)
		}
	}
	for _, value := range []string{"field", "field=", "field=x"} {
		if curlFormReadsFile(value) {
			t.Fatalf("curlFormReadsFile(%q) = true", value)
		}
	}
	for _, value := range []string{"field", "field@", "field=value@file"} {
		if curlURLEncodedDataReadsFile(value) {
			t.Fatalf("curlURLEncodedDataReadsFile(%q) = true", value)
		}
	}
}

func TestDestructivePathHelperBoundaries(t *testing.T) {
	if findDeletesRoot([]string{"/", "-name", "value"}, "/", false) {
		t.Fatal("find without -delete was destructive")
	}
	if !findDeletesRoot([]string{"-delete"}, "/", false) {
		t.Fatal("find with an implicit root search path was not destructive")
	}
	if paths, _ := findSearchPaths(nil); len(paths) != 0 {
		t.Fatalf("empty find paths = %#v", paths)
	}
	if blockDevicePath("relative", "/work", true) {
		t.Fatal("unresolved relative device was classified")
	}
	if blockDevicePath("/dev/mapper/control", "/", false) {
		t.Fatal("device-mapper control node was classified as storage")
	}
	for _, name := range []string{"/dev/mapper/root", "/dev/disk/by-id/device"} {
		if !blockDevicePath(name, "/", false) {
			t.Fatalf("blockDevicePath(%q) = false", name)
		}
	}
}
