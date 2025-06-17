package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
)

// die is print-and-exit-with-error helper, ala Perl.
func die(format string, a ...any) {
	fmt.Printf(format, a...)
	os.Exit(1)
}

// fileExists returns true if a file exists, and false if it doesn't.
func fileExists(file string) bool {
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return true
}

// simplerun is an abstraction to execute a Command.
func simpleRun(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}
