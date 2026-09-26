// Command gitman is Gitman's single binary: it runs the web process, the
// worker, the Git push hooks, and the admin CLI, selected by its first
// argument.
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

type command struct {
	summary string
	run     func(args []string) error
}

var commands = map[string]command{
	"version": {
		summary: "print the Gitman version",
		run:     runVersion,
	},
	"check": {
		summary: "validate a .gitman.yml pipeline file",
		run:     runCheck,
	},
	"worker": {
		summary: "run the worker process (claims and runs pipelines)",
		run:     runWorker,
	},
	"web": {
		summary: "run the web process (Git over HTTP and the web interface)",
		run:     runWeb,
	},
	"admin": {
		summary: "manage people, tokens, repositories and rules",
		run:     runAdmin,
	},
	"hook": {
		summary: "run a Git hook (started by Git, not by hand)",
		run:     runHook,
	},
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	name := os.Args[1]
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "gitman: unknown command %q\n\n", name)
		printUsage()
		os.Exit(1)
	}

	if err := cmd.run(os.Args[2:]); err != nil {
		if !errors.Is(err, errHookFailed) {
			fmt.Fprintf(os.Stderr, "gitman: %v\n", err)
		}
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: gitman <command> [arguments]")
	fmt.Fprintln(os.Stderr, "\ncommands:")

	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", name, commands[name].summary)
	}
}
