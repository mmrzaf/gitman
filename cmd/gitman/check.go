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
	if len(cfg.Images) > 0 {
		fmt.Printf(", %d image policies", len(cfg.Images))
	}
	fmt.Println()
	if cfg.Docker {
		fmt.Println("warning: docker: true needs a ref rule that allows Docker (gitman admin rule set --docker).")
	}
	if len(cfg.Targets) > 0 {
		fmt.Println("warning: targets need a ref rule that allows deployments (gitman admin rule set --deploy).")
	}
	return nil
}
