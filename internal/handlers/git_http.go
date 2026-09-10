package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"

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

		currentUser, credentialsPresent, err := app.gitHTTPUser(r, operation)
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

func (app *App) gitHTTPUser(r *http.Request, operation gitHTTPOperation) (*models.User, bool, error) {
	authUser, authPass, ok := r.BasicAuth()
	if !ok {
		return nil, false, nil
	}
	requiredScope := models.AccessTokenScopeRepoRead
	if operation == gitHTTPWrite {
		requiredScope = models.AccessTokenScopeRepoWrite
	}
	hash := sha256.Sum256([]byte(authPass))
	user, err := app.DB.AuthenticateAccessTokenForUserWithScope(r.Context(), hex.EncodeToString(hash[:]), authUser, requiredScope)
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

const (
	defaultGitHTTPMaxConcurrent      = 16
	defaultGitHTTPMaxConcurrentPerIP = 4
	defaultGitHTTPTimeout            = 30 * time.Minute
)

func (app *App) gitHTTPConcurrencyLimiter() *requestConcurrencyLimiter {
	app.gitHTTPOnce.Do(func() {
		total := app.Config.GitHTTPMaxConcurrent
		if total <= 0 {
			total = defaultGitHTTPMaxConcurrent
		}
		perIP := app.Config.GitHTTPMaxConcurrentPerIP
		if perIP <= 0 {
			perIP = defaultGitHTTPMaxConcurrentPerIP
		}
		app.gitHTTPLimiter = newRequestConcurrencyLimiter(total, perIP)
	})
	return app.gitHTTPLimiter
}

// HandleGitHTTP delegates Smart HTTP protocol semantics to git-http-backend,
// but Gitman owns the process lifetime and concurrency boundary. Public clones
// therefore cannot create an unbounded number of long-lived Git processes.
func (app *App) HandleGitHTTP(w http.ResponseWriter, r *http.Request) {
	releaseSlot, ok := app.gitHTTPConcurrencyLimiter().tryAcquire(app.clientIP(r))
	if !ok {
		w.Header().Set("Retry-After", "2")
		app.respondGitHTTPError(w, r, apperr.New(apperr.KindUnavailable, "Git server is busy; retry shortly"))
		return
	}
	defer releaseSlot()

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

	timeout := app.Config.GitHTTPTimeout
	if timeout <= 0 {
		timeout = defaultGitHTTPTimeout
	}
	deadline := time.Now().Add(timeout)
	controller := http.NewResponseController(w)
	defer func() {
		_ = controller.SetReadDeadline(time.Time{})
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Debug("could not set Git HTTP read deadline", "request_id", RequestID(r), "error", err)
	}
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Debug("could not set Git HTTP write deadline", "request_id", RequestID(r), "error", err)
	}

	remoteUser := ""
	if user := GetUser(r); user != nil {
		remoteUser = user.Username
	}
	backendCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	result, backendErr := serveGitHTTPBackend(backendCtx, w, r, gitBin, repoPath, absProjectRoot, remoteUser)
	if result.Stderr != "" {
		slog.Warn("git-http-backend stderr",
			"request_id", RequestID(r),
			"repo", repoPath,
			"remote_user", remoteUser,
			"stderr", result.Stderr,
			"truncated", result.StderrTruncated,
		)
	}
	if backendErr == nil {
		return
	}
	if result.Started {
		slog.Warn("git-http-backend request ended with error",
			"request_id", RequestID(r),
			"repo", repoPath,
			"remote_user", remoteUser,
			"error", backendErr,
		)
		return
	}
	if errors.Is(backendErr, context.DeadlineExceeded) {
		app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Git operation timed out", backendErr))
		return
	}
	if errors.Is(backendErr, context.Canceled) && r.Context().Err() != nil {
		return
	}
	app.respondGitHTTPError(w, r, apperr.Wrap(apperr.KindUnavailable, "Git backend is temporarily unavailable", backendErr))
}
