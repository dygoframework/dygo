package main

import (
	"io"
)

// cmdRun runs one of the app's console commands: dygo run seed --users 10
// builds the main package and runs it as "app seed --users 10", which
// console.Main hands to the command named seed. With no arguments it runs
// the app's help, which lists the commands.
func cmdRun(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && isHelpFlag(args[0]) {
		return printCommandHelp("run", stdout)
	}
	if len(args) == 0 {
		args = []string{"help"}
	}
	return runMainPackage("console.Main(", "console commands yet: run dygo g command <name> first", args, stdout, stderr)
}
