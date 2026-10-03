package main

import (
	"fmt"
	"os"

	"github.com/mmrzaf/gitman/internal/ci"
)

// runCheck validates a pipeline file without needing a Gitman server, a
// database, or a push: the same check push preparation runs against the
// pushed commit, run locally against a working tree instead.
func runCheck(args []string) error {
	path := ci.FileName
	if len(args) > 0 {
		path = args[0]
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	cfg, err := ci.Parse(data)
	if err != nil {
		return fmt.Errorf("%s:\n%v", path, err)
	}

	fmt.Printf("%s is valid: image %s, %d step(s)", path, cfg.Image, len(cfg.Steps))
	if len(cfg.Targets) > 0 {
		fmt.Printf(", %d target(s)", len(cfg.Targets))
	}
	fmt.Println()
	return nil
}
