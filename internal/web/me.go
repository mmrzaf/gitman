package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/postgres"
)

type mePage struct {
	Tokens       []auth.AccessToken
	PasswordForm *form
	TokenForm    *form
	NewToken     string
	// Tab is "tokens" or "password"; Dialog is "token-new" or empty.
	Tab    string
	Dialog string
}

func (a *App) meView(w http.ResponseWriter, r *http.Request) error {
	person := personFrom(r)
	tokens, err := a.people.ListTokens(r.Context(), person.ID)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "me", "Your account", mePage{
		Tokens: tokens, PasswordForm: newForm(nil), TokenForm: newForm(nil),
		Tab: tabFrom(r, "tokens", "password"), Dialog: dialogFrom(r, "token-new"),
	})
	return nil
}

func (a *App) mePasswordChange(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	person := personFrom(r)
	f := newForm(nil)
	current := r.PostForm.Get("current_password")
	next := r.PostForm.Get("new_password")
	if next != r.PostForm.Get("confirm_password") {
		f.Fail("confirm_password", "Does not match the new password.")
		return a.reRenderMe(w, r, mePage{PasswordForm: f, TokenForm: newForm(nil), Tab: "password"})
	}

	err := a.people.ChangePassword(r.Context(), person.ID, current, next)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		f.Fail("current_password", "That is not your current password.")
		return a.reRenderMe(w, r, mePage{PasswordForm: f, TokenForm: newForm(nil), Tab: "password"})
	case apperr.KindOf(err) == apperr.KindInvalid:
		f.Fail("new_password", apperr.PublicMessage(err))
		return a.reRenderMe(w, r, mePage{PasswordForm: f, TokenForm: newForm(nil), Tab: "password"})
	case err != nil:
		return err
	}

	// Changing the password ended every session, this one included.
	token, expires, err := a.people.CreateSession(r.Context(), person.ID, sessionTTL)
	if err != nil {
		return err
	}
	a.setSessionCookie(w, token, expires)
	a.redirect(w, r, "/me?tab=password", flashSuccess, "Password changed. Your other sessions were signed out.")
	return nil
}

// reRenderMe shows the account page again after a failed submission,
// with that form's input and its tab and dialog open.
func (a *App) reRenderMe(w http.ResponseWriter, r *http.Request, page mePage) error {
	tokens, err := a.people.ListTokens(r.Context(), personFrom(r).ID)
	if err != nil {
		return err
	}
	page.Tokens = tokens
	a.render(w, r, http.StatusUnprocessableEntity, "me", "Your account", page)
	return nil
}

func (a *App) meCreateToken(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	person := personFrom(r)
	f := newForm(r.PostForm)
	name := f.Get("name")
	scope := auth.ScopeRead
	if f.Get("scope") == "write" {
		scope = auth.ScopeWrite
	}
	var ttl *time.Duration
	switch f.Get("expires") {
	case "30":
		d := 30 * 24 * time.Hour
		ttl = &d
	case "90":
		d := 90 * 24 * time.Hour
		ttl = &d
	case "never", "":
	default:
		f.Fail("expires", "Unrecognized expiry.")
	}
	if name == "" {
		f.Fail("name", "Enter a name.")
	}
	if !f.Valid() {
		return a.reRenderMe(w, r, mePage{PasswordForm: newForm(nil), TokenForm: f, Tab: "tokens", Dialog: "token-new"})
	}

	plain, _, err := a.people.CreateToken(r.Context(), person.ID, name, scope, ttl)
	switch {
	case failForm(f, "name", err):
		return a.reRenderMe(w, r, mePage{PasswordForm: newForm(nil), TokenForm: f, Tab: "tokens", Dialog: "token-new"})
	case err != nil:
		return err
	}
	tokens, err := a.people.ListTokens(r.Context(), person.ID)
	if err != nil {
		return err
	}
	// The token is shown in this response only, and kept out of caches.
	noStore(w)
	a.render(w, r, http.StatusOK, "me", "Your account", mePage{
		Tokens: tokens, PasswordForm: newForm(nil), TokenForm: newForm(nil), NewToken: plain, Tab: "tokens",
	})
	return nil
}

func (a *App) meDeleteToken(w http.ResponseWriter, r *http.Request) error {
	token, err := a.people.RevokeToken(r.Context(), personFrom(r).ID, r.PathValue("id"))
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return apperr.New(apperr.KindNotFound, "That token no longer exists.")
	case errors.Is(err, auth.ErrNotTokenOwner):
		return apperr.New(apperr.KindForbidden, "That token does not belong to you.")
	case err != nil:
		return err
	}
	a.redirect(w, r, "/me", flashSuccess, "Revoked \u201c"+token.Name+"\u201d.")
	return nil
}
