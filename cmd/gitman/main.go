package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := Execute(os.Args); err != nil {
		slog.Error("gitman failed", "error", err)
		os.Exit(1)
	}
}
