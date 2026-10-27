package web

import (
	"net/http"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/repo"
)

type operationsPage struct {
	Operations      []repo.Operation
	ImageOperations []ci.ImageOperation
}

func (a *App) operationsView(w http.ResponseWriter, r *http.Request) error {
	ops, err := a.repos.PendingOperations(r.Context())
	if err != nil {
		return err
	}
	images, err := a.ci.ImageOperations(r.Context(), 50)
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "operations", "Operations", operationsPage{Operations: ops, ImageOperations: images})
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
