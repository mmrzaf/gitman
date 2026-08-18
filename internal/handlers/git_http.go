package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/repository"
)

type gitHTTPOperation uint8

const (
	gitHTTPRead gitHTTPOperation = iota
	gitHTTPWrite
)

func gitOperationForService(service string) (gitHTTPOperation, error) {
	switch service {
	case "git-upload-pack":
		return gitHTTPRead, nil
	case "git-receive-pack":
		return gitHTTPWrite, nil
	default:
		return 0, apperr.New(apperr.KindInvalid, "unsupported Git service")
	}
}

func (app *App) GitHTTPInfoRefsAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation, err := gitOperationForService(r.URL.Query().Get("service"))
		if err != nil {
			app.respondGitHTTPError(w, r, err)
			return
		}
		app.gitHTTPAuth(operation, next).ServeHTTP(w, r)
	})
}

func (app *App) GitHTTPAuthMiddleware(operation gitHTTPOperation) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return app.gitHTTPAuth(operation, next)
	}
}

func (app *App) gitHTTPAuth(operation gitHTTPOperation, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := chi.URLParam(r, "username")
		repoName := chi.URLParam(r, "repo_name")

		owner, err := app.DB.GetUserByUsername(r.Context(), username)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				app.respondGitHTTPError(w, r, apperr.New(apperr.KindNotFound, "repository not found"))
			} else {
				app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
			}
			return
		}
		repo, err := app.DB.GetRepositoryByOwnerAndName(r.Context(), owner.ID, repoName)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				app.respondGitHTTPError(w, r, apperr.New(apperr.KindNotFound, "repository not found"))
			} else {
				app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
			}
			return
		}

		currentUser, credentialsPresent, err := app.gitHTTPUser(r)
		if err != nil {
			app.respondGitHTTPError(w, r, err)
			return
		}

		permission := repository.PermissionRead
		needsAuthentication := repo.IsPrivate
		if operation == gitHTTPWrite {
			permission = repository.PermissionWrite
			needsAuthentication = true
		}
		if needsAuthentication && currentUser == nil {
			app.respondGitHTTPError(w, r, apperr.New(apperr.KindUnauthenticated, "authentication required"))
			return
		}

		allowed, err := repository.Allowed(r.Context(), app.DB, currentUser, repo, permission)
		if err != nil {
			app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
			return
		}
		if !allowed {
			if !credentialsPresent && needsAuthentication {
				app.respondGitHTTPError(w, r, apperr.New(apperr.KindUnauthenticated, "authentication required"))
			} else {
				app.respondGitHTTPError(w, r, apperr.New(apperr.KindForbidden, "repository access denied"))
			}
			return
		}

		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, username, repoName)
		if err != nil {
			app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err))
			return
		}
		if err := git.CheckBareRepository(r.Context(), repoPath); err != nil {
			app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
			return
		}

		ctx := context.WithValue(r.Context(), repoContextKey, repo)
		ctx = context.WithValue(ctx, repoPathContextKey, repoPath)
		ctx = context.WithValue(ctx, repoOwnerContextKey, owner)
		if currentUser != nil {
			ctx = context.WithValue(ctx, userContextKey, currentUser)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (app *App) gitHTTPUser(r *http.Request) (*models.User, bool, error) {
	authUser, authPass, ok := r.BasicAuth()
	if !ok {
		return nil, false, nil
	}
	hash := sha256.Sum256([]byte(authPass))
	user, err := app.DB.GetUserByTokenHash(r.Context(), hex.EncodeToString(hash[:]))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, true, nil
		}
		return nil, true, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err)
	}
	if user.Username != authUser {
		return nil, true, nil
	}
	return user, true, nil
}

// HandleGitHTTP delegates the Smart HTTP protocol to git-http-backend. Gitman
// owns authentication/authorization; Git owns Git protocol semantics.
func (app *App) HandleGitHTTP(w http.ResponseWriter, r *http.Request) {
	repoPath := GetRepoPath(r)
	gitBin, err := exec.LookPath("git")
	if err != nil {
		app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Git backend is unavailable", err))
		return
	}
	absProjectRoot, err := filepath.Abs(app.Config.ReposPath)
	if err != nil {
		app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve the repository root", err))
		return
	}

	remoteUser := ""
	if user := GetUser(r); user != nil {
		remoteUser = user.Username
	}
	var stderr bytes.Buffer
	handler := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Dir:  repoPath,
		Env: []string{
			"GIT_PROJECT_ROOT=" + absProjectRoot,
			"GIT_HTTP_EXPORT_ALL=true",
			fmt.Sprintf("PATH_INFO=%s", r.URL.Path),
			"REMOTE_USER=" + remoteUser,
		},
		Stderr: &stderr,
	}
	handler.ServeHTTP(w, r)
	if stderr.Len() > 0 {
		slog.Warn("git-http-backend stderr",
			"request_id", RequestID(r),
			"repo", repoPath,
			"remote_user", remoteUser,
			"stderr", stderr.String(),
		)
	}
}
