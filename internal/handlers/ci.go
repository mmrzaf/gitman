package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
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

	statusFilter := models.CIStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if statusFilter != "" && !statusFilter.Valid() {
		app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Invalid CI status filter"))
		return
	}
	branchFilter := strings.TrimSpace(r.URL.Query().Get("branch"))
	if branchFilter != "" {
		if err := git.ValidateRefNameContext(ctx, branchFilter); err != nil {
			if errors.Is(err, git.ErrInvalidRef) {
				app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Invalid branch filter"))
			} else {
				app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
			}
			return
		}
	}

	runs, err := app.DB.GetCIRunsByRepoFiltered(ctx, repo.ID, statusFilter, branchFilter, 100)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		return
	}

	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err))
		return
	}
	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}

	var branches, tags []string
	defaultBranch := ""
	if !isEmpty {
		if branches, err = git.GetBranches(ctx, repoPath); err != nil {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
			return
		}
		if tags, err = git.GetTags(ctx, repoPath); err != nil {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
			return
		}
		if defaultBranch, err = git.GetDefaultBranch(ctx, repoPath); err != nil {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
			return
		}
	}

	refRules, err := app.DB.ListRepoCIRefRules(ctx, repo.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI settings are temporarily unavailable", err))
		return
	}

	runViews, err := loadCIRunViews(ctx, repoPath, runs)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	hookExists := hookIsInstalled(app.Config.ReposPath, owner.Username, repo.Name)
	hookState := app.hookState(owner.Username, repo.Name)
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
			StatusFilter:  string(statusFilter),
			BranchFilter:  branchFilter,
		},
	})
}

func (app *App) canViewCI(ctx context.Context, user *models.User, repo *models.Repository) bool {
	if user == nil || repo == nil {
		return false
	}
	if member, ok := ctx.Value(repoMemberContextKey).(bool); ok {
		return member
	}
	return user.ID == repo.OwnerID
}

func (app *App) canControlCI(ctx context.Context, user *models.User, repo *models.Repository) bool {
	if user == nil || repo == nil {
		return false
	}
	if canWrite, ok := ctx.Value(repoWriteContextKey).(bool); ok {
		return canWrite
	}
	return user.ID == repo.OwnerID
}

func (app *App) HandleCISettingsRulePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	if !app.parseWebForm(w, r) {
		return
	}
	refType := models.CIRefType(strings.TrimSpace(r.FormValue("ref_type")))
	refName := strings.TrimSpace(r.FormValue("ref_name"))
	if !refType.Valid() {
		app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Invalid CI ref type"))
		return
	}
	if refName == "" || strings.ContainsAny(refName, "\x00\r\n") {
		app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Invalid CI ref name"))
		return
	}
	refIsPattern := strings.ContainsAny(refName, "*?[")
	if refIsPattern {
		if _, err := path.Match(refName, "test"); err != nil {
			app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Invalid CI ref pattern"))
			return
		}
	} else {
		if err := git.ValidateRefNameContext(r.Context(), refName); err != nil {
			if errors.Is(err, git.ErrInvalidRef) {
				app.respondWebError(w, r, apperr.Wrap(apperr.KindInvalid, "Invalid CI ref name", err))
			} else {
				app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
			}
			return
		}
		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
		if err != nil {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err))
			return
		}
		if refType == models.CIRefBranch {
			if _, err := git.ResolveBranchCommitHash(r.Context(), repoPath, refName); err != nil {
				if errors.Is(err, git.ErrRefNotFound) || errors.Is(err, git.ErrInvalidRef) {
					app.respondWebError(w, r, apperr.Wrap(apperr.KindInvalid, "Branch does not resolve in repository", err))
				} else {
					app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
				}
				return
			}
		} else {
			if _, err := git.ResolveTagCommitHash(r.Context(), repoPath, refName); err != nil {
				if errors.Is(err, git.ErrRefNotFound) || errors.Is(err, git.ErrInvalidRef) {
					app.respondWebError(w, r, apperr.Wrap(apperr.KindInvalid, "Tag does not resolve in repository", err))
				} else {
					app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
				}
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
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI settings are temporarily unavailable", err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci?success=ci_rule_saved", owner.Username, repo.Name), http.StatusSeeOther)
}

func (app *App) HandleCISettingsRuleDeletePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	if !app.parseWebForm(w, r) {
		return
	}
	refType := models.CIRefType(strings.TrimSpace(r.FormValue("ref_type")))
	refName := strings.TrimSpace(r.FormValue("ref_name"))
	if !refType.Valid() {
		app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Invalid CI ref type"))
		return
	}
	if err := app.DB.DeleteRepoCIRefRule(r.Context(), repo.ID, refType, refName); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "CI ref rule not found"))
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI settings are temporarily unavailable", err))
		}
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/ci?success=ci_rule_deleted", owner.Username, repo.Name), http.StatusSeeOther)
}

type triggerRequest struct {
	CommitHash string         `json:"commit_hash"`
	Branch     string         `json:"branch"`
	Tag        string         `json:"tag"`
	Event      models.CIEvent `json:"event"`
}

func ciTriggerDecodeError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return apperr.Wrap(apperr.KindTooLarge, "CI request body too large", err)
	}
	return apperr.Wrap(apperr.KindInvalid, "Invalid CI request", err)
}

func requestMediaType(r *http.Request) (string, error) {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", apperr.Wrap(apperr.KindInvalid, "Invalid Content-Type", err)
	}
	return strings.ToLower(mediaType), nil
}

func decodeTriggerRequest(w http.ResponseWriter, r *http.Request) (triggerRequest, error) {
	var req triggerRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	mediaType, err := requestMediaType(r)
	if err != nil {
		return req, err
	}
	switch mediaType {
	case "application/json":
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return req, ciTriggerDecodeError(fmt.Errorf("decode JSON: %w", err))
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			if err != nil {
				return req, ciTriggerDecodeError(fmt.Errorf("decode trailing JSON: %w", err))
			}
			return req, apperr.New(apperr.KindInvalid, "CI request must contain exactly one JSON object")
		}
		return req, nil
	case "", "application/x-www-form-urlencoded", "multipart/form-data":
		if err := r.ParseForm(); err != nil {
			return req, ciTriggerDecodeError(fmt.Errorf("parse form: %w", err))
		}
		req.CommitHash = r.FormValue("commit_hash")
		req.Branch = r.FormValue("branch")
		req.Tag = r.FormValue("tag")
		req.Event = models.CIEvent(r.FormValue("event"))
		return req, nil
	default:
		return req, apperr.New(apperr.KindUnsupported, "Unsupported CI request content type")
	}
}

func normalizeCITrigger(ctx context.Context, reposPath string, owner *models.User, repo *models.Repository, req triggerRequest, defaultEvent models.CIEvent) (triggerRequest, error) {
	req.CommitHash = strings.TrimSpace(req.CommitHash)
	req.Branch = strings.TrimSpace(req.Branch)
	req.Tag = strings.TrimSpace(req.Tag)
	req.Event = models.CIEvent(strings.TrimSpace(string(req.Event)))

	invalid := func(message string, err error) (triggerRequest, error) {
		return req, apperr.Wrap(apperr.KindInvalid, message, err)
	}
	unavailable := func(message string, err error) (triggerRequest, error) {
		return req, apperr.Wrap(apperr.KindUnavailable, message, err)
	}

	for name, value := range map[string]string{
		"commit_hash": req.CommitHash,
		"branch":      req.Branch,
		"tag":         req.Tag,
		"event":       string(req.Event),
	} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return invalid(fmt.Sprintf("%s contains unsupported control characters", name), nil)
		}
	}
	if req.Branch != "" && req.Tag != "" {
		return invalid("branch and tag are mutually exclusive", nil)
	}
	if req.Branch != "" {
		if err := git.ValidateRefNameContext(ctx, req.Branch); err != nil {
			if errors.Is(err, git.ErrInvalidRef) {
				return invalid("invalid branch", err)
			}
			return unavailable("Repository data is temporarily unavailable", err)
		}
	}
	if req.Tag != "" {
		if err := git.ValidateRefNameContext(ctx, req.Tag); err != nil {
			if errors.Is(err, git.ErrInvalidRef) {
				return invalid("invalid tag", err)
			}
			return unavailable("Repository data is temporarily unavailable", err)
		}
	}

	switch defaultEvent {
	case models.CIEventPush:
		req.Event = models.CIEventPush
		if req.Branch == "" && req.Tag == "" {
			return invalid("push event requires a branch or tag", nil)
		}
	case models.CIEventManual:
		req.Event = models.CIEventManual
	default:
		return invalid("invalid CI event", nil)
	}

	repoPath, err := git.SecureRepoPath(reposPath, owner.Username, repo.Name)
	if err != nil {
		return req, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err)
	}
	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		return unavailable("Repository data is temporarily unavailable", err)
	}
	if isEmpty {
		return invalid("repository is empty", nil)
	}

	var requestedRefHash string
	if req.Branch != "" {
		requestedRefHash, err = git.ResolveBranchCommitHash(ctx, repoPath, req.Branch)
		if err != nil {
			if errors.Is(err, git.ErrRefNotFound) || errors.Is(err, git.ErrInvalidRef) {
				return invalid("branch does not resolve in repository", err)
			}
			return unavailable("Repository data is temporarily unavailable", err)
		}
	} else if req.Tag != "" {
		requestedRefHash, err = git.ResolveTagCommitHash(ctx, repoPath, req.Tag)
		if err != nil {
			if errors.Is(err, git.ErrRefNotFound) || errors.Is(err, git.ErrInvalidRef) {
				return invalid("tag does not resolve in repository", err)
			}
			return unavailable("Repository data is temporarily unavailable", err)
		}
	}

	if req.CommitHash == "" {
		if requestedRefHash != "" {
			req.CommitHash = requestedRefHash
		} else {
			resolvedRef, resolveErr := git.ResolveRefInfo(ctx, repoPath, "")
			if resolveErr != nil {
				return unavailable("Repository data is temporarily unavailable", resolveErr)
			}
			resolvedHash, resolveErr := git.ResolveRevisionCommitHash(ctx, repoPath, resolvedRef.Name)
			if resolveErr != nil {
				return unavailable("Repository data is temporarily unavailable", resolveErr)
			}
			req.CommitHash = resolvedHash
			if resolvedRef.Kind == git.RefKindBranch {
				req.Branch = resolvedRef.Name
				requestedRefHash = resolvedHash
			}
		}
	}

	resolvedHash, err := git.ResolveCommitHash(ctx, repoPath, req.CommitHash)
	if err != nil {
		if errors.Is(err, git.ErrRefNotFound) || errors.Is(err, git.ErrInvalidCommit) {
			return invalid("commit does not resolve in repository", err)
		}
		return unavailable("Repository data is temporarily unavailable", err)
	}
	req.CommitHash = resolvedHash

	if req.Tag != "" && resolvedHash != requestedRefHash {
		return invalid("commit does not match tag", nil)
	}
	if req.Branch != "" {
		if req.Event == models.CIEventPush {
			if resolvedHash != requestedRefHash {
				return invalid("commit does not match pushed branch tip", nil)
			}
		} else {
			reachable, reachErr := git.IsCommitReachableFromBranch(ctx, repoPath, resolvedHash, req.Branch)
			if reachErr != nil {
				return unavailable("Repository data is temporarily unavailable", reachErr)
			}
			if !reachable {
				return invalid("commit is not reachable from branch", nil)
			}
		}
	}
	return req, nil
}

func (app *App) createCIRun(w http.ResponseWriter, r *http.Request, repo *models.Repository, owner *models.User, defaultEvent models.CIEvent) (string, error) {
	req, err := decodeTriggerRequest(w, r)
	if err != nil {
		return "", err
	}
	req, err = normalizeCITrigger(r.Context(), app.Config.ReposPath, owner, repo, req, defaultEvent)
	if err != nil {
		return "", err
	}
	var runID string
	if req.Event == models.CIEventPush {
		runID, err = app.DB.CreatePushCIRun(r.Context(), repo.ID, req.CommitHash, req.Branch, req.Tag)
	} else {
		runID, err = app.DB.CreateCIRun(r.Context(), repo.ID, req.CommitHash, req.Branch, req.Tag, req.Event)
	}
	if err != nil {
		return "", apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err)
	}
	slog.Info("CI run created", "request_id", RequestID(r), "run_id", runID, "repo", repo.ID, "event", req.Event)
	return runID, nil
}

func (app *App) HandleCITriggerPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	mediaType, mediaTypeErr := requestMediaType(r)
	jsonResponse := mediaTypeErr == nil && mediaType == "application/json"
	respondError := func(err error) {
		if jsonResponse {
			app.respondAPIError(w, r, err)
			return
		}
		app.respondWebError(w, r, err)
	}
	if currentUser == nil {
		respondError(apperr.New(apperr.KindUnauthenticated, "authentication required"))
		return
	}
	if !app.canControlCI(r.Context(), currentUser, repo) {
		respondError(apperr.New(apperr.KindForbidden, "repository access denied"))
		return
	}

	runID, err := app.createCIRun(w, r, repo, owner, models.CIEventManual)
	if err != nil {
		respondError(err)
		return
	}
	if jsonResponse {
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

func (app *App) getRepoCIRun(ctx context.Context, repoID, runID string) (*models.CIRun, error) {
	run, err := app.DB.GetCIRunByID(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.RepoID != repoID {
		return nil, db.ErrNotFound
	}
	return run, nil
}

func (app *App) HandleCIRunCancelPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	user := GetUser(r)
	if !app.canControlCI(r.Context(), user, repo) {
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	runID := chi.URLParam(r, "run_id")
	_, err := app.getRepoCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "CI run not found"))
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	cancelled, err := app.DB.CancelCIRun(r.Context(), repo.ID, runID, "Cancelled by "+user.Username)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
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
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	runID := chi.URLParam(r, "run_id")
	newRunID, err := app.DB.RetryCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			app.respondWebError(w, r, apperr.Wrap(apperr.KindNotFound, "CI run not found", err))
		case errors.Is(err, db.ErrCIRunNotRetryable):
			redirectCIRun(w, r, owner.Username, repo.Name, runID, "", "Only completed runs can be retried.")
		default:
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	slog.Info("CI run retried", "run_id", newRunID, "retry_of", runID, "repo", repo.ID, "user", user.Username)
	redirectCIRun(w, r, owner.Username, repo.Name, newRunID, "Retry queued.", "")
}

func (app *App) HandleCITriggerWebhook(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner, err := app.DB.GetUserByID(r.Context(), repo.OwnerID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindInternal, "Repository owner is unavailable", err))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
		}
		return
	}
	req, err := decodeTriggerRequest(w, r)
	if err != nil {
		app.respondAPIError(w, r, err)
		return
	}
	req, err = normalizeCITrigger(r.Context(), app.Config.ReposPath, owner, repo, req, models.CIEventPush)
	if err != nil {
		app.respondAPIError(w, r, err)
		return
	}
	policy, err := (cipolicy.Resolver{DB: app.DB, ReposPath: app.Config.ReposPath}).Resolve(r.Context(), owner, repo, req.Branch, req.Tag)
	if err != nil {
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI ref policy is temporarily unavailable", err))
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
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		return
	}
	slog.Info("CI run created", "run_id", runID, "repo", repo.ID, "event", req.Event, "policy_source", policy.Source)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
}

func listArtifacts(root string) ([]string, error) {
	files, err := listArtifactFiles(root)
	if err != nil {
		return nil, err
	}
	artifacts := make([]string, 0, len(files))
	for _, file := range files {
		artifacts = append(artifacts, file.Path)
	}
	return artifacts, nil
}

func ciRunNavigationRef(run *models.CIRun) string {
	if run == nil {
		return ""
	}
	if run.CommitHash != "" {
		return run.CommitHash
	}
	if run.Branch != "" {
		return run.Branch
	}
	return run.Tag
}

func (app *App) HandleCIRunGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.getRepoCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "CI run not found"))
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}

	repoPath := GetRepoPath(r)
	logContent, logOffset, err := readCILog(run.LogFile)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI log storage is temporarily unavailable", err))
		return
	}
	cfg, configView, err := loadCIConfigView(r.Context(), repoPath, run.CommitHash)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	logView := parseCILog(logContent, run, cfg)

	artifactFiles, err := listArtifactFiles(artifactRunDir(app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID))
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Artifact storage is temporarily unavailable", err))
		return
	}
	artifactTree := buildArtifactTree(artifactFiles)
	decorateArtifactTreeURLs(artifactTree, owner.Username, repo.Name, run.ID)

	attemptRuns, err := app.DB.GetCIRunRetryChain(r.Context(), repo.ID, run.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		return
	}
	attemptViews, err := loadCIRunViews(r.Context(), repoPath, attemptRuns)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	for i := range attemptViews {
		attemptViews[i].IsCurrent = attemptViews[i].Run.ID == run.ID
		attemptViews[i].AttemptNumber = i + 1
	}

	var commit *git.Commit
	views, err := loadCIRunViews(r.Context(), repoPath, []models.CIRun{*run})
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
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

func writeCILogFragment(w http.ResponseWriter, content string) {
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, content)
}

func ciLogUnavailableText(run *models.CIRun) string {
	if run != nil && (run.Status == models.CIStatusPending || run.Status == models.CIStatusRunning) {
		return "log not yet available — worker is preparing the workspace"
	}
	return "no build log is available for this run"
}

func (app *App) HandleCIRunLogGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.getRepoCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "CI run not found"))
		} else {
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	w.Header().Set("X-Gitman-CI-Status", string(run.Status))
	w.Header().Set("X-Gitman-CI-Reason", run.StatusReason)

	offsetValue, incremental := r.URL.Query()["offset"]
	if !incremental {
		if run.LogFile == "" {
			writeCILogFragment(w, "("+ciLogUnavailableText(run)+")")
			return
		}
		data, err := os.ReadFile(run.LogFile)
		if err != nil {
			if os.IsNotExist(err) {
				writeCILogFragment(w, "("+ciLogUnavailableText(run)+")")
				return
			}
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI log is temporarily unavailable", err))
			return
		}
		consume := completeCILogPrefixLen(data)
		data = data[:consume]
		w.Header().Set("X-Gitman-Log-Offset", strconv.FormatInt(int64(consume), 10))
		writeCILogFragment(w, stripANSI(data))
		return
	}

	offset := int64(0)
	if len(offsetValue) > 0 && strings.TrimSpace(offsetValue[0]) != "" {
		parsed, parseErr := strconv.ParseInt(strings.TrimSpace(offsetValue[0]), 10, 64)
		if parseErr != nil || parsed < 0 {
			app.respondPlainError(w, r, apperr.New(apperr.KindInvalid, "invalid log offset"))
			return
		}
		offset = parsed
	}
	if run.LogFile == "" {
		w.Header().Set("X-Gitman-Log-Offset", "0")
		if run.Status == models.CIStatusPending || run.Status == models.CIStatusRunning {
			w.Header().Set("X-Gitman-Log-Pending", "true")
			writeCILogFragment(w, "")
		} else {
			writeCILogFragment(w, ciLogUnavailableText(run)+"\n")
		}
		return
	}
	file, err := os.Open(run.LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("X-Gitman-Log-Offset", strconv.FormatInt(offset, 10))
			if run.Status == models.CIStatusPending || run.Status == models.CIStatusRunning {
				w.Header().Set("X-Gitman-Log-Pending", "true")
				writeCILogFragment(w, "")
			} else {
				writeCILogFragment(w, ciLogUnavailableText(run)+"\n")
			}
			return
		}
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI log is temporarily unavailable", err))
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI log is temporarily unavailable", err))
		return
	}
	if offset > stat.Size() {
		offset = 0
		w.Header().Set("X-Gitman-Log-Reset", "true")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI log is temporarily unavailable", err))
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI log is temporarily unavailable", err))
		return
	}
	consume := completeCILogPrefixLen(data)
	data = data[:consume]
	nextOffset := offset + int64(consume)
	w.Header().Set("X-Gitman-Log-Offset", strconv.FormatInt(nextOffset, 10))
	writeCILogFragment(w, stripANSI(data))
}

// completeUTF8PrefixLen avoids splitting a valid UTF-8 rune across incremental
// log responses. Invalid byte sequences are consumed normally so a malformed
// log cannot permanently stall the stream at one offset.
func completeCILogPrefixLen(data []byte) int {
	limit := completeUTF8PrefixLen(data)
	data = data[:limit]
	escape := bytes.LastIndexByte(data, 0x1b)
	if escape < 0 {
		return limit
	}
	suffix := data[escape:]
	if len(suffix) == 1 {
		return escape
	}
	switch suffix[1] {
	case '[': // CSI: ESC [ ... final-byte
		for i := 2; i < len(suffix); i++ {
			if suffix[i] >= 0x40 && suffix[i] <= 0x7e {
				return limit
			}
		}
		return escape
	case ']': // OSC: ESC ] ... BEL or ESC \
		for i := 2; i < len(suffix); i++ {
			if suffix[i] == 0x07 || (suffix[i] == 0x1b && i+1 < len(suffix) && suffix[i+1] == '\\') {
				return limit
			}
		}
		return escape
	default:
		return limit
	}
}

func stripANSI(data []byte) string {
	if bytes.IndexByte(data, 0x1b) < 0 {
		return string(data)
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != 0x1b {
			out = append(out, data[i])
			i++
			continue
		}
		if i+1 >= len(data) {
			break
		}
		switch data[i+1] {
		case '[':
			j := i + 2
			for j < len(data) && !(data[j] >= 0x40 && data[j] <= 0x7e) {
				j++
			}
			if j < len(data) {
				i = j + 1
			} else {
				i = len(data)
			}
		case ']':
			j := i + 2
			for j < len(data) {
				if data[j] == 0x07 {
					j++
					break
				}
				if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			// Two-byte escape sequence. Do not surface terminal control bytes.
			i += 2
		}
	}
	return string(out)
}

func completeUTF8PrefixLen(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	start := len(data) - 1
	min := len(data) - utf8.UTFMax
	if min < 0 {
		min = 0
	}
	for start >= min {
		if utf8.RuneStart(data[start]) {
			if utf8.FullRune(data[start:]) {
				return len(data)
			}
			return start
		}
		start--
	}
	return len(data)
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
	run, err := app.getRepoCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "CI run not found"))
		} else {
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}

	logs, err := collectCIRunLogFiles(app.Config.ArtifactsPath, owner.Username, repo.Name, run)
	if err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI logs are temporarily unavailable", err))
		return
	}

	filename := fmt.Sprintf("%s-%s-ci-logs.log", repo.Name, shortString(run.ID, 8))
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))

	if len(logs) == 0 {
		_, _ = io.WriteString(w, ciLogUnavailableText(run)+"\n")
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
			slog.Warn("CI log became unreadable during download",
				"request_id", RequestID(r),
				"run", run.ID,
				"log", filepath.Base(logPath),
				"error", err,
			)
			if !writef("=== %s ===\n(log file unavailable)\n", filepath.Base(logPath)) {
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
		if !writeString(stripANSI(data)) {
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
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI secrets are temporarily unavailable", err))
		return
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
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
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

	if !app.parseWebForm(w, r) {
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
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not encrypt the secret", err))
		return
	}

	if err := app.DB.AddRepoSecret(r.Context(), repo.ID, key, encrypted); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI secrets are temporarily unavailable", err))
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
		if errors.Is(err, db.ErrNotFound) {
			app.renderCISecretsPage(w, r, "Secret not found.", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI secrets are temporarily unavailable", err))
		}
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
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}

	if !app.parseWebForm(w, r) {
		return
	}

	hp, err := hookPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err))
		return
	}
	state := detectHookState(hp)
	if state == hookUnmanaged {
		app.respondWebError(w, r, apperr.New(apperr.KindConflict, "Refusing to overwrite unmanaged post-receive hook"))
		return
	}

	previousSecret, err := app.DB.GetWebhookSecret(r.Context(), repo.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI hook state is temporarily unavailable", err))
		return
	}

	if err := os.MkdirAll(filepath.Dir(hp), 0o700); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository hook storage is temporarily unavailable", err))
		return
	}

	// Durable local hooks have no credential. Revoke the legacy managed-hook
	// secret during upgrade so an obsolete bearer is not left active.
	if err := app.DB.SetWebhookSecret(r.Context(), repo.ID, ""); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI hook state is temporarily unavailable", err))
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
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository hook storage is temporarily unavailable", err))
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
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}

	hp, err := hookPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err))
		return
	}
	state := detectHookState(hp)
	if state == hookUnmanaged {
		app.respondWebError(w, r, apperr.New(apperr.KindConflict, "Refusing to remove unmanaged post-receive hook"))
		return
	}

	if err := app.DB.SetWebhookSecret(r.Context(), repo.ID, ""); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI hook state is temporarily unavailable", err))
		return
	}
	if state == hookManaged || state == hookOutdated {
		if err := os.Remove(hp); err != nil && !os.IsNotExist(err) {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository hook storage is temporarily unavailable", err))
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
	if branch == "" || artifact == "" {
		app.respondAPIError(w, r, apperr.New(apperr.KindInvalid, "invalid branch or artifact path"))
		return
	}
	if err := git.ValidateRefNameContext(r.Context(), branch); err != nil {
		if errors.Is(err, git.ErrInvalidRef) {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindInvalid, "invalid branch or artifact path", err))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
		}
		return
	}
	run, err := app.DB.GetLatestSuccessfulRunForBranch(r.Context(), repo.ID, branch)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "no successful run found for branch"))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	app.serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID, artifact)
}

func (app *App) HandleArtifactByTag(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	tag := strings.TrimSpace(r.URL.Query().Get("ref"))
	artifact := artifactPathParam(r)
	if tag == "" {
		tag, artifact = splitLegacyArtifactPath(artifact)
	}
	if tag == "" || artifact == "" {
		app.respondAPIError(w, r, apperr.New(apperr.KindInvalid, "invalid tag or artifact path"))
		return
	}
	if err := git.ValidateRefNameContext(r.Context(), tag); err != nil {
		if errors.Is(err, git.ErrInvalidRef) {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindInvalid, "invalid tag or artifact path", err))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository refs are temporarily unavailable", err))
		}
		return
	}
	run, err := app.DB.GetSuccessfulRunForTag(r.Context(), repo.ID, tag)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "no successful run found for tag"))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	app.serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID, artifact)
}

func (app *App) HandleArtifactByCommit(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	commit := chi.URLParam(r, "commit_hash")
	artifact := artifactPathParam(r)
	run, err := app.DB.GetSuccessfulRunForCommit(r.Context(), repo.ID, commit)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "no successful run found for commit"))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	app.serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID, artifact)
}

func (app *App) HandleArtifactByRunID(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.getRepoCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "run not found"))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	app.serveArtifact(w, r, app.Config.ArtifactsPath, owner.Username, repo.Name, runID, run.AttemptID, artifactPathParam(r))
}

func (app *App) HandleCIRunArtifactPreviewGET(w http.ResponseWriter, r *http.Request) {
	repo, owner := GetRepo(r), GetRepoOwner(r)
	runID := chi.URLParam(r, "run_id")
	run, err := app.getRepoCIRun(r.Context(), repo.ID, runID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "CI run not found"))
		} else {
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", err))
		}
		return
	}
	artifact := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(artifactPathParam(r), "/")))
	if artifact == "." || artifact == "" || filepath.IsAbs(artifact) || artifact == ".." || strings.HasPrefix(artifact, ".."+string(filepath.Separator)) {
		app.respondPlainError(w, r, apperr.New(apperr.KindInvalid, "Invalid artifact path"))
		return
	}
	baseDir := artifactRunDir(app.Config.ArtifactsPath, owner.Username, repo.Name, run.ID, run.AttemptID)
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this artifact", err))
		return
	}
	requestedAbs, err := filepath.Abs(filepath.Join(baseDir, artifact))
	if err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindInvalid, "Invalid artifact path", err))
		return
	}
	rel, err := filepath.Rel(baseAbs, requestedAbs)
	if err != nil {
		app.respondPlainError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this artifact", err))
		return
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		app.respondPlainError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	current := baseAbs
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "Artifact not found"))
			} else {
				app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "Artifact storage is temporarily unavailable", err))
			}
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			app.respondPlainError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
			return
		}
	}
	info, err := os.Stat(requestedAbs)
	if err != nil {
		if os.IsNotExist(err) {
			app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "Artifact not found"))
		} else {
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "Artifact storage is temporarily unavailable", err))
		}
		return
	}
	if !info.Mode().IsRegular() {
		app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "Artifact not found"))
		return
	}
	previewable, err := artifactLooksPreviewable(requestedAbs, info.Size())
	if err != nil {
		if os.IsNotExist(err) {
			app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "Artifact not found"))
		} else {
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "Artifact storage is temporarily unavailable", err))
		}
		return
	}
	if !previewable {
		app.respondPlainError(w, r, apperr.New(apperr.KindUnsupported, "Artifact is not previewable as text"))
		return
	}
	data, err := os.ReadFile(requestedAbs)
	if err != nil {
		if os.IsNotExist(err) {
			app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "Artifact not found"))
		} else {
			app.respondPlainError(w, r, apperr.Wrap(apperr.KindUnavailable, "Artifact storage is temporarily unavailable", err))
		}
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

func (app *App) serveArtifact(w http.ResponseWriter, r *http.Request, artifactsPath, owner, repo, runID, attemptID, artifact string) {
	noStore(w)
	artifact = filepath.Clean(filepath.FromSlash(strings.TrimPrefix(artifact, "/")))
	if artifact == "." || artifact == "" || filepath.IsAbs(artifact) || artifact == ".." || strings.HasPrefix(artifact, ".."+string(filepath.Separator)) {
		app.respondAPIError(w, r, apperr.New(apperr.KindInvalid, "invalid artifact path"))
		return
	}

	baseDir := artifactRunDir(artifactsPath, owner, repo, runID, attemptID)
	requestedPath := filepath.Join(baseDir, artifact)
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this artifact", err))
		return
	}
	requestedAbs, err := filepath.Abs(requestedPath)
	if err != nil {
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindInvalid, "invalid artifact path", err))
		return
	}
	rel, err := filepath.Rel(baseAbs, requestedAbs)
	if err != nil {
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this artifact", err))
		return
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		app.respondAPIError(w, r, apperr.New(apperr.KindForbidden, "artifact access denied"))
		return
	}

	current := baseAbs
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "artifact not found"))
			} else {
				app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "artifact storage is temporarily unavailable", err))
			}
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			app.respondAPIError(w, r, apperr.New(apperr.KindForbidden, "artifact access denied"))
			return
		}
	}

	f, err := os.Open(requestedAbs)
	if err != nil {
		if os.IsNotExist(err) {
			app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "artifact not found"))
		} else {
			app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "artifact storage is temporarily unavailable", err))
		}
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("failed to close artifact download file", "path", requestedAbs, "error", err)
		}
	}()
	stat, err := f.Stat()
	if err != nil {
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "artifact storage is temporarily unavailable", err))
		return
	}
	if !stat.Mode().IsRegular() {
		app.respondAPIError(w, r, apperr.New(apperr.KindNotFound, "artifact not found"))
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(artifact)})
	w.Header().Set("Content-Disposition", disposition)
	http.ServeContent(w, r, filepath.Base(artifact), stat.ModTime(), f)
}

// StatusBadge returns a short display string and CSS class for a CI run status.
func StatusBadge(status models.CIStatus) (label, class string) {
	switch status {
	case models.CIStatusPending:
		return "Pending", "badge-pending"
	case models.CIStatusRunning:
		return "Running", "badge-running"
	case models.CIStatusSuccess:
		return "Success", "badge-success"
	case models.CIStatusFailed:
		return "Failed", "badge-failed"
	case models.CIStatusSkipped:
		return "Skipped", "badge-skipped"
	case models.CIStatusCancelled:
		return "Cancelled", "badge-cancelled"
	default:
		return string(status), "badge-unknown"
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
