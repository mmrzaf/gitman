// Package push implements the Git pre-receive and post-receive hooks.
//
// Git runs hooks as separate processes. Gitman passes each push's
// context to them through environment variables set on the receive-pack
// process it starts, and points core.hooksPath at a directory of small
// scripts that exec this same binary as `gitman hook <name>`. The web
// process rewrites those scripts on every start, so they always name the
// binary that is actually running.
package push

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmrzaf/gitman/internal/config"
)

// Environment variables carrying a push's context from the HTTP handler,
// through git receive-pack, into the hook process.
const (
	EnvRepoID     = "GITMAN_HOOK_REPO_ID"
	EnvPersonID   = "GITMAN_HOOK_PERSON_ID"
	EnvRemoteAddr = "GITMAN_HOOK_REMOTE_ADDR"
)

// Names of the hooks Gitman installs.
const (
	PreReceive  = "pre-receive"
	PostReceive = "post-receive"
)

// gitHookEnv lists the variables Git sets for a hook that the hook's own
// Git commands must see. During pre-receive, pushed objects sit in a
// quarantine directory named by these variables until the push is
// accepted.
var gitHookEnv = []string{
	"GIT_QUARANTINE_PATH",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
}

// Env returns the environment for a receive-pack process serving one
// push: the configuration the hook process loads, and the push's context.
// The secret key is deliberately not passed; hooks never need it.
func Env(cfg *config.Config, repoID, personID, remoteAddr string) []string {
	return []string{
		config.EnvDatabaseURL + "=" + cfg.DatabaseURL,
		config.EnvDataDir + "=" + cfg.DataDir,
		config.EnvPublicURL + "=" + cfg.PublicURL,
		EnvRepoID + "=" + repoID,
		EnvPersonID + "=" + personID,
		EnvRemoteAddr + "=" + remoteAddr,
	}
}

// Context is what a hook process knows about the push it is serving.
type Context struct {
	RepoID     string
	PersonID   string
	RemoteAddr string
	// GitEnv holds the variables from gitHookEnv that Git set.
	GitEnv []string
}

// ContextFromEnv reads a hook's context from its environment. It fails
// when the hook was not started by Gitman's own transport, which is the
// only way a push can legitimately reach it.
func ContextFromEnv() (Context, error) {
	c := Context{
		RepoID:     os.Getenv(EnvRepoID),
		PersonID:   os.Getenv(EnvPersonID),
		RemoteAddr: os.Getenv(EnvRemoteAddr),
	}
	if c.RepoID == "" || c.PersonID == "" {
		return Context{}, errors.New("this hook only runs for pushes received by Gitman")
	}
	for _, key := range gitHookEnv {
		if value, ok := os.LookupEnv(key); ok {
			c.GitEnv = append(c.GitEnv, key+"="+value)
		}
	}
	return c, nil
}

// Install writes the hook scripts into dir, each exec'ing executable with
// `hook <name>`. Each script is written to a temporary file and renamed
// into place, so a push running while the web process restarts never
// sees a half-written script.
func Install(dir, executable string) error {
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("hook executable must be an absolute path, got %q", executable)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}
	for _, name := range []string{PreReceive, PostReceive} {
		script := "#!/bin/sh\nexec " + shellQuote(executable) + " hook " + name + "\n"
		tmp, err := os.CreateTemp(dir, "."+name+"-*")
		if err != nil {
			return fmt.Errorf("write %s hook: %w", name, err)
		}
		_, writeErr := tmp.WriteString(script)
		closeErr := tmp.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("write %s hook: %w", name, err)
		}
		if err := os.Chmod(tmp.Name(), 0o755); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("write %s hook: %w", name, err)
		}
		if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("install %s hook: %w", name, err)
		}
	}
	return nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
