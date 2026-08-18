package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	cipolicy "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

// secretKeyRegex: only uppercase letters, digits, and underscores, starting with a letter.
var secretKeyRegex = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type CIPageData struct {
	Owner         *models.User
	Repository    *models.Repository
	Runs          []CIRunView
	HookExists    bool
	HookState     string
	Branches      []string
	Tags          []string
	DefaultBranch string
	RefRules      []models.RepoCIRefRule
	CanControl    bool
	StatusFilter  string
	BranchFilter  string
}

type CIRunPageData struct {
	Owner         *models.User
	Repository    *models.Repository
	Run           *models.CIRun
	Commit        *git.Commit
	LogContent    string
	LogOffset     int64
	Log           CILogView
	Config        CIConfigView
	Artifacts     []*ArtifactNode
	ArtifactCount int
	ArtifactBytes int64
	Attempts      []CIRunView
	CanControl    bool
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

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter != "" {
		switch statusFilter {
		case "pending", "running", "success", "failed", "skipped", "cancelled":
		default:
			statusFilter = ""
		}
	}
	branchFilter := strings.TrimSpace(r.URL.Query().Get("branch"))
	if branchFilter != "" && git.ValidateRefName(branchFilter) != nil {
		branchFilter = ""
	}

	runs, err := app.DB.GetCIRunsByRepoFiltered(ctx, repo.ID, statusFilter, branchFilter, 100)
	if err != nil {
		slog.Error("failed to list CI runs", "repo", repo.ID, "error", err)
		runs = []models.CIRun{}
	}

	hookExists := hookIsInstalled(app.Config.ReposPath, owner.Username, repo.Name)
	hookState := app.hookState(owner.Username, repo.Name)

	var branches, tags []string
	defaultBranch := ""
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err == nil && !git.IsEmpty(ctx, repoPath) {
		if branches, err = git.GetBranches(ctx, repoPath); err != nil {
			branches = []string{}
		}
		if tags, err = git.GetTags(ctx, repoPath); err != nil {
			tags = []string{}
		}
		if defaultBranch, err = git.GetDefaultBranch(ctx, repoPath); err != nil {
			defaultBranch = ""
		}
	}
	runViews := loadCIRunViews(ctx, repoPath, runs)
	refRules, err := app.DB.ListRepoCIRefRules(ctx, repo.ID)
	if err != nil {
		slog.Warn("failed to list CI ref rules", "repo", repo.ID, "error", err)
		refRules = []models.RepoCIRefRule{}
	}

	app.renderPage(w, r, "repo_ci.html", PageData{
		Title: repo.Name + " - CI",
		User:  GetUser(r),
		Data: CIPageData{
			Owner:         owner,
			Repository:    repo,
			Runs:          runViews,
			HookExists:    hookExists,
			HookState:     string(hookState),
			Branches:      branches,
			Tags:          tags,
			DefaultBranch: defaultBranch,
			RefRules:      refRules,
			CanControl:    app.canControlCI(r.Context(), GetUser(r), repo),
			StatusFilter:  statusFilter,
			BranchFilter:  branchFilter,
		},
	})
}

func (app *App) canViewCI(ctx context.Context, user *models.User, repo *models.Repository) bool {
	if user == nil || repo == nil {
		return false
	}
	if user.ID == repo.OwnerID {
		return true
	}
	hasRead, err := app.DB.HasRepoAccess(ctx, repo.ID, user.ID, "read")
	return err == nil && hasRead
}

func (app *App) canControlCI(ctx context.Context, user *models.User, repo *models.Repository) bool {
	if user == nil || repo == nil {
		return false
	}
	if user.ID == repo.OwnerID {
		return true
	}
	hasWrite, err := app.DB.HasRepoAccess(ctx, repo.ID, user.ID, "write")
	return err == nil && hasWrite
}

func (app *App) HandleCISettingsRulePOST(w http.ResponseWriter, r *http.Request) {
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
	refType := strings.TrimSpace(r.FormValue("ref_type"))
	refName := strings.TrimSpace(r.FormValue("ref_name"))
	if refType != "branch" && refType != "tag" {
		app.renderError(w, r, PageData{User: currentUser}, "Invalid CI ref type", http.StatusBadRequest)
		return
	}
	if refName == "" || strings.ContainsAny(refName, "\x00\r\n") {
		app.renderError(w, r, PageData{User: currentUser}, "Invalid CI ref name", http.StatusBadRequest)
		return
	}
	refIsPattern := strings.ContainsAny(refName, "*?[")
	if refIsPattern {
		if _, err := path.Match(refName, "test"); err != nil {
			app.renderError(w, r, PageData{User: currentUser}, "Invalid CI ref pattern", http.StatusBadRequest)
			return
		}
	} else {
		if err := git.ValidateRefName(refName); err != nil {
			app.renderError(w, r, PageData{User: currentUser}, "Invalid CI ref name", http.StatusBadRequest)
			return
		}
		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
		if err != nil {
			app.renderError(w, r, PageData{User: currentUser}, "Invalid repository path", http.StatusInternalServerError)
			return
		}
		if refType == "branch" {
			if _, err := git.ResolveBranchCommitHash(r.Context(), repoPath, refName); err != nil {
				app.renderError(w, r, PageData{User: currentUser}, "Branch does not resolve in repository", http.StatusBadRequest)
				return
			}
		} else {
			if _, err := git.ResolveTagCommitHash(r.Context(), repoPath, refName); err != nil {
				app.renderError(w, r, PageData{User: currentUser}, "Tag does not resolve in repository", http.StatusBadRequest)
				return
			}
		}
	}
	rule := models.RepoCIRefRule{
		RepoID:            repo.ID,
		RefType:           refType,
		RefName:           refName,
		AutoRun:           r.FormValue("auto_run") == "on",
		AllowSecrets:      r.FormValue("allow_secrets") == "on",
		AllowDockerSocket: r.FormValue("allow_docker_socket") == "on",
	}
	if err := app.DB.UpsertRepoCIRefRule(r.Context(), rule); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to save CI ref rule", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci?success=ci_rule_saved", owner.Username, repo.Name), http.StatusSeeOther)
}

func (app *App) HandleCISettingsRuleDeletePOST(w http.ResponseWriter, r *http.Request) {
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
	refType := strings.TrimSpace(r.FormValue("ref_type"))
	refName := strings.TrimSpace(r.FormValue("ref_name"))
	if err := app.DB.DeleteRepoCIRefRule(r.Context(), repo.ID, refType, refName); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to delete CI ref rule", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci?success=ci_rule_deleted", owner.Username, repo.Name), http.StatusSeeOther)
}

type triggerRequest struct {
	CommitHash string `json:"commit_hash"`
	Branch     string `json:"branch"`
	Tag        string `json:"tag"`
	Event      string `json:"event"`
}

func decodeTriggerRequest(w http.ResponseWriter, r *http.Request) (triggerRequest, error) {
	var req triggerRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return req, fmt.Errorf("invalid JSON: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return req, fmt.Errorf("JSON body must contain exactly one object")
		}
		return req, nil
	}
	if err := r.ParseForm(); err != nil {
		return req, fmt.Errorf("invalid form data: %w", err)
	}
	req.CommitHash = r.FormValue("commit_hash")
	req.Branch = r.FormValue("branch")
	req.Tag = r.FormValue("tag")
	req.Event = r.FormValue("event")
	return req, nil
}

func normalizeCITrigger(ctx context.Context, reposPath string, owner *models.User, repo *models.Repository, req triggerRequest, defaultEvent string) (triggerRequest, error) {
	req.CommitHash = strings.TrimSpace(req.CommitHash)
	req.Branch = strings.TrimSpace(req.Branch)
	req.Tag = strings.TrimSpace(req.Tag)
	req.Event = strings.TrimSpace(req.Event)

	for name, value := range map[string]string{
		"commit_hash": req.CommitHash,
		"branch":      req.Branch,
		"tag":         req.Tag,
		"event":       req.Event,
	} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return req, fmt.Errorf("%s contains unsupported control characters", name)
		}
	}
	if req.Branch != "" && req.Tag != "" {
		return req, fmt.Errorf("branch and tag are mutually exclusive")
	}
	if req.Branch != "" {
		if err := git.ValidateRefName(req.Branch); err != nil {
			return req, fmt.Errorf("invalid branch: %w", err)
		}
	}
	if req.Tag != "" {
		if err := git.ValidateRefName(req.Tag); err != nil {
			return req, fmt.Errorf("invalid tag: %w", err)
		}
	}

	switch defaultEvent {
	case "push":
		req.Event = "push"
		if req.Branch == "" && req.Tag == "" {
			return req, fmt.Errorf("push event requires a branch or tag")
		}
	case "manual":
		req.Event = "manual"
	default:
		return req, fmt.Errorf("invalid CI event")
	}

	repoPath, err := git.SecureRepoPath(reposPath, owner.Username, repo.Name)
	if err != nil {
		return req, fmt.Errorf("invalid repository path: %w", err)
	}
	if git.IsEmpty(ctx, repoPath) {
		return req, fmt.Errorf("repository is empty")
	}

	var requestedRefHash string
	if req.Branch != "" {
		requestedRefHash, err = git.ResolveBranchCommitHash(ctx, repoPath, req.Branch)
		if err != nil {
			return req, fmt.Errorf("branch does not resolve in repository")
		}
	} else if req.Tag != "" {
		requestedRefHash, err = git.ResolveTagCommitHash(ctx, repoPath, req.Tag)
		if err != nil {
			return req, fmt.Errorf("tag does not resolve in repository")
		}
	}

	if req.CommitHash == "" {
		if requestedRefHash != "" {
			req.CommitHash = requestedRefHash
		} else {
			resolvedRef, err := git.ResolveRef(ctx, repoPath, "")
			if err != nil {
				return req, fmt.Errorf("resolve CI ref: %w", err)
			}
			commits, err := git.GetCommits(ctx, repoPath, resolvedRef, 0, 1)
			if err != nil || len(commits) == 0 {
				return req, fmt.Errorf("resolve CI commit")
			}
			req.CommitHash = commits[0].Hash
			if branchHash, branchErr := git.ResolveBranchCommitHash(ctx, repoPath, resolvedRef); branchErr == nil && branchHash == req.CommitHash {
				req.Branch = resolvedRef
				requestedRefHash = branchHash
			}
		}
	}

	resolvedHash, err := git.ResolveCommitHash(ctx, repoPath, req.CommitHash)
	if err != nil {
		return req, fmt.Errorf("commit does not resolve in repository")
	}
	req.CommitHash = resolvedHash

	if req.Tag != "" && resolvedHash != requestedRefHash {
		return req, fmt.Errorf("commit does not match tag")
	}
	if req.Branch != "" {
		if req.Event == "push" {
			if resolvedHash != requestedRefHash {
				return req, fmt.Errorf("commit does not match pushed branch tip")
			}
		} else {
			reachable, err := git.IsCommitReachableFromBranch(ctx, repoPath, resolvedHash, req.Branch)
			if err != nil || !reachable {
				return req, fmt.Errorf("commit is not reachable from branch")
			}
		}
	}
	return req, nil
}

func (app *App) createCIRun(w http.ResponseWriter, r *http.Request, repo *models.Repository, owner *models.User, defaultEvent string) (string, bool) {
	req, err := decodeTriggerRequest(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	req, err = normalizeCITrigger(r.Context(), app.Config.ReposPath, owner, repo, req, defaultEvent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	var runID string
	if req.Event == "push" {
		runID, err = app.DB.CreatePushCIRun(r.Context(), repo.ID, req.CommitHash, req.Branch, req.Tag)
	} else {
		runID, err = app.DB.CreateCIRun(r.Context(), repo.ID, req.CommitHash, req.Branch, req.Tag, req.Event)
	}
	if err != nil {
		slog.Error("failed to create CI run", "repo", repo.ID, "error", err)
		http.Error(w, "Failed to create CI run", http.StatusInternalServerError)
		return "", false
	}
	slog.Info("CI run created", "run_id", runID, "repo", repo.ID, "event", req.Event)
	return runID, true
}

func (app *App) HandleCITriggerPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	if currentUser == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !app.canControlCI(r.Context(), currentUser, repo) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	runID, ok := app.createCIRun(w, r, repo, owner, "manual")
	if !ok {
		return
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
		return
	}
	redirectCIRun(w, r, owner.Username, repo.Name, runID, "Run queued.", "")
}

func ciRunURL(owner, repo, runID string) string {
	return fmt.Sprintf("/%s/%s/ci/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(runID))
}

func redirectCIRun(w http.ResponseWriter, r *http.Request, owner, repo, runID, message, errMessage string) {
	target := ciRunURL(owner, repo, runID)
	values := url.Values{}
	if message != "" {
		values.Set("message", message)
	}
	if errMessage != "" {
		values.Set("error", errMessage)
	}
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (app *App) HandleCIRunCancelPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	user := GetUser(r)
	if !app.canControlCI(r.Context(), user, repo) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	runID := chi.URLParam(r, "run_id")
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		app.renderError(w, r, PageData{User: user}, "CI run not found", http.StatusNotFound)
		return
	}
	cancelled, err := app.DB.CancelCIRun(r.Context(), repo.ID, runID, "Cancelled by "+user.Username)
	if err != nil {
		slog.Error("failed to cancel CI run", "run_id", runID, "repo", repo.ID, "error", err)
		redirectCIRun(w, r, owner.Username, repo.Name, runID, "", "Failed to cancel the run.")
		return
	}
	if !cancelled {
		redirectCIRun(w, r, owner.Username, repo.Name, runID, "", "This run has already finished.")
		return
	}
	slog.Info("CI run cancelled", "run_id", runID, "repo", repo.ID, "user", user.Username)
	redirectCIRun(w, r, owner.Username, repo.Name, runID, "Run cancelled.", "")
}

func (app *App) HandleCIRunRetryPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	user := GetUser(r)
	if !app.canControlCI(r.Context(), user, repo) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	runID := chi.URLParam(r, "run_id")
	newRunID, err := app.DB.RetryCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		slog.Warn("failed to retry CI run", "run_id", runID, "repo", repo.ID, "error", err)
		redirectCIRun(w, r, owner.Username, repo.Name, runID, "", "Only completed runs can be retried.")
		return
	}
	slog.Info("CI run retried", "run_id", newRunID, "retry_of", runID, "repo", repo.ID, "user", user.Username)
	redirectCIRun(w, r, owner.Username, repo.Name, newRunID, "Retry queued.", "")
}

func (app *App) HandleCITriggerWebhook(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner, err := app.DB.GetUserByID(r.Context(), repo.OwnerID)
	if err != nil || owner == nil {
		http.Error(w, "Repository owner not found", http.StatusInternalServerError)
		return
	}
	req, err := decodeTriggerRequest(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req, err = normalizeCITrigger(r.Context(), app.Config.ReposPath, owner, repo, req, "push")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	policy, err := (cipolicy.Resolver{DB: app.DB, ReposPath: app.Config.ReposPath}).Resolve(r.Context(), owner, repo, req.Branch, req.Tag)
	if err != nil {
		slog.Warn("CI webhook denied because ref policy failed closed", "repo", repo.ID, "branch", req.Branch, "tag", req.Tag, "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ignored", "reason": "ref policy unavailable"})
		return
	}
	if !policy.AutoRun {
		slog.Info("CI webhook ignored by ref policy", "repo", repo.ID, "ref_type", policy.RefType, "ref", policy.RefName, "source", policy.Source)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ignored", "reason": "auto-run disabled for ref"})
		return
	}
	runID, err := app.DB.CreatePushCIRun(r.Context(), repo.ID, req.CommitHash, req.Branch, req.Tag)
	if err != nil {
		slog.Error("failed to create push CI run", "repo", repo.ID, "error", err)
		http.Error(w, "Failed to create CI run", http.StatusInternalServerError)
		return
	}
	slog.Info("CI run created", "run_id", runID, "repo", repo.ID, "event", req.Event, "policy_source", policy.Source)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
}

func listArtifacts(root string) []string {
	var artifacts []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err == nil {
			artifacts = append(artifacts, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(artifacts)
	return artifacts
}

func ciRunNavigationRef(run *models.CIRun) string {
	if run == nil {
		return ""
	}
	if run.Branch != "" {
		return run.Branch
	}
	if run.Tag != "" {
		return run.Tag
	}
	return run.CommitHash
}

func (app *App) HandleCIRunGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		app.renderError(w, r, PageData{User: GetUser(r)}, "CI run not found", http.StatusNotFound)
		return
	}

	repoPath := GetRepoPath(r)
	logContent, logOffset := readCILog(run.LogFile)
	cfg, configView := loadCIConfigView(r.Context(), repoPath, run.CommitHash)
	logView := parseCILog(logContent, run, cfg)

	artifactFiles := listArtifactFiles(artifactRunDir(app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID))
	artifactTree := buildArtifactTree(artifactFiles)
	decorateArtifactTreeURLs(artifactTree, owner.Username, repo.Name, run.ID)

	attemptRuns, err := app.DB.GetCIRunRetryChain(r.Context(), repo.ID, run.ID)
	if err != nil {
		slog.Warn("failed to load CI retry chain", "run_id", run.ID, "error", err)
		attemptRuns = []models.CIRun{*run}
	}
	attemptViews := loadCIRunViews(r.Context(), repoPath, attemptRuns)
	for i := range attemptViews {
		attemptViews[i].IsCurrent = attemptViews[i].Run.ID == run.ID
		attemptViews[i].AttemptNumber = i + 1
	}

	var commit *git.Commit
	views := loadCIRunViews(r.Context(), repoPath, []models.CIRun{*run})
	if len(views) == 1 {
		commit = views[0].Commit
	}

	app.renderPage(w, r, "repo_ci_run.html", PageData{
		Title:   fmt.Sprintf("Run %s — CI", shortString(run.ID, 8)),
		User:    GetUser(r),
		Success: strings.TrimSpace(r.URL.Query().Get("message")),
		Error:   strings.TrimSpace(r.URL.Query().Get("error")),
		RepoNav: app.repoNavData(r, ciRunNavigationRef(run)),
		Data: CIRunPageData{
			Owner: owner, Repository: repo, Run: run, Commit: commit,
			LogContent: logContent, LogOffset: logOffset, Log: logView, Config: configView,
			Artifacts: artifactTree, ArtifactCount: len(artifactFiles), ArtifactBytes: artifactTreeSize(artifactTree),
			Attempts:   attemptViews,
			CanControl: app.canControlCI(r.Context(), GetUser(r), repo),
		},
	})
}

var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func writeCILogFragment(w http.ResponseWriter, content string) {
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, content)
}

func (app *App) HandleCIRunLogGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("X-Gitman-CI-Status", run.Status)
	w.Header().Set("X-Gitman-CI-Reason", run.StatusReason)

	offsetValue, incremental := r.URL.Query()["offset"]
	if !incremental {
		if run.LogFile == "" {
			writeCILogFragment(w, "(log not yet available — worker is preparing the workspace)")
			return
		}
		data, err := os.ReadFile(run.LogFile)
		if err != nil {
			writeCILogFragment(w, "(log file not readable)")
			return
		}
		w.Header().Set("X-Gitman-Log-Offset", strconv.FormatInt(int64(len(data)), 10))
		writeCILogFragment(w, ansiEscapeRegex.ReplaceAllString(string(data), ""))
		return
	}

	offset := int64(0)
	if len(offsetValue) > 0 && strings.TrimSpace(offsetValue[0]) != "" {
		parsed, parseErr := strconv.ParseInt(strings.TrimSpace(offsetValue[0]), 10, 64)
		if parseErr != nil || parsed < 0 {
			http.Error(w, "invalid log offset", http.StatusBadRequest)
			return
		}
		offset = parsed
	}
	if run.LogFile == "" {
		w.Header().Set("X-Gitman-Log-Offset", "0")
		w.Header().Set("X-Gitman-Log-Pending", "true")
		writeCILogFragment(w, "")
		return
	}
	file, err := os.Open(run.LogFile)
	if err != nil {
		w.Header().Set("X-Gitman-Log-Offset", strconv.FormatInt(offset, 10))
		writeCILogFragment(w, "")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "log unavailable", http.StatusServiceUnavailable)
		return
	}
	if offset > stat.Size() {
		offset = 0
		w.Header().Set("X-Gitman-Log-Reset", "true")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		http.Error(w, "log unavailable", http.StatusServiceUnavailable)
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "log unavailable", http.StatusServiceUnavailable)
		return
	}
	nextOffset := offset + int64(len(data))
	w.Header().Set("X-Gitman-Log-Offset", strconv.FormatInt(nextOffset, 10))
	writeCILogFragment(w, ansiEscapeRegex.ReplaceAllString(string(data), ""))
}

func ciRunLogDir(artifactsPath, owner, repo, runID string) string {
	return filepath.Join(artifactsPath, "logs", owner, repo, runID)
}

func pathIsWithinDir(baseDir, candidate string) bool {
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func collectCIRunLogFiles(artifactsPath, owner, repo string, run *models.CIRun) ([]string, error) {
	if run == nil {
		return nil, nil
	}

	logDir := ciRunLogDir(artifactsPath, owner, repo, run.ID)
	seen := map[string]struct{}{}
	var logs []string
	addLog := func(path string) error {
		if path == "" || !pathIsWithinDir(logDir, path) {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if _, ok := seen[abs]; ok {
			return nil
		}
		seen[abs] = struct{}{}
		logs = append(logs, abs)
		return nil
	}

	entries, err := os.ReadDir(logDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if err := addLog(filepath.Join(logDir, entry.Name())); err != nil {
			return nil, err
		}
	}
	if err := addLog(run.LogFile); err != nil {
		return nil, err
	}
	sort.Strings(logs)
	return logs, nil
}

func (app *App) HandleCIRunLogsDownloadGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	logs, err := collectCIRunLogFiles(app.Config.ArtifactsPath, owner.Username, repo.Name, run)
	if err != nil {
		slog.Warn("failed to collect CI log files", "run", run.ID, "error", err)
		http.Error(w, "log files not readable", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("%s-%s-ci-logs.log", repo.Name, shortString(run.ID, 8))
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))

	if len(logs) == 0 {
		_, _ = io.WriteString(w, "log not yet available — worker is preparing the workspace\n")
		return
	}

	writeString := func(s string) bool {
		if _, err := io.WriteString(w, s); err != nil {
			slog.Warn("failed to write CI log download response", "run", run.ID, "error", err)
			return false
		}
		return true
	}
	writef := func(format string, args ...any) bool {
		return writeString(fmt.Sprintf(format, args...))
	}

	for i, logPath := range logs {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if !writef("=== %s ===\n(log file not readable: %v)\n", filepath.Base(logPath), err) {
				return
			}
			continue
		}
		if len(logs) > 1 {
			if i > 0 && !writeString("\n") {
				return
			}
			if !writef("=== %s ===\n", filepath.Base(logPath)) {
				return
			}
		}
		if !writeString(ansiEscapeRegex.ReplaceAllString(string(data), "")) {
			return
		}
		if len(data) > 0 && data[len(data)-1] != '\n' && !writeString("\n") {
			return
		}
	}
}

func (app *App) renderCISecretsPage(w http.ResponseWriter, r *http.Request, errStr, successStr string) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	secrets, err := app.DB.GetRepoSecrets(r.Context(), repo.ID)
	if err != nil {
		secrets = []models.RepoSecret{}
	}

	app.renderPage(w, r, "repo_ci_secrets.html", PageData{
		Title:   repo.Name + " - CI Secrets",
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

func (app *App) HandleCISecretsGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderError(w, r, PageData{User: currentUser}, "Forbidden", http.StatusForbidden)
		return
	}

	app.renderCISecretsPage(w, r, "", "")
}

func (app *App) HandleCISecretsAddPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderCISecretsPage(w, r, "Only the repository owner can manage secrets.", "")
		return
	}

	if app.Config.SecretKey == "" {
		app.renderCISecretsPage(w, r, "GITMAN_SECRET_KEY is not configured on this server. Secrets cannot be stored.", "")
		return
	}

	if err := r.ParseForm(); err != nil {
		app.renderCISecretsPage(w, r, "Invalid form data.", "")
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	value := r.FormValue("value")

	if key == "" || value == "" {
		app.renderCISecretsPage(w, r, "Key and value are required.", "")
		return
	}

	if !secretKeyRegex.MatchString(key) {
		app.renderCISecretsPage(w, r, "Key must be uppercase letters, digits, and underscores, starting with a letter.", "")
		return
	}

	encrypted, err := db.EncryptSecret(app.Config.SecretKey, value)
	if err != nil {
		app.renderCISecretsPage(w, r, "Failed to encrypt secret.", "")
		return
	}

	if err := app.DB.AddRepoSecret(r.Context(), repo.ID, key, encrypted); err != nil {
		app.renderCISecretsPage(w, r, "Failed to save secret.", "")
		return
	}

	app.renderCISecretsPage(w, r, "", fmt.Sprintf("Secret %q saved.", key))
}

func (app *App) HandleCISecretsDeletePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)
	secretID := chi.URLParam(r, "id")

	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderCISecretsPage(w, r, "Forbidden.", "")
		return
	}

	if err := app.DB.DeleteRepoSecret(r.Context(), secretID, repo.ID); err != nil {
		app.renderCISecretsPage(w, r, "Failed to delete secret.", "")
		return
	}

	app.renderCISecretsPage(w, r, "", "Secret deleted.")
}

func hookPath(reposPath, ownerUsername, repoName string) (string, error) {
	repoPath, err := git.SecureRepoPath(reposPath, ownerUsername, repoName)
	if err != nil {
		return "", err
	}
	return filepath.Join(repoPath, "hooks", "post-receive"), nil
}

const (
	gitmanHookPrefix = "# Managed by Gitman CI/CD."
	gitmanHookMarker = "# Managed by Gitman CI/CD. Durable queue format: 1"
)

type hookState string

const (
	hookAbsent    hookState = "absent"
	hookManaged   hookState = "managed"
	hookOutdated  hookState = "outdated"
	hookUnmanaged hookState = "unmanaged"
)

func detectHookState(path string) hookState {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return hookAbsent
	}
	if err != nil {
		return hookUnmanaged
	}
	if strings.Contains(string(data), gitmanHookMarker) {
		return hookManaged
	}
	if strings.Contains(string(data), gitmanHookPrefix) {
		return hookOutdated
	}
	return hookUnmanaged
}

func (app *App) hookState(ownerUsername, repoName string) hookState {
	hp, err := hookPath(app.Config.ReposPath, ownerUsername, repoName)
	if err != nil {
		return hookUnmanaged
	}
	return detectHookState(hp)
}

func hookIsInstalled(reposPath, ownerUsername, repoName string) bool {
	hp, err := hookPath(reposPath, ownerUsername, repoName)
	if err != nil {
		return false
	}
	return detectHookState(hp) == hookManaged
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

	hp, err := hookPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Invalid repository path", http.StatusInternalServerError)
		return
	}
	state := detectHookState(hp)
	if state == hookUnmanaged {
		app.renderError(w, r, PageData{User: currentUser}, "Refusing to overwrite unmanaged post-receive hook", http.StatusConflict)
		return
	}

	previousSecret, err := app.DB.GetWebhookSecret(r.Context(), repo.ID)
	if err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to read existing webhook secret", http.StatusInternalServerError)
		return
	}

	if err := os.MkdirAll(filepath.Dir(hp), 0o700); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to create hooks directory", http.StatusInternalServerError)
		return
	}

	// Durable local hooks have no credential. Revoke the legacy managed-hook
	// secret during upgrade so an obsolete bearer is not left active.
	if err := app.DB.SetWebhookSecret(r.Context(), repo.ID, ""); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to revoke legacy webhook secret", http.StatusInternalServerError)
		return
	}
	rollbackSecret := func() {
		if err := app.DB.SetWebhookSecret(r.Context(), repo.ID, previousSecret); err != nil {
			slog.Warn("failed to restore previous webhook secret", "repo", repo.ID, "error", err)
		}
	}
	script := buildHookScript(owner.Username, repo.Name)

	if err := writeExecutableFileAtomic(hp, script); err != nil {
		rollbackSecret()
		app.renderError(w, r, PageData{User: currentUser}, "Failed to write hook script", http.StatusInternalServerError)
		return
	}

	slog.Info("post-receive hook installed", "repo", repo.ID, "by", currentUser.Username, "previous_state", state)
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci?success=hook_installed", owner.Username, repo.Name), http.StatusSeeOther)
}

// HandleCIHookUninstallPOST removes the Gitman-managed post-receive hook from the bare repo.
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
	state := detectHookState(hp)
	if state == hookUnmanaged {
		app.renderError(w, r, PageData{User: currentUser}, "Refusing to remove unmanaged post-receive hook", http.StatusConflict)
		return
	}

	if err := app.DB.SetWebhookSecret(r.Context(), repo.ID, ""); err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to revoke webhook secret; hook was not removed", http.StatusInternalServerError)
		return
	}
	if state == hookManaged || state == hookOutdated {
		if err := os.Remove(hp); err != nil && !os.IsNotExist(err) {
			app.renderError(w, r, PageData{User: currentUser}, "Webhook secret revoked, but the inert hook file could not be removed", http.StatusInternalServerError)
			return
		}
	}

	slog.Info("post-receive hook uninstalled", "repo", repo.ID, "by", currentUser.Username, "previous_state", state)
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci", owner.Username, repo.Name), http.StatusSeeOther)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func writeExecutableFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".post-receive-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func buildHookScript(ownerUsername, repoName string) string {
	return fmt.Sprintf(`#!/bin/bash
%s
# Durable local delivery: events remain queued while the web process is down.
GITMAN_OWNER=%s
GITMAN_REPO=%s
HOOK_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
QUEUE_DIR="$HOOK_DIR/%s"
SEQUENCE_FILE="$QUEUE_DIR/.sequence"
umask 077

if ! mkdir -p "$QUEUE_DIR"; then
    command -v logger >/dev/null 2>&1 && logger -t gitman-ci-hook -- "cannot create CI queue for $GITMAN_OWNER/$GITMAN_REPO"
    exit 0
fi

while read -r old new ref; do
    if [[ "$ref" != refs/heads/* && "$ref" != refs/tags/* ]]; then
        continue
    fi
    if [[ "$new" =~ ^0+$ ]]; then
        continue
    fi
    tmp="$(mktemp "$QUEUE_DIR/.event.XXXXXXXXXXXX")" || continue
    if printf '%%s\n%%s\n%%s\n' "$old" "$new" "$ref" > "$tmp"; then
        (
            flock -x 9 || exit 1
            sequence=0
            if [[ -f "$SEQUENCE_FILE" ]]; then
                IFS= read -r sequence < "$SEQUENCE_FILE" || exit 1
                [[ "$sequence" =~ ^[0-9]+$ ]] || exit 1
            fi
            next=$((10#$sequence + 1))
            while [[ -e "$(printf "$QUEUE_DIR/event-%%020d" "$next")" ||
                     -e "$(printf "$QUEUE_DIR/.pending-event-%%020d" "$next")" ||
                     -e "$(printf "$QUEUE_DIR/.processing-event-%%020d" "$next")" ]]; do
                next=$((next + 1))
            done
            pending="$(printf "$QUEUE_DIR/.pending-event-%%020d" "$next")"
            if ! mv "$tmp" "$pending" || ! sync -f "$QUEUE_DIR"; then
                exit 1
            fi
            sequence_tmp="$(mktemp "$QUEUE_DIR/.sequence.XXXXXXXXXXXX")" || exit 1
            if ! printf '%%s\n' "$next" > "$sequence_tmp" ||
               ! sync -f "$sequence_tmp" ||
               ! mv -f "$sequence_tmp" "$SEQUENCE_FILE"; then
                rm -f "$sequence_tmp"
                exit 1
            fi
            final="$(printf "$QUEUE_DIR/event-%%020d" "$next")"
            mv "$pending" "$final" && sync -f "$QUEUE_DIR"
        ) 9>"$QUEUE_DIR/.sequence.lock"
        if [[ -e "$tmp" ]]; then
            command -v logger >/dev/null 2>&1 && logger -t gitman-ci-hook -- "cannot persist CI event for $GITMAN_OWNER/$GITMAN_REPO"
            rm -f "$tmp"
        fi
    else
        rm -f "$tmp"
    fi
done
exit 0
`, gitmanHookMarker, shellQuote(ownerUsername), shellQuote(repoName), ciHookQueueDirName)
}

// Artifact endpoints use ?ref=<branch-or-tag> and a wildcard artifact path so
// nested files and refs containing slashes are representable.
func artifactPathParam(r *http.Request) string {
	return strings.TrimPrefix(chi.URLParam(r, "*"), "/")
}

func splitLegacyArtifactPath(raw string) (string, string) {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func (app *App) HandleArtifactByBranch(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	branch := strings.TrimSpace(r.URL.Query().Get("ref"))
	artifact := artifactPathParam(r)
	if branch == "" {
		branch, artifact = splitLegacyArtifactPath(artifact)
	}
	if git.ValidateRefName(branch) != nil || artifact == "" {
		http.Error(w, "Invalid branch or artifact path", http.StatusBadRequest)
		return
	}
	run, err := app.DB.GetLatestSuccessfulRunForBranch(r.Context(), repo.ID, branch)
	if err != nil || run == nil {
		http.Error(w, "No successful run found for branch", http.StatusNotFound)
		return
	}
	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID, artifact)
}

func (app *App) HandleArtifactByTag(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	tag := strings.TrimSpace(r.URL.Query().Get("ref"))
	artifact := artifactPathParam(r)
	if tag == "" {
		tag, artifact = splitLegacyArtifactPath(artifact)
	}
	if git.ValidateRefName(tag) != nil || artifact == "" {
		http.Error(w, "Invalid tag or artifact path", http.StatusBadRequest)
		return
	}
	run, err := app.DB.GetSuccessfulRunForTag(r.Context(), repo.ID, tag)
	if err != nil || run == nil {
		http.Error(w, "No successful run found for tag", http.StatusNotFound)
		return
	}
	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID, artifact)
}

func (app *App) HandleArtifactByCommit(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	commit := chi.URLParam(r, "commit_hash")
	artifact := artifactPathParam(r)
	run, err := app.DB.GetSuccessfulRunForCommit(r.Context(), repo.ID, commit)
	if err != nil || run == nil {
		http.Error(w, "No successful run found for commit", http.StatusNotFound)
		return
	}
	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID, artifact)
}

func (app *App) HandleArtifactByRunID(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}
	serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, runID, run.AttemptID, artifactPathParam(r))
}

func (app *App) HandleCIRunArtifactPreviewGET(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.DB.GetCIRunByID(r.Context(), runID)
	if err != nil || run == nil || run.RepoID != repo.ID {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}
	artifact := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(artifactPathParam(r), "/")))
	if artifact == "." || artifact == "" || filepath.IsAbs(artifact) || artifact == ".." || strings.HasPrefix(artifact, ".."+string(filepath.Separator)) {
		http.Error(w, "Invalid artifact path", http.StatusBadRequest)
		return
	}
	baseDir := artifactRunDir(app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID)
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	requestedAbs, err := filepath.Abs(filepath.Join(baseDir, artifact))
	if err != nil {
		http.Error(w, "Invalid artifact path", http.StatusBadRequest)
		return
	}
	rel, err := filepath.Rel(baseAbs, requestedAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	current := baseAbs
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			http.Error(w, "Artifact not found", http.StatusNotFound)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}
	info, err := os.Stat(requestedAbs)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}
	if !artifactLooksPreviewable(requestedAbs, info.Size()) {
		http.Error(w, "Artifact is not previewable as text", http.StatusUnsupportedMediaType)
		return
	}
	data, err := os.ReadFile(requestedAbs)
	if err != nil {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(data)
}

func artifactRunDir(artifactsPath, owner, repo, runID, attemptID string) string {
	base := filepath.Join(artifactsPath, "files", owner, repo, runID)
	if attemptID == "" {
		return base
	}
	return filepath.Join(base, attemptID)
}

func serveArtifact(w http.ResponseWriter, r *http.Request, artifactsPath, owner, repo, runID, attemptID, artifact string) {
	noStore(w)
	artifact = filepath.Clean(filepath.FromSlash(strings.TrimPrefix(artifact, "/")))
	if artifact == "." || artifact == "" || filepath.IsAbs(artifact) || artifact == ".." || strings.HasPrefix(artifact, ".."+string(filepath.Separator)) {
		http.Error(w, "Invalid artifact path", http.StatusBadRequest)
		return
	}

	baseDir := artifactRunDir(artifactsPath, owner, repo, runID, attemptID)
	requestedPath := filepath.Join(baseDir, artifact)
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	requestedAbs, err := filepath.Abs(requestedPath)
	if err != nil {
		http.Error(w, "Invalid artifact path", http.StatusBadRequest)
		return
	}
	rel, err := filepath.Rel(baseAbs, requestedAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	current := baseAbs
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			http.Error(w, "Artifact not found", http.StatusNotFound)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	f, err := os.Open(requestedAbs)
	if err != nil {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("failed to close artifact download file", "path", requestedAbs, "error", err)
		}
	}()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(artifact)})
	w.Header().Set("Content-Disposition", disposition)
	http.ServeContent(w, r, filepath.Base(artifact), stat.ModTime(), f)
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
	case "cancelled":
		return "Cancelled", "badge-cancelled"
	default:
		return status, "badge-unknown"
	}
}

// FormatDuration returns a human-readable elapsed time for a CI run.
func FormatDuration(run *models.CIRun) string {
	if run == nil {
		return ""
	}
	start := run.CreatedAt
	if run.StartedAt != nil {
		start = *run.StartedAt
	}
	end := time.Now()
	if run.CompletedAt != nil {
		end = *run.CompletedAt
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

// FormatQueueDuration returns how long the run waited before execution.
func FormatQueueDuration(run *models.CIRun) string {
	if run == nil {
		return ""
	}
	end := time.Now()
	if run.StartedAt != nil {
		end = *run.StartedAt
	} else if run.CompletedAt != nil {
		end = *run.CompletedAt
	}
	d := end.Sub(run.CreatedAt)
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}
