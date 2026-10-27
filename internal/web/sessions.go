package web

import (
	"errors"
	"net/http"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/postgres"
)

type sessionsPage struct {
	Sessions       []auth.Session
	CurrentSession string
}

func (a *App) sessionsView(w http.ResponseWriter, r *http.Request) error {
	sessions, err := a.people.ListAllSessions(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "sessions", "Sessions", sessionsPage{Sessions: sessions, CurrentSession: sessionIDFrom(r)})
	return nil
}

func (a *App) sessionsRevoke(w http.ResponseWriter, r *http.Request) error {
	session, err := a.people.RevokeSession(r.Context(), r.PathValue("id"), personFrom(r).ID)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return apperr.New(apperr.KindNotFound, "That session no longer exists.")
	case errors.Is(err, auth.ErrNotSessionOwner):
		return apperr.New(apperr.KindForbidden, "That session does not belong to its owner.")
	case err != nil:
		return err
	}
	a.redirect(w, r, "/sessions", flashSuccess, "Revoked "+session.Username+"'s session.")
	return nil
}
