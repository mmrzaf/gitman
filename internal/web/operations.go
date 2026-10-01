package web

import (
	"github.com/mmrzaf/gitman/internal/repo"
	"net/http"
)

type operationsPage struct{ Operations []repo.Operation }

func (a *App) operationsView(w http.ResponseWriter, r *http.Request) error {
	ops, err := a.repos.PendingOperations(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "operations", "Repository recovery", operationsPage{Operations: ops})
	return nil
}

func (a *App) operationsRecover(w http.ResponseWriter, r *http.Request) error {
	if err := a.refs.Recover(r.Context()); err != nil {
		a.log.Error("repository recovery requires attention", "error", err)
		a.redirect(w, r, "/operations", flashError, "Some operations still need attention. Inspect their recovery errors.")
	} else {
		a.redirect(w, r, "/operations", flashSuccess, "Repository recovery finished.")
	}
	return nil
}
