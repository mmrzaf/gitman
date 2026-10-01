package push

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
)

// Refs changes branches and tags on behalf of the web interface. Every
// change is checked by the rules a push is checked by, and recorded by the
// code that records a push, so a change made here and one made by pushing
// are the same change to Gitman.
type Refs struct {
	db     *postgres.DB
	people *auth.Service
	repos  *repo.Service
	ci     *ci.Service
}

// NewRefs returns the Refs working on db's data.
func NewRefs(db *postgres.DB, people *auth.Service, repos *repo.Service, runs *ci.Service) *Refs {
	return &Refs{db: db, people: people, repos: repos, ci: runs}
}

// Delete deletes a branch or tag of a repository for a person, as a push
// that deleted it would: it checks repo.CheckDelete, removes the ref from
// Git through a durable operation, which records the push and its
// update, brings the ref index up to date, cancels the ref's queued runs
// and tells the pages that show it. remoteAddr is the address the request
// came from.
//
// A refusal is an apperr.KindForbidden error carrying the reason; a ref
// that is not there is git.ErrNotFound, and one that changed meanwhile is
// git.ErrRefMoved.
func (s *Refs) Delete(ctx context.Context, gitRepo *git.Repo, repoID, personID, remoteAddr string, kind git.Kind, name string) error {
	h := &Hook{
		DB: s.db, People: s.people, Repos: s.repos, CI: s.ci, Git: gitRepo,
		Ctx: Context{RepoID: repoID, PersonID: personID, RemoteAddr: remoteAddr},
		Out: io.Discard,
	}
	pc, err := h.load(ctx)
	if err != nil {
		return err
	}
	if _, reason := repo.CheckDelete(pc.repo, pc.rules, kind, name, pc.who()); reason != "" {
		return apperr.New(apperr.KindForbidden, fmt.Sprintf("You may not delete %s %s: %s.", kind, name, reason))
	}
	old, err := gitRepo.RefOID(ctx, kind, name)
	if err != nil {
		return err
	}
	update := Update{
		Old: old, New: strings.Repeat("0", len(old)), Ref: git.FullName(kind, name), Kind: kind, Name: name,
	}
	return h.ApplyPush(ctx, []Update{update})
}

// Recover reconciles journaled repository mutations using the same recorder as
// live pushes. No pipeline script is executed by recovery.
func (s *Refs) Recover(ctx context.Context) error {
	return s.repos.RecoverOperations(ctx, func(c context.Context, op repo.Operation) error {
		r, err := s.repos.GetByID(c, op.RepoID)
		if err != nil {
			return err
		}
		gr, err := s.repos.Open(r)
		if err != nil {
			return err
		}
		h := &Hook{DB: s.db, People: s.people, Repos: s.repos, CI: s.ci, Git: gr, Out: io.Discard}
		return h.RecoverPush(c, op)
	})
}
