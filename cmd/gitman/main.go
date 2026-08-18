package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"

	"github.com/mmrzaf/gitman/internal/apperr"
)

func main() {
	if err := Execute(os.Args); err != nil {
		if len(os.Args) > 1 && os.Args[1] == "serve" {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code := exitErr.ExitCode()
				if code > 0 {
					os.Exit(code)
				}
				os.Exit(1)
			}
			_, _ = fmt.Fprintln(os.Stderr, apperr.PublicMessage(err))
			os.Exit(1)
		}
		slog.Error("gitman failed", "error", err)
		os.Exit(1)
	}
}
