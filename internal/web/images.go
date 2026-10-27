package web

import (
	"fmt"
	"net/http"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/ci"
)

type imagesPage struct {
	Hosts []ci.ImageHost
	// Plan is the host whose dry-run plan was asked for by ?plan=.
	Plan *ci.ImageHost
}

func (a *App) imagesView(w http.ResponseWriter, r *http.Request) error {
	hosts, err := a.ci.ImageHosts(r.Context())
	if err != nil {
		return err
	}
	data := imagesPage{Hosts: hosts}
	if engine := r.URL.Query().Get("plan"); engine != "" {
		for i := range hosts {
			if hosts[i].EngineID == engine {
				data.Plan = &hosts[i]
				break
			}
		}
	}
	a.render(w, r, http.StatusOK, "images", "Images", data)
	return nil
}

func (a *App) imagesPrune(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	host := r.PostForm.Get("host")
	id, plan, err := a.ci.QueueImagePrune(r.Context(), host, personFrom(r).ID)
	switch {
	case apperr.KindOf(err) == apperr.KindInvalid || apperr.KindOf(err) == apperr.KindConflict || apperr.KindOf(err) == apperr.KindNotFound:
		a.redirect(w, r, "/images", flashError, apperr.PublicMessage(err))
		return nil
	case err != nil:
		return err
	case id == "":
		a.redirect(w, r, "/images", flashInfo, "No built images are prunable on that host.")
		return nil
	}
	a.redirect(w, r, "/images", flashSuccess, fmt.Sprintf("Queued pruning %d image(s). The worker will report each result here.", len(plan.Remove)))
	return nil
}

func (a *App) imagesCachePrune(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	id, err := a.ci.QueueImageCachePrune(r.Context(), r.PostForm.Get("host"), personFrom(r).ID, nil)
	switch {
	case apperr.KindOf(err) == apperr.KindInvalid || apperr.KindOf(err) == apperr.KindConflict || apperr.KindOf(err) == apperr.KindNotFound:
		a.redirect(w, r, "/images", flashError, apperr.PublicMessage(err))
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, "/images", flashSuccess, "Queued a build-cache prune as operation "+id+".")
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
		a.redirect(w, r, "/images", flashError, apperr.PublicMessage(err))
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, "/images", flashSuccess, "Queued removal of "+image+" as operation "+id+".")
	return nil
}
