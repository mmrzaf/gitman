package web

import (
	"net/http"

	"github.com/mmrzaf/gitman/internal/ci"
)

type workersPage struct{ Workers []ci.WorkerStatus }

func (a *App) workersView(w http.ResponseWriter, r *http.Request) error {
	workers, err := a.ci.Workers(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "workers", "Workers", workersPage{Workers: workers})
	return nil
}
