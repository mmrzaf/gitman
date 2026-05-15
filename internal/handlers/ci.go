package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

// secretKeyRegex: only uppercase letters, digits, and underscores, starting with a letter.
var secretKeyRegex = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type CIPageData struct {
	Owner      *models.User
	Repository *models.Repository
	Runs       []models.CIRun
	HookExists bool
}

type CIRunPageData struct {
	Owner      *models.User
	Repository *models.Repository
	Run        *models.CIRun
	LogContent string
	Artifacts  []string
}

type CISecretsPageData struct {
	Owner      *models.User
	Repository *models.Repository
	Secrets    []models.RepoSecret
	NoKey      bool // true when GITMAN_SECRET_KEY is not configured
}

func (app *App) HandleCIGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	runs, err := app.DB.GetCIRunsByRepo(ctx, repo.ID, 50)
	if err != nil {
		slog.Error("failed to list CI runs", "repo", repo.ID, "error", err)
		runs = []models.CIRun{}
	}

	hookExists := hookIsInstalled(app.Config.ReposPath, owner.Username, repo.Name)

	app.renderPage(w, r, "repo_ci.html", PageData{
		Title: repo.Name + " - CI",
		User:  GetUser(r),
		Data: CIPageData{
			Owner:      owner,
			Repository: repo,
			Runs:       runs,
			HookExists: hookExists,
		},
	})
}

type triggerRequest struct {
	CommitHash string `json:"commit_hash"`
	Branch     string `json:"branch"`
	Tag        string `json:"tag"`
	Event      string `json:"event"`
}

func (app *App) HandleCITriggerPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	ctx := r.Context()

	if currentUser == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	isOwner := currentUser.ID == repo.OwnerID
	if !isOwner {
		hasWrite, _ := app.DB.HasRepoAccess(ctx, repo.ID, currentUser.ID, "write")
		if !hasWrite {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	var req triggerRequest

	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		req.Branch = strings.TrimSpace(r.FormValue("branch"))
		req.Tag = strings.TrimSpace(r.FormValue("tag"))
		req.Event = strings.TrimSpace(r.FormValue("event"))
	}

	if req.Event == "" {
		req.Event = "manual"
	}

	if req.CommitHash == "" {
		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
		if err == nil && !git.IsEmpty(ctx, repoPath) {
			if req.Branch == "" {
				req.Branch, _ = git.GetDefaultBranch(ctx, repoPath)
			}
			commits, err := git.GetCommits(ctx, repoPath, req.Branch, 0, 1)
			if err == nil && len(commits) > 0 {
				req.CommitHash = commits[0].Hash
			}
		}
	}

	runID, err := app.DB.CreateCIRun(ctx, repo.ID, req.CommitHash, req.Branch, req.Tag, req.Event)
	if err != nil {
		slog.Error("failed to create CI run", "repo", repo.ID, "error", err)
		if isHTMX(r) {
			http.Error(w, "Failed to create CI run", http.StatusInternalServerError)
		} else {
			http.Error(w, "Failed to create CI run", http.StatusInternalServerError)
		}
		return
	}

	slog.Info("CI run created", "run_id", runID, "repo", repo.ID, "event", req.Event)

	if strings.Contains(ct, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if err := json.NewEncoder(w).Encode(map[string]string{"run_id": runID}); err != nil {
			slog.Warn("failed to encode CI trigger response", "run_id", runID, "error", err)
		}
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci", owner.Username, repo.Name), http.StatusSeeOther)
}

func (app *App) HandleCITriggerWebhook(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	ctx := r.Context()

	var req triggerRequest
	owner, err := app.DB.GetUserByID(ctx, repo.OwnerID)
	if err != nil || owner == nil {
		http.Error(w, "Repository owner not found", http.StatusInternalServerError)
		return
	}

	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		req.Branch = strings.TrimSpace(r.FormValue("branch"))
		req.Tag = strings.TrimSpace(r.FormValue("tag"))
		req.Event = strings.TrimSpace(r.FormValue("event"))
	}

	if req.Event == "" {
		req.Event = "manual"
	}

	if req.CommitHash == "" {
		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
		if err == nil && !git.IsEmpty(ctx, repoPath) {
			if req.Branch == "" {
				req.Branch, _ = git.GetDefaultBranch(ctx, repoPath)
			}
			commits, err := git.GetCommits(ctx, repoPath, req.Branch, 0, 1)
			if err == nil && len(commits) > 0 {
				req.CommitHash = commits[0].Hash
			}
		}
	}

	runID, err := app.DB.CreateCIRun(ctx, repo.ID, req.CommitHash, req.Branch, req.Tag, req.Event)
	if err != nil {
		slog.Error("failed to create CI run", "repo", repo.ID, "error", err)
		if isHTMX(r) {
			http.Error(w, "Failed to create CI run", http.StatusInternalServerError)
		} else {
			http.Error(w, "Failed to create CI run", http.StatusInternalServerError)
		}
		return
	}

	slog.Info("CI run created", "run_id", runID, "repo", repo.ID, "event", req.Event)

	if strings.Contains(ct, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if err := json.NewEncoder(w).Encode(map[string]string{"run_id": runID}); err != nil {
			slog.Warn("failed to encode CI trigger response", "run_id", runID, "error", err)
		}
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci", owner.Username, repo.Name), http.StatusSeeOther)
}

func (app *App) HandleCIRunGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	ctx := r.Context()

	run, err := app.DB.GetCIRunByID(ctx, runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		app.renderError(w, r, PageData{User: GetUser(r)}, "CI run not found", http.StatusNotFound)
		return
	}

	logContent := ""
	if run.LogFile != "" {
		data, err := os.ReadFile(run.LogFile)
		if err == nil {
			logContent = string(data)
		}
	}
	artifactDir := filepath.Join(app.Config.ArtifactsPath, "files", owner.Username, repo.Name, run.ID)
	var artifacts []string
	if entries, err := os.ReadDir(artifactDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				artifacts = append(artifacts, entry.Name())
			}
		}
	}
	app.renderPage(w, r, "repo_ci_run.html", PageData{
		Title: fmt.Sprintf("Run %s — CI", run.ID[:8]),
		User:  GetUser(r),
		Data: CIRunPageData{
			Owner:      owner,
			Repository: repo,
			Run:        run,
			LogContent: logContent,
			Artifacts:  artifacts,
		},
	})
}

func (app *App) HandleCIRunLogGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	runID := chi.URLParam(r, "run_id")
	ctx := r.Context()

	run, err := app.DB.GetCIRunByID(ctx, runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if run.LogFile == "" {
		if _, err := w.Write([]byte("(log not yet available — worker is preparing the workspace)")); err != nil {
			slog.Warn("failed to write CI log response", "run_id", runID, "error", err)
		}
		return
	}

	data, err := os.ReadFile(run.LogFile)
	if err != nil {
		if _, writeErr := w.Write([]byte("(log file not readable)")); writeErr != nil {
			slog.Warn("failed to write CI log error response", "run_id", runID, "error", writeErr)
		}
		return
	}

	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	sanitized := re.ReplaceAllString(string(data), "")

	if _, err := w.Write([]byte(sanitized)); err != nil {
		slog.Warn("failed to write CI log data", "run_id", runID, "error", err)
	}
}

func (app *App) HandleCISecretsGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderError(w, r, PageData{User: currentUser}, "Forbidden", http.StatusForbidden)
		return
	}

	secrets, err := app.DB.GetRepoSecrets(r.Context(), repo.ID)
	if err != nil {
		secrets = []models.RepoSecret{}
	}

	app.renderPage(w, r, "repo_ci_secrets.html", PageData{
		Title: repo.Name + " - CI Secrets",
		User:  currentUser,
		Data: CISecretsPageData{
			Owner:      owner,
			Repository: repo,
			Secrets:    secrets,
			NoKey:      app.Config.SecretKey == "",
		},
	})
}

func (app *App) HandleCISecretsAddPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)

	renderPanel := func(errStr, successStr string) {
		secrets, _ := app.DB.GetRepoSecrets(r.Context(), repo.ID)
		app.renderPartial(w, r, "repo_ci_secrets.html", "ci_secrets_panel", PageData{
			User:    currentUser,
			Error:   errStr,
			Success: successStr,
			Data: CISecretsPageData{
				Owner:      owner,
				Repository: repo,
				Secrets:    secrets,
				NoKey:      app.Config.SecretKey == "",
			},
		})
	}

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		renderPanel("Only the repository owner can manage secrets.", "")
		return
	}

	if app.Config.SecretKey == "" {
		renderPanel("GITMAN_SECRET_KEY is not configured on this server. Secrets cannot be stored.", "")
		return
	}

	if err := r.ParseForm(); err != nil {
		renderPanel("Invalid form data.", "")
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	value := strings.TrimSpace(r.FormValue("value"))

	if key == "" || value == "" {
		renderPanel("Key and value are required.", "")
		return
	}

	if !secretKeyRegex.MatchString(key) {
		renderPanel("Key must be uppercase letters, digits, and underscores, starting with a letter.", "")
		return
	}

	encrypted, err := db.EncryptSecret(app.Config.SecretKey, value)
	if err != nil {
		renderPanel("Failed to encrypt secret.", "")
		return
	}

	if err := app.DB.AddRepoSecret(r.Context(), repo.ID, key, encrypted); err != nil {
		renderPanel("Failed to save secret.", "")
		return
	}

	renderPanel("", fmt.Sprintf("Secret %q saved.", key))
}

func (app *App) HandleCISecretsDeletePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	secretID := chi.URLParam(r, "id")

	renderPanel := func(errStr, successStr string) {
		secrets, _ := app.DB.GetRepoSecrets(r.Context(), repo.ID)
		app.renderPartial(w, r, "repo_ci_secrets.html", "ci_secrets_panel", PageData{
			User:    currentUser,
			Error:   errStr,
			Success: successStr,
			Data: CISecretsPageData{
				Owner:      owner,
				Repository: repo,
				Secrets:    secrets,
				NoKey:      app.Config.SecretKey == "",
			},
		})
	}

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		renderPanel("Forbidden.", "")
		return
	}

	if err := app.DB.DeleteRepoSecret(r.Context(), secretID, repo.ID); err != nil {
		renderPanel("Failed to delete secret.", "")
		return
	}

	renderPanel("", "Secret deleted.")
}

func hookPath(reposPath, ownerUsername, repoName string) (string, error) {
	repoPath, err := git.SecureRepoPath(reposPath, ownerUsername, repoName)
	if err != nil {
		return "", err
	}
	return filepath.Join(repoPath, "hooks", "post-receive"), nil
}

func hookIsInstalled(reposPath, ownerUsername, repoName string) bool {
	hp, err := hookPath(reposPath, ownerUsername, repoName)
	if err != nil {
		return false
	}
	_, err = os.Stat(hp)
	return err == nil
}

func (app *App) HandleCIHookInstallPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderError(w, r, PageData{User: currentUser}, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Invalid form data", http.StatusBadRequest)
		return
	}

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to generate webhook secret", http.StatusInternalServerError)
		return
	}
	secret := hex.EncodeToString(secretBytes)

	if err := app.DB.SetWebhookSecret(r.Context(), repo.ID, secret); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to save webhook secret", http.StatusInternalServerError)
		return
	}

	hp, err := hookPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Invalid repository path", http.StatusInternalServerError)
		return
	}

	if err := os.MkdirAll(filepath.Dir(hp), 0o700); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to create hooks directory", http.StatusInternalServerError)
		return
	}
	script := buildHookScript(app.Config.InternalURL, owner.Username, repo.Name, secret)

	if err := os.WriteFile(hp, []byte(script), 0o700); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to write hook script", http.StatusInternalServerError)
		return
	}

	slog.Info("post-receive hook installed", "repo", repo.ID, "by", currentUser.Username)
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci?success=hook_installed", owner.Username, repo.Name), http.StatusSeeOther)
}

// HandleCIHookUninstallPOST removes the post-receive hook from the bare repo.
func (app *App) HandleCIHookUninstallPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderError(w, r, PageData{User: currentUser}, "Forbidden", http.StatusForbidden)
		return
	}

	hp, err := hookPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Invalid repository path", http.StatusInternalServerError)
		return
	}

	if err := os.Remove(hp); err != nil && !os.IsNotExist(err) {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to remove hook", http.StatusInternalServerError)
		return
	}

	slog.Info("post-receive hook uninstalled", "repo", repo.ID, "by", currentUser.Username)
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci", owner.Username, repo.Name), http.StatusSeeOther)
}

func buildHookScript(serverURL, ownerUsername, repoName, secret string) string {
	return fmt.Sprintf(`#!/bin/bash
# Managed by Gitman CI/CD. Do not edit manually.
# Re-install via the repository's CI settings page to update the token.

GITMAN_SERVER="%s"
GITMAN_SECRET="%s"
GITMAN_OWNER="%s"
GITMAN_REPO="%s"

while read -r old new ref; do
    branch=""
    tag=""

    if [[ "$ref" == refs/heads/* ]]; then
        branch="${ref#refs/heads/}"
    elif [[ "$ref" == refs/tags/* ]]; then
        tag="${ref#refs/tags/}"
    else
        continue
    fi

    # Skip delete operations (new is all zeros)
    if [[ "$new" == "0000000000000000000000000000000000000000" ]]; then
        continue
    fi

    payload=$(printf '{"commit_hash":"%%s","branch":"%%s","tag":"%%s","event":"push"}' \
        "$new" "$branch" "$tag")

    curl -s -f -X POST \
        -H "X-Gitman-Webhook-Secret: $GITMAN_SECRET" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "$GITMAN_SERVER/repos/$GITMAN_OWNER/$GITMAN_REPO/ci/webhook" \
        >/dev/null 2>&1 || true
done

exit 0
`, serverURL, secret, ownerUsername, repoName)
}

// HandleArtifactByBranch serves the latest successful artifact for a branch.
// Route: GET /api/repos/{username}/{repo_name}/artifacts/latest/branch/{branch_name}/{filename}
func (app *App) HandleArtifactByBranch(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	branch := chi.URLParam(r, "branch_name")
	filename := chi.URLParam(r, "filename")

	run, err := app.DB.GetLatestSuccessfulRunForBranch(r.Context(), repo.ID, branch)
	if err != nil || run == nil {
		http.Error(w, "No successful run found for branch", http.StatusNotFound)
		return
	}

	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, filename)
}

// HandleArtifactByTag serves the artifact from the run associated with a git tag.
// Route: GET /api/repos/{username}/{repo_name}/artifacts/tag/{tag_name}/{filename}
func (app *App) HandleArtifactByTag(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	tag := chi.URLParam(r, "tag_name")
	filename := chi.URLParam(r, "filename")

	run, err := app.DB.GetSuccessfulRunForTag(r.Context(), repo.ID, tag)
	if err != nil || run == nil {
		http.Error(w, "No successful run found for tag", http.StatusNotFound)
		return
	}

	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, filename)
}

// HandleArtifactByCommit serves the artifact from the run for a specific commit.
// Route: GET /api/repos/{username}/{repo_name}/artifacts/commit/{commit_hash}/{filename}
func (app *App) HandleArtifactByCommit(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	commit := chi.URLParam(r, "commit_hash")
	filename := chi.URLParam(r, "filename")

	run, err := app.DB.GetSuccessfulRunForCommit(r.Context(), repo.ID, commit)
	if err != nil || run == nil {
		http.Error(w, "No successful run found for commit", http.StatusNotFound)
		return
	}

	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, filename)
}

// HandleArtifactByRunID serves an artifact file for a specific CI run.
// Route: GET /api/repos/{username}/{repo_name}/artifacts/run/{run_id}/{filename}
func (app *App) HandleArtifactByRunID(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	filename := chi.URLParam(r, "filename")

	// Verify run exists and belongs to this repo
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}

	// Use the same validation and serving logic as other artifact endpoints
	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, runID, filename)
}

func serveArtifact(w http.ResponseWriter, r *http.Request, artifactsPath, owner, repo, runID, filename string) {
	// Strict filename validation – only alphanumeric, dot, dash, underscore
	if !regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`).MatchString(filename) {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	baseDir := filepath.Join(artifactsPath, "files", owner, repo, runID)
	requestedPath := filepath.Join(baseDir, filename)
	cleaned, err := filepath.Abs(requestedPath)
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !strings.HasPrefix(cleaned, baseAbs+string(os.PathSeparator)) && cleaned != baseAbs {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	f, err := os.Open(requestedPath)
	if err != nil {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			slog.Warn("failed to close artifact file", "path", requestedPath, "error", closeErr)
		}
	}()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "Artifact stat failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// StatusBadge returns a short display string and CSS class for a CI run status.
func StatusBadge(status string) (label, class string) {
	switch status {
	case "pending":
		return "Pending", "badge-pending"
	case "running":
		return "Running", "badge-running"
	case "success":
		return "Success", "badge-success"
	case "failed":
		return "Failed", "badge-failed"
	case "skipped":
		return "Skipped", "badge-skipped"
	default:
		return status, "badge-unknown"
	}
}

// FormatDuration returns a human-readable elapsed time for a CI run.
func FormatDuration(run *models.CIRun) string {
	if run.CompletedAt == nil {
		return time.Since(run.CreatedAt).Truncate(time.Second).String()
	}
	return run.CompletedAt.Sub(run.CreatedAt).Truncate(time.Second).String()
}
