package web

import (
	"errors"
	"net/http"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/postgres"
)

type peoplePage struct {
	People []auth.Person
	Form   *form
	// Password is a password just generated for Username, shown this
	// once in the response to the action that made it.
	Username, Password string
	// Dialog is "person-new" when the page opens with the form to add
	// someone: asked for by link, or after that form failed.
	Dialog string
}

func (a *App) peopleView(w http.ResponseWriter, r *http.Request) error {
	return a.renderPeople(w, r, http.StatusOK, peoplePage{Form: newForm(nil), Dialog: dialogFrom(r, "person-new")})
}

func (a *App) renderPeople(w http.ResponseWriter, r *http.Request, status int, page peoplePage) error {
	list, err := a.people.List(r.Context())
	if err != nil {
		return err
	}
	page.People = list
	if page.Password != "" {
		noStore(w)
	}
	a.render(w, r, status, "people", "People", page)
	return nil
}

func (a *App) peopleAdd(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	username := f.Get("username")
	if username == "" {
		f.Fail("username", "Enter a username.")
		return a.renderPeople(w, r, http.StatusUnprocessableEntity, peoplePage{Form: f, Dialog: "person-new"})
	}
	password, err := auth.GeneratePassword()
	if err != nil {
		return err
	}
	p, err := a.people.CreateBootstrap(r.Context(), username, password, f.Get("role") == "admin", personFrom(r).ID)
	switch {
	case errors.Is(err, postgres.ErrAlreadyExists):
		f.Fail("username", "Someone already has that username.")
		return a.renderPeople(w, r, http.StatusUnprocessableEntity, peoplePage{Form: f, Dialog: "person-new"})
	case failForm(f, "username", err):
		return a.renderPeople(w, r, http.StatusUnprocessableEntity, peoplePage{Form: f, Dialog: "person-new"})
	case err != nil:
		return err
	}
	// The password is shown in this response, never carried to the next
	// one in a cookie, and never cached.
	return a.renderPeople(w, r, http.StatusOK, peoplePage{Form: newForm(nil), Username: p.Username, Password: password})
}

// personByUsername resolves the {username} path value, or a not-found
// page for a username no one has.
func (a *App) personByUsername(r *http.Request) (*auth.Person, error) {
	p, err := a.people.GetByUsername(r.Context(), r.PathValue("username"))
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, apperr.New(apperr.KindNotFound, "There is no person named \u201c"+r.PathValue("username")+"\u201d.")
	}
	return p, err
}

// personAction runs an admin action on the {username} person and
// redirects back to /people with its outcome. ErrLastAdmin is an outcome
// to explain, not a failure.
func (a *App) personAction(w http.ResponseWriter, r *http.Request, act func(p *auth.Person) (string, error)) error {
	p, err := a.personByUsername(r)
	if err != nil {
		return err
	}
	message, err := act(p)
	if errors.Is(err, auth.ErrLastAdmin) {
		a.redirect(w, r, "/people", flashError, apperr.PublicMessage(err))
		return nil
	}
	if err != nil {
		return err
	}
	a.redirect(w, r, "/people", flashSuccess, message)
	return nil
}

func (a *App) peopleDisable(w http.ResponseWriter, r *http.Request) error {
	return a.personAction(w, r, func(p *auth.Person) (string, error) {
		return "Disabled " + p.Username + ".", a.people.Disable(r.Context(), p.ID, personFrom(r).ID)
	})
}

func (a *App) peopleEnable(w http.ResponseWriter, r *http.Request) error {
	p, err := a.personByUsername(r)
	if err != nil {
		return err
	}
	password, err := a.people.Enable(r.Context(), p.ID, personFrom(r).ID)
	if err != nil {
		return err
	}
	return a.renderPeople(w, r, http.StatusOK, peoplePage{Form: newForm(nil), Username: p.Username, Password: password})
}

func (a *App) peopleRole(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	isAdmin := r.PostForm.Get("role") == "admin"
	return a.personAction(w, r, func(p *auth.Person) (string, error) {
		role := "a member"
		if isAdmin {
			role = "an admin"
		}
		return p.Username + " is now " + role + ".", a.people.SetAdmin(r.Context(), p.ID, isAdmin, personFrom(r).ID)
	})
}

// peopleResetPassword gives a person a new, generated password. Not one's
// own: resetting a password signs its owner out, which here would end
// this very session before the new password could be shown.
func (a *App) peopleResetPassword(w http.ResponseWriter, r *http.Request) error {
	p, err := a.personByUsername(r)
	if err != nil {
		return err
	}
	if p.ID == personFrom(r).ID {
		a.redirect(w, r, "/people", flashError, "To change your own password, use your account page.")
		return nil
	}
	password, err := auth.GeneratePassword()
	if err != nil {
		return err
	}
	if err := a.people.ResetPassword(r.Context(), p.ID, password, personFrom(r).ID); err != nil {
		return err
	}
	return a.renderPeople(w, r, http.StatusOK, peoplePage{Form: newForm(nil), Username: p.Username, Password: password})
}
