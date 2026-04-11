package ssh

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
)

func Serve(keyIDStr string, cfg *config.Config, database *db.DB) {
	// Avoid leaking internal errors to the remote user's terminal

	originalCmd := os.Getenv("SSH_ORIGINAL_COMMAND")
	if originalCmd == "" {
		fmt.Println("Hi there! You've successfully authenticated, but this server does not provide shell access.")
		os.Exit(0)
	}

	parts := strings.SplitN(originalCmd, " ", 2)
	if len(parts) != 2 {
		log.Fatalf("fatal: invalid command format")
	}

	action := parts[0]
	repoPathRaw := strings.Trim(parts[1], "'\"")

	if action != "git-receive-pack" && action != "git-upload-pack" && action != "git-upload-archive" {
		log.Fatalf("fatal: unsupported git command")
	}

	repoParts := strings.Split(repoPathRaw, "/")
	if len(repoParts) != 2 {
		log.Fatalf("fatal: invalid repository format")
	}

	reqUsername := repoParts[0]
	reqRepoName := strings.TrimSuffix(repoParts[1], ".git")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sshKey, err := database.GetSSHKeyByID(ctx, keyIDStr)
	if err != nil || sshKey == nil {
		log.Fatalf("fatal: unauthorized key")
	}

	user, err := database.GetUserByID(ctx, sshKey.UserID)
	if err != nil || user == nil {
		log.Fatalf("fatal: unauthorized user")
	}

	repoOwner, err := database.GetUserByUsername(ctx, reqUsername)
	if err != nil || repoOwner == nil {
		log.Fatalf("fatal: repository not found")
	}

	repo, err := database.GetRepositoryByOwnerAndName(ctx, repoOwner.ID, reqRepoName)
	if err != nil || repo == nil {
		log.Fatalf("fatal: repository not found")
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
		if err == nil && access {
			hasAccess = true
		}
	}

	if !hasAccess {
		log.Fatalf("fatal: access denied")
	}

	fullDiskPath, err := git.SecureRepoPath(cfg.ReposPath, reqUsername, reqRepoName)
	if err != nil {
		log.Fatalf("fatal: invalid repository path")
	}

	cmd := exec.Command(action, fullDiskPath)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Fatalf("fatal: git command failed")
	}
}
