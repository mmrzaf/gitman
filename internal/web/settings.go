package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// repoByName resolves the {repo} path value, or a not-found page for a
// name that no longer exists.
func (a *App) repoByName(r *http.Request) (*reposvc.Repo, error) {
	return a.repoNamed(r, r.PathValue("repo"))
}

// repoNamed resolves an explicit repository name, for callers that must
// split it out of a path value themselves — the files-at-ref routes,
// whose {repo} value is "name@ref" glued together.
//
// This is the one place every page and settings handler resolves a
// repository by name, so it is the one place read access is checked: to
// anyone who cannot read a restricted repository, it does not exist —
// the same not-found error as a name nobody has ever used.
func (a *App) repoNamed(r *http.Request, name string) (*reposvc.Repo, error) {
	notFound := apperr.New(apperr.KindNotFound, "There is no repository named \u201c"+name+"\u201d.")
	repo, err := a.repos.GetByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	person := personFrom(r)
	readable, err := a.repos.CanRead(r.Context(), repo, person.ID, person.IsAdmin)
	if err != nil {
		return nil, err
	}
	if !readable {
		return nil, notFound
	}
	return repo, nil
}

// repoSettingsRepo resolves the repository the same way repoByName does,
// then requires the signed-in person to be an admin — checked in that
// order, and only after, so a restricted repository a non-admin cannot
// read stays invisible (404) instead of announcing itself with a 403
// that a repository they could read but don't manage would also get.
func (a *App) repoSettingsRepo(r *http.Request) (*reposvc.Repo, error) {
	repo, err := a.repoByName(r)
	if err != nil {
		return nil, err
	}
	if !personFrom(r).IsAdmin {
		return nil, apperr.New(apperr.KindForbidden, "Only admins may manage a repository's settings.")
	}
	return repo, nil
}

type repoSettingsPage struct {
	repoFrame
	settingsState
	CloneURL     string
	Rules        []reposvc.Rule
	Secrets      []reposvc.Secret
	People       []auth.Person
	SecretsReady bool
}

// settingsState is what differs between the settings page shown fresh
// and shown again after a submission failed: what each form holds, and
// which tab and dialog are open.
type settingsState struct {
	DescForm   *form
	RuleForm   *form
	SecretForm *form
	DeleteForm *form
	// EditForm is the rule being edited, in the "rule-edit" dialog.
	EditForm *form
	// ReplaceKey is the secret whose value the "secret-replace" dialog
	// replaces.
	ReplaceKey string
	AccessForm *form
	// Tab is "general", "rules", "secrets", "access" or "danger"; Dialog
	// is "rule-new", "rule-edit", "secret-new", "secret-replace", or
	// empty.
	Tab    string
	Dialog string
}

var settingsTabs = []string{"general", "rules", "secrets", "access", "danger"}

// freshSettings is the settings page with nothing submitted: every form
// pre-filled with what is already saved, the same way any other field
// starts from its saved value.
func (a *App) freshSettings(ctx context.Context, repo *reposvc.Repo, tab string) (settingsState, error) {
	readers, err := a.repos.ListReaders(ctx, repo.ID)
	if err != nil {
		return settingsState{}, err
	}
	access := url.Values{
		"visibility": {string(repo.Visibility)}, "readers": readers,
		"default_push_policy": {string(repo.DefaultPushPolicy)}, "default_push_people": repo.DefaultPushPeople,
	}
	return settingsState{
		DescForm: newForm(url.Values{"description": {repo.Description}}),
		RuleForm: newForm(nil), SecretForm: newForm(nil), DeleteForm: newForm(nil),
		AccessForm: newForm(access),
		Tab:        tab,
	}, nil
}

// ruleValues is a saved rule as the rule form's values, for editing it.
func ruleValues(rule reposvc.Rule) url.Values {
	v := url.Values{
		"kind": {string(rule.Kind)}, "pattern": {rule.Pattern},
		"push_policy": {string(rule.PushPolicy)}, "push_people": rule.PushPeople,
	}
	for name, on := range map[string]bool{
		"allow_force": rule.AllowForce, "allow_delete": rule.AllowDelete, "run_on_push": rule.RunOnPush,
		"allow_docker": rule.AllowDocker, "allow_secrets": rule.AllowSecrets, "allow_ship": rule.AllowShip,
	} {
		if on {
			v.Set(name, "on")
		}
	}
	return v
}

func (a *App) repoSettingsData(r *http.Request, repo *reposvc.Repo, state settingsState) (repoSettingsPage, error) {
	rules, err := a.repos.ListRules(r.Context(), repo.ID)
	if err != nil {
		return repoSettingsPage{}, err
	}
	secrets, err := a.repos.ListSecrets(r.Context(), repo.ID)
	if err != nil {
		return repoSettingsPage{}, err
	}
	everyone, err := a.people.List(r.Context())
	if err != nil {
		return repoSettingsPage{}, err
	}
	return repoSettingsPage{
		repoFrame: repoFrame{Repo: repo, Section: "settings"}, settingsState: state,
		CloneURL: a.cloneURL(repo), Rules: rules, Secrets: secrets, People: everyone, SecretsReady: a.repos.SecretsAvailable(),
	}, nil
}

// repoSettings shows the settings page, at the tab ?tab= names, with the
// dialog ?dialog= names open: a link can lead straight to editing a rule
// (?dialog=rule-edit&kind=branch&pattern=main) or replacing a secret
// (?dialog=secret-replace&key=DEPLOY_TOKEN).
func (a *App) repoSettings(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	state, err := a.freshSettings(r.Context(), repo, tabFrom(r, settingsTabs...))
	if err != nil {
		return err
	}
	state.Dialog = dialogFrom(r, "rule-new", "rule-edit", "secret-new", "secret-replace")
	data, err := a.repoSettingsData(r, repo, state)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	switch data.Dialog {
	case "rule-edit":
		data.Dialog = ""
		for _, rule := range data.Rules {
			if string(rule.Kind) == q.Get("kind") && rule.Pattern == q.Get("pattern") {
				data.EditForm, data.Dialog = newForm(ruleValues(rule)), "rule-edit"
			}
		}
	case "secret-replace":
		data.Dialog = ""
		for _, secret := range data.Secrets {
			if secret.Key == q.Get("key") {
				data.ReplaceKey, data.Dialog = secret.Key, "secret-replace"
			}
		}
	}
	a.render(w, r, http.StatusOK, "repo_settings", repo.Name+" settings", data)
	return nil
}

// reRenderRepoSettings shows the settings page again after a failed
// action, with the failed form's input and its tab and dialog open.
func (a *App) reRenderRepoSettings(w http.ResponseWriter, r *http.Request, repo *reposvc.Repo, state settingsState) error {
	data, err := a.repoSettingsData(r, repo, state)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusUnprocessableEntity, "repo_settings", repo.Name+" settings", data)
	return nil
}

func (a *App) repoSettingsDescription(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	description := f.Get("description")
	err = a.repos.SetDescription(r.Context(), repo.ID, description, personFrom(r).ID)
	switch {
	case failForm(f, "description", err):
		state, err := a.freshSettings(r.Context(), repo, "general")
		if err != nil {
			return err
		}
		state.DescForm = f
		return a.reRenderRepoSettings(w, r, repo, state)
	case err != nil:
		return err
	}
	a.redirect(w, r, "/"+repo.Name+"/settings", flashSuccess, "Saved.")
	return nil
}

func (a *App) repoSettingsRuleSet(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	kind := git.Kind(f.Get("kind"))
	pattern := f.Get("pattern")
	if kind != git.KindBranch && kind != git.KindTag {
		f.Fail("kind", "Choose branch or tag.")
	}
	rule := reposvc.Rule{
		Kind: kind, Pattern: pattern, PushPolicy: reposvc.PushPolicy(f.Get("push_policy")),
		AllowForce: r.PostForm.Has("allow_force"), AllowDelete: r.PostForm.Has("allow_delete"),
		RunOnPush: r.PostForm.Has("run_on_push"), AllowDocker: r.PostForm.Has("allow_docker"),
		AllowSecrets: r.PostForm.Has("allow_secrets"), AllowShip: r.PostForm.Has("allow_ship"),
	}
	if rule.PushPolicy == reposvc.PushPeople {
		rule.PushPeople = r.PostForm["push_people"]
	}
	if f.Valid() {
		if err := a.repos.SaveRule(r.Context(), repo.ID, rule, personFrom(r).ID); err != nil && !failForm(f, "", err) {
			return err
		}
	}
	if !f.Valid() {
		state, err := a.freshSettings(r.Context(), repo, "rules")
		if err != nil {
			return err
		}
		if f.Get("mode") == "edit" {
			state.EditForm, state.Dialog = f, "rule-edit"
		} else {
			state.RuleForm, state.Dialog = f, "rule-new"
		}
		return a.reRenderRepoSettings(w, r, repo, state)
	}
	a.redirect(w, r, "/"+repo.Name+"/settings?tab=rules", flashSuccess, "Saved the rule for "+string(kind)+" \u201c"+pattern+"\u201d.")
	return nil
}

func (a *App) repoSettingsRuleDelete(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	kind := git.Kind(r.PostForm.Get("kind"))
	pattern := r.PostForm.Get("pattern")
	err = a.repos.DeleteRule(r.Context(), repo.ID, kind, pattern, personFrom(r).ID)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		a.redirect(w, r, "/"+repo.Name+"/settings?tab=rules", flashInfo, "There was no rule for "+string(kind)+" \u201c"+pattern+"\u201d.")
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, "/"+repo.Name+"/settings?tab=rules", flashSuccess, "Removed the rule for "+string(kind)+" \u201c"+pattern+"\u201d.")
	return nil
}

func (a *App) repoSettingsSecretSet(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	key := f.Get("key")
	err = a.repos.SetSecret(r.Context(), repo.ID, key, r.PostForm.Get("value"), personFrom(r).ID)
	switch {
	case failForm(f, "", err):
		state, err := a.freshSettings(r.Context(), repo, "secrets")
		if err != nil {
			return err
		}
		if f.Get("mode") == "replace" {
			state.ReplaceKey, state.Dialog = key, "secret-replace"
		} else {
			state.Dialog = "secret-new"
		}
		state.SecretForm = f
		return a.reRenderRepoSettings(w, r, repo, state)
	case err != nil:
		return err
	}
	a.redirect(w, r, "/"+repo.Name+"/settings?tab=secrets", flashSuccess, "Saved "+key+".")
	return nil
}

func (a *App) repoSettingsSecretDelete(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	key := r.PostForm.Get("key")
	err = a.repos.DeleteSecret(r.Context(), repo.ID, key, personFrom(r).ID)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		a.redirect(w, r, "/"+repo.Name+"/settings?tab=secrets", flashInfo, "There was no secret named "+key+".")
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, "/"+repo.Name+"/settings?tab=secrets", flashSuccess, "Removed "+key+".")
	return nil
}

// repoSettingsAccess saves who may read a repository and, for a branch
// or tag no rule matches, who may push to it.
func (a *App) repoSettingsAccess(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)

	visibility := reposvc.Visibility(f.Get("visibility"))
	if err := reposvc.ValidateVisibility(visibility); err != nil {
		failForm(f, "visibility", err)
	}
	policy := reposvc.PushPolicy(f.Get("default_push_policy"))
	var people []string
	if policy == reposvc.PushPeople {
		people = r.PostForm["default_push_people"]
	}
	if err := reposvc.ValidateDefaultPush(policy, people); err != nil {
		failForm(f, "", err)
	}

	if f.Valid() {
		actorID := personFrom(r).ID
		if err := a.repos.SetVisibility(r.Context(), repo.ID, visibility, actorID); err != nil && !failForm(f, "visibility", err) {
			return err
		}
	}
	if f.Valid() {
		if err := a.setReaders(r.Context(), repo.ID, r.PostForm["readers"], personFrom(r).ID); err != nil {
			return err
		}
	}
	if f.Valid() {
		if err := a.repos.SetDefaultPush(r.Context(), repo.ID, policy, people, personFrom(r).ID); err != nil && !failForm(f, "", err) {
			return err
		}
	}

	if !f.Valid() {
		state, err := a.freshSettings(r.Context(), repo, "access")
		if err != nil {
			return err
		}
		state.AccessForm = f
		return a.reRenderRepoSettings(w, r, repo, state)
	}
	a.redirect(w, r, "/"+repo.Name+"/settings?tab=access", flashSuccess, "Saved.")
	return nil
}

// setReaders makes a repository's explicit readers match exactly the
// given person IDs, adding and removing only what changed.
func (a *App) setReaders(ctx context.Context, repoID string, people []string, actorID string) error {
	everyone, err := a.people.List(ctx)
	if err != nil {
		return err
	}
	usernames := make(map[string]string, len(everyone))
	for _, p := range everyone {
		usernames[p.ID] = p.Username
	}
	current, err := a.repos.ListReaders(ctx, repoID)
	if err != nil {
		return err
	}
	currentSet := make(map[string]bool, len(current))
	for _, id := range current {
		currentSet[id] = true
	}
	wantSet := make(map[string]bool, len(people))
	for _, id := range people {
		wantSet[id] = true
	}
	for _, id := range people {
		if !currentSet[id] {
			if err := a.repos.AddReader(ctx, repoID, id, usernames[id], actorID); err != nil {
				return err
			}
		}
	}
	for _, id := range current {
		if !wantSet[id] {
			if err := a.repos.RemoveReader(ctx, repoID, id, usernames[id], actorID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) repoSettingsDelete(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoSettingsRepo(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	if f.Get("confirm_name") != repo.Name {
		f.Fail("confirm_name", "Type the repository's name exactly to confirm.")
		state, err := a.freshSettings(r.Context(), repo, "danger")
		if err != nil {
			return err
		}
		state.DeleteForm = f
		return a.reRenderRepoSettings(w, r, repo, state)
	}
	if err := a.repos.Delete(r.Context(), repo.ID, personFrom(r).ID); err != nil {
		return err
	}
	a.redirect(w, r, "/", flashSuccess, "Deleted "+repo.Name+", with its history, runs and deployments.")
	return nil
}
