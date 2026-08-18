package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/repository"
	"github.com/mmrzaf/gitman/internal/validate"
)

type gitSSHCommand struct {
	Action   string
	Owner    string
	RepoName string
}

func parseGitSSHCommand(original string) (gitSSHCommand, error) {
	original = strings.TrimSpace(original)
	space := strings.IndexByte(original, ' ')
	if space <= 0 || space == len(original)-1 {
		return gitSSHCommand{}, fmt.Errorf("invalid SSH command")
	}
	action := original[:space]
	switch action {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
	default:
		return gitSSHCommand{}, fmt.Errorf("unsupported Git SSH command")
	}
	arg := strings.TrimSpace(original[space+1:])
	if len(arg) < 3 || (arg[0] != '\'' && arg[0] != '"') || arg[len(arg)-1] != arg[0] {
		return gitSSHCommand{}, fmt.Errorf("invalid repository argument")
	}
	path := arg[1 : len(arg)-1]
	if strings.ContainsAny(path, "'\"\\\x00\r\n\t") {
		return gitSSHCommand{}, fmt.Errorf("invalid repository argument")
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return gitSSHCommand{}, fmt.Errorf("invalid repository path")
	}
	owner := parts[0]
	repoName := strings.TrimSuffix(parts[1], ".git")
	if parts[1] == repoName || validate.StorageName(owner) != nil || validate.StorageName(repoName) != nil {
		return gitSSHCommand{}, fmt.Errorf("invalid repository path")
	}
	return gitSSHCommand{Action: action, Owner: owner, RepoName: repoName}, nil
}

// Serve executes the single Git command requested by OpenSSH's forced command.
// It never exits the process; the cmd/gitman boundary owns process exit status.
func Serve(ctx context.Context, keyID, originalCommand string, cfg *config.Config, database *db.DB, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		return apperr.New(apperr.KindInternal, "Gitman could not start the SSH request")
	}
	if originalCommand == "" {
		_, err := fmt.Fprintln(stdout, "Hi there! You've successfully authenticated, but this server does not provide shell access.")
		return err
	}
	if cfg == nil || database == nil {
		return apperr.New(apperr.KindInternal, "Gitman could not start the SSH request")
	}
	request, err := parseGitSSHCommand(originalCommand)
	if err != nil {
		return apperr.Wrap(apperr.KindInvalid, "unsupported Git SSH command", err)
	}

	sshKey, err := database.GetSSHKeyByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return apperr.Wrap(apperr.KindUnauthenticated, "SSH key is not authorized", err)
		}
		return apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err)
	}
	user, err := database.GetUserByID(ctx, sshKey.UserID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return apperr.Wrap(apperr.KindInternal, "Gitman is temporarily unavailable", err)
		}
		return apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err)
	}
	repoOwner, err := database.GetUserByUsername(ctx, request.Owner)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return apperr.Wrap(apperr.KindNotFound, "repository not found", err)
		}
		return apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err)
	}
	repo, err := database.GetRepositoryByOwnerAndName(ctx, repoOwner.ID, request.RepoName)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return apperr.Wrap(apperr.KindNotFound, "repository not found", err)
		}
		return apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err)
	}

	permission := repository.PermissionRead
	if request.Action == "git-receive-pack" {
		permission = repository.PermissionWrite
	}
	allowed, err := repository.Allowed(ctx, database, user, repo, permission)
	if err != nil {
		return apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err)
	}
	if !allowed {
		return apperr.New(apperr.KindForbidden, "repository access denied")
	}

	fullDiskPath, err := git.SecureRepoPath(cfg.ReposPath, request.Owner, request.RepoName)
	if err != nil {
		return apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err)
	}
	if err := git.CheckBareRepository(ctx, fullDiskPath); err != nil {
		return apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err)
	}
	// OpenSSH gives us the traditional git-*-pack command name, but Gitman
	// executes it through the same `git` executable used everywhere else. This
	// keeps SSH from depending on separate helper binaries being on PATH.
	gitSubcommand := strings.TrimPrefix(request.Action, "git-")
	cmd := exec.CommandContext(ctx, "git", gitSubcommand, fullDiskPath)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// git-upload-pack/git-receive-pack already wrote its protocol-safe
			// diagnostics to stderr. Preserve the exit status without adding a
			// second Gitman error line to the client.
			return exitErr
		}
		return apperr.Wrap(apperr.KindUnavailable, "Git backend is unavailable", err)
	}
	return nil
}
