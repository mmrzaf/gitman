package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
)

func Serve(keyIDStr string, cfg *config.Config, database *db.DB) {
	originalCmd := os.Getenv("SSH_ORIGINAL_COMMAND")
	if originalCmd == "" {
		fmt.Println("Hi there! You've successfully authenticated, but this server does not provide shell access.")
		os.Exit(0)
	}

	parts := strings.SplitN(originalCmd, " ", 2)
	if len(parts) != 2 {
		slog.Error("invalid SSH command format", "cmd", originalCmd)
		os.Exit(1)
	}

	action := parts[0]
	repoPathRaw := strings.Trim(parts[1], "'\"")

	if action != "git-receive-pack" && action != "git-upload-pack" && action != "git-upload-archive" {
		slog.Error("unsupported git command", "action", action)
		os.Exit(1)
	}

	repoParts := strings.Split(repoPathRaw, "/")
	if len(repoParts) != 2 {
		slog.Error("invalid repository format", "path", repoPathRaw)
		os.Exit(1)
	}

	reqUsername := repoParts[0]
	reqRepoName := strings.TrimSuffix(repoParts[1], ".git")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sshKey, err := database.GetSSHKeyByID(ctx, keyIDStr)
	if err != nil || sshKey == nil {
		slog.Error("unauthorized key", "keyID", keyIDStr, "error", err)
		os.Exit(1)
	}

	user, err := database.GetUserByID(ctx, sshKey.UserID)
	if err != nil || user == nil {
		slog.Error("unauthorized user", "keyID", keyIDStr, "userID", sshKey.UserID, "error", err)
		os.Exit(1)
	}

	repoOwner, err := database.GetUserByUsername(ctx, reqUsername)
	if err != nil || repoOwner == nil {
		slog.Error("repository owner not found", "username", reqUsername, "error", err)
		os.Exit(1)
	}

	repo, err := database.GetRepositoryByOwnerAndName(ctx, repoOwner.ID, reqRepoName)
	if err != nil || repo == nil {
		slog.Error("repository not found", "owner", reqUsername, "repo", reqRepoName, "error", err)
		os.Exit(1)
	}

	hasAccess := false
	if user.ID == repoOwner.ID {
		hasAccess = true
	} else {
		requiredLevel := "read"
		if action == "git-receive-pack" {
			requiredLevel = "write"
		}
		access, err := database.HasRepoAccess(ctx, repo.ID, user.ID, requiredLevel)
		if err != nil || !access {
			slog.Error("access check failed", "userID", user.ID, "repoID", repo.ID, "error", err)
			os.Exit(1)
		}
		hasAccess = true
	}

	if !hasAccess {
		slog.Error("access denied", "user", user.Username, "repo", repo.Name)
		os.Exit(1)
	}

	fullDiskPath, err := git.SecureRepoPath(cfg.ReposPath, reqUsername, reqRepoName)
	if err != nil {
		slog.Error("invalid repository path", "error", err)
		os.Exit(1)
	}

	cmd := exec.Command(action, fullDiskPath)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		slog.Error("git command failed", "action", action, "repo", fullDiskPath, "error", err)
		os.Exit(1)
	}
}
