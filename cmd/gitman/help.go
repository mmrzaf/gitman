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
  web             Start web interface
  serve <keyID>   SSH git handler
  admin           Administration commands`)
	return err
}
