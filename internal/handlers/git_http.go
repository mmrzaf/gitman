package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// GitHTTPAuthMiddleware handles standard HTTP Basic Authentication
func (app *App) GitHTTPAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := chi.URLParam(r, "username")
		repoName := chi.URLParam(r, "repo_name")

		owner, err := app.DB.GetUserByUsername(r.Context(), username)
		if err != nil || owner == nil {
			http.Error(w, "Repository not found", http.StatusNotFound)
			return
		}

		repo, err := app.DB.GetRepositoryByOwnerAndName(r.Context(), owner.ID, repoName)
		if err != nil || repo == nil {
			http.Error(w, "Repository not found", http.StatusNotFound)
			return
		}

		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, username, repoName)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		fmt.Println("Resolved Repo Path:", repoPath)

		var currentUser *models.User
		authUser, authPass, hasAuth := r.BasicAuth()

		if hasAuth {
			hash := sha256.Sum256([]byte(authPass))
			tokenHashHex := hex.EncodeToString(hash[:])

			tokenUser, err := app.DB.GetUserByTokenHash(r.Context(), tokenHashHex)

			if err == nil && tokenUser != nil && tokenUser.Username == authUser {
				currentUser = tokenUser
			} else {
				user, err := app.DB.GetUserByUsername(r.Context(), authUser)
				if err == nil && user != nil {
					if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(authPass)); err == nil {
						currentUser = user
					}
				}
			}
		}

		service := r.URL.Query().Get("service")
		isPushing := service == "git-receive-pack" || strings.Contains(r.URL.Path, "git-receive-pack")

		needsAuth := repo.IsPrivate || isPushing

		if needsAuth && currentUser == nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="Gitman Repository"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		isOwner := currentUser != nil && currentUser.ID == repo.OwnerID

		hasReadAccess := isOwner
		hasWriteAccess := isOwner

		if currentUser != nil && !isOwner {
			read, _ := app.DB.HasRepoAccess(r.Context(), repo.ID, currentUser.ID, "read")
			write, _ := app.DB.HasRepoAccess(r.Context(), repo.ID, currentUser.ID, "write")
			hasReadAccess = read
			hasWriteAccess = write
		}

		if needsAuth {
			if currentUser == nil {
				w.Header().Set("WWW-Authenticate", `Basic realm="Gitman Repository"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			if isPushing && !hasWriteAccess {
				http.Error(w, "Forbidden: You don't have push access", http.StatusForbidden)
				return
			}

			if !isPushing && repo.IsPrivate && !hasReadAccess {
				http.Error(w, "Forbidden: You don't have read access to this private repository", http.StatusForbidden)
				return
			}
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

// HandleGitHTTP processes the Smart HTTP Git requests.
func (app *App) HandleGitHTTP(w http.ResponseWriter, r *http.Request) {
	repoPath := GetRepoPath(r) // Fetched safely from context

	gitBin, err := exec.LookPath("git")
	if err != nil {
		slog.Error("git executable not found in PATH", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	remoteUser := ""
	if user := GetUser(r); user != nil {
		remoteUser = user.Username
	}

	absProjectRoot, err := filepath.Abs(app.Config.ReposPath)
	if err != nil {
		slog.Error("Failed to resolve absolute path for ReposPath", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	handler := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Dir:  repoPath,
		Env: []string{
			"GIT_PROJECT_ROOT=" + absProjectRoot,
			"GIT_HTTP_EXPORT_ALL=true",
			"PATH_INFO=" + r.URL.Path,
			"REMOTE_USER=" + remoteUser,
		},
	}

	slog.Debug("Serving Git HTTP",
		"repo", repoPath,
		"project_root", absProjectRoot,
		"path", r.URL.Path,
		"method", r.Method,
		"remote_user", remoteUser,
	)

	handler.ServeHTTP(w, r)
}
