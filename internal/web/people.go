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
	// Password is a generated credential shown once on the GET reached
	// by a refresh-safe redirect after the creation, enable or reset.
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
	if status == http.StatusOK && r.Method == http.MethodGet {
		if secret, ok := a.consumeCredential(w, r, revealPerson); ok {
			page.Username, page.Password = secret.username, secret.value
		}
	}
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
	role := f.Get("role")
	if role != "member" && role != "admin" {
		f.Fail("role", "Choose a valid role.")
		return a.renderPeople(w, r, http.StatusUnprocessableEntity, peoplePage{Form: f, Dialog: "person-new"})
	}
	password, err := auth.GeneratePassword()
	if err != nil {
		return err
	}
	p, err := a.people.CreateBootstrap(r.Context(), username, password, role == "admin", personFrom(r).ID)
	switch {
	case errors.Is(err, postgres.ErrAlreadyExists):
		f.Fail("username", "Someone already has that username.")
		return a.renderPeople(w, r, http.StatusUnprocessableEntity, peoplePage{Form: f, Dialog: "person-new"})
	case failForm(f, "username", err):
		return a.renderPeople(w, r, http.StatusUnprocessableEntity, peoplePage{Form: f, Dialog: "person-new"})
	case err != nil:
		return err
	}
	// The redirect makes browser refresh safe; only the next authenticated
	// GET can consume the generated password.
	a.showCredential(w, r, revealPerson, p.Username, password, "/people")
	return nil
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

func (a *App) peopleDisable(w http.ResponseWriter, r *http.Request) error {
	p, err := a.personByUsername(r)
	if err != nil {
		return err
	}
	if err := a.people.Disable(r.Context(), p.ID, personFrom(r).ID); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) {
			a.redirect(w, r, "/people", flashError, apperr.PublicMessage(err))
			return nil
		}
		return err
	}
	message := "Disabled " + p.Username + "."
	if p.ID == personFrom(r).ID {
		// The account is disabled and signed out; /people would be a 403.
		a.clearSessionCookie(w)
		a.redirect(w, r, "/login", flashSuccess, message)
		return nil
	}
	a.redirect(w, r, "/people", flashSuccess, message)
	return nil
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
	a.showCredential(w, r, revealPerson, p.Username, password, "/people")
	return nil
}

func (a *App) peopleRole(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	role := r.PostForm.Get("role")
	if role != "member" && role != "admin" {
		return apperr.New(apperr.KindInvalid, "Choose a valid role.")
	}
	isAdmin := role == "admin"
	p, err := a.personByUsername(r)
	if err != nil {
		return err
	}
	message := p.Username + " is now a member."
	if isAdmin {
		message = p.Username + " is now an admin."
	}
	if err := a.people.SetAdmin(r.Context(), p.ID, isAdmin, personFrom(r).ID); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) {
			a.redirect(w, r, "/people", flashError, apperr.PublicMessage(err))
			return nil
		}
		return err
	}
	if p.ID == personFrom(r).ID && !isAdmin {
		// /people is admin-only; go somewhere the new member can see.
		a.redirect(w, r, "/", flashSuccess, message)
		return nil
	}
	a.redirect(w, r, "/people", flashSuccess, message)
	return nil
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
	a.showCredential(w, r, revealPerson, p.Username, password, "/people")
	return nil
}
