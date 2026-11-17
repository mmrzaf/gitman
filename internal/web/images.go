package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/ci"
)

// workersURL is where image actions return: the one page that shows workers
// and images together.
const workersURL = "/workers"

func (a *App) imagesPrune(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	host := r.PostForm.Get("host")
	requested := make([]ci.ImageTarget, 0, len(r.PostForm["target"]))
	for _, raw := range r.PostForm["target"] {
		ref, id, ok := strings.Cut(raw, "|")
		if ok && ref != "" && id != "" {
			requested = append(requested, ci.ImageTarget{Ref: ref, ID: id})
		}
	}
	id, plan, err := a.ci.QueueImagePruneTargets(r.Context(), host, personFrom(r).ID, requested)
	switch {
	case apperr.KindOf(err) == apperr.KindInvalid || apperr.KindOf(err) == apperr.KindConflict || apperr.KindOf(err) == apperr.KindNotFound:
		a.redirect(w, r, workersURL, flashError, apperr.PublicMessage(err))
		return nil
	case err != nil:
		return err
	case id == "":
		a.redirect(w, r, workersURL, flashInfo, "No reviewed built images are still prunable on that host.")
		return nil
	}
	a.redirect(w, r, workersURL, flashSuccess, fmt.Sprintf("Queued pruning %d image(s). The worker will report each result on the Operations page.", len(plan.Remove)))
	return nil
}

func (a *App) imagesCachePrune(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	id, err := a.ci.QueueImageCachePrune(r.Context(), r.PostForm.Get("host"), personFrom(r).ID, nil)
	switch {
	case apperr.KindOf(err) == apperr.KindInvalid || apperr.KindOf(err) == apperr.KindConflict || apperr.KindOf(err) == apperr.KindNotFound:
		a.redirect(w, r, workersURL, flashError, apperr.PublicMessage(err))
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, workersURL, flashSuccess, "Queued a build-cache prune as operation "+id+". Its result is on the Operations page.")
	return nil
}

func (a *App) imagesRemove(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	image := r.PostForm.Get("image")
	id, err := a.ci.QueueImageRemove(r.Context(), r.PostForm.Get("host"), image, personFrom(r).ID)
	switch {
	case apperr.KindOf(err) == apperr.KindInvalid || apperr.KindOf(err) == apperr.KindConflict || apperr.KindOf(err) == apperr.KindNotFound:
		a.redirect(w, r, workersURL, flashError, apperr.PublicMessage(err))
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, workersURL, flashSuccess, "Queued removal of "+image+" as operation "+id+". Its result is on the Operations page.")
	return nil
}
