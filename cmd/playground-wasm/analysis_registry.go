package main

import (
	"path"
	"strings"

	"github.com/nullptrpanic/libcommand"
	commandanalysis "github.com/nullptrpanic/libcommand/analysis"
)

var playgroundAnalysisCommands = map[string]libcommand.Command{
	"rm":        commandanalysis.RM,
	"poweroff":  commandanalysis.Poweroff,
	"reboot":    commandanalysis.Reboot,
	"halt":      commandanalysis.Halt,
	"shutdown":  commandanalysis.Shutdown,
	"init":      commandanalysis.Init,
	"telinit":   commandanalysis.Telinit,
	"systemctl": commandanalysis.Systemctl,
	"mkfifo":    commandanalysis.Mkfifo,
	"cat":       commandanalysis.Cat,
	"nc":        commandanalysis.NC,
	"ncat":      commandanalysis.Ncat,
	"netcat":    commandanalysis.Netcat,
	"socat":     commandanalysis.Socat,
	"find":      commandanalysis.Find,
	"mkfs":      commandanalysis.Mkfs,
	"wipefs":    commandanalysis.Wipefs,
	"dd":        commandanalysis.DD,
	"sh":        commandanalysis.Shell,
	"bash":      commandanalysis.Shell,
	"dash":      commandanalysis.Shell,
	"ksh":       commandanalysis.Shell,
	"zsh":       commandanalysis.Shell,
	"python":    commandanalysis.Python,
	"python2":   commandanalysis.Python,
	"python3":   commandanalysis.Python,
	"perl":      commandanalysis.Perl,
	"curl":      commandanalysis.Curl,
}

func lookupPlaygroundAnalysisCommand(name string) libcommand.Command {
	if command := playgroundAnalysisCommands[name]; command != nil {
		return command
	}
	base := path.Base(name)
	if command := playgroundAnalysisCommands[base]; command != nil {
		return command
	}
	if strings.HasPrefix(base, "mkfs.") {
		return playgroundAnalysisCommands["mkfs"]
	}
	if strings.HasPrefix(base, "python") {
		return playgroundAnalysisCommands["python"]
	}
	return nil
}
