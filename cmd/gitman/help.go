package main

import (
	"fmt"
	"io"
)

func help(out io.Writer) error {
	_, err := fmt.Fprintln(out, `Gitman - Lightweight Git Server

Usage:
  gitman <command>

Commands:
  web             Start web interface and Git smart HTTP server
  serve <keyID>   SSH git handler (invoked as forced command by OpenSSH)
  worker          Start the CI/CD background worker
  admin           Administration commands`)
	return err
}
