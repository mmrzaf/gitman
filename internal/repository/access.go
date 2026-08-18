package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

type Permission uint8

const (
	PermissionRead Permission = iota
	PermissionWrite
	PermissionMember
)

// Access describes the repository capabilities Gitman has established for one
// request identity. Collaborator state is loaded at most once so authorization
// never depends on several database reads agreeing with each other mid-request.
type Access struct {
	CanRead  bool
	IsMember bool
	CanWrite bool
}

// ResolveAccess calculates repository capabilities without hiding persistence
// failures. Public repositories grant anonymous source reads; member-only
// surfaces still require the owner or an explicit collaborator.
func ResolveAccess(ctx context.Context, database *db.DB, user *models.User, repo *models.Repository) (Access, error) {
	if repo == nil {
		return Access{}, fmt.Errorf("repository is nil")
	}
	if user == nil {
		return Access{CanRead: !repo.IsPrivate}, nil
	}
	if user.ID == repo.OwnerID {
		return Access{CanRead: true, IsMember: true, CanWrite: true}, nil
	}
	if database == nil {
		return Access{}, fmt.Errorf("database is nil")
	}

	level, err := database.GetRepoAccessLevel(ctx, repo.ID, user.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Access{CanRead: !repo.IsPrivate}, nil
		}
		return Access{}, err
	}
	return Access{
		CanRead:  true,
		IsMember: true,
		CanWrite: level == models.AccessWrite,
	}, nil
}

// Allowed answers a single authorization question using the shared repository
// access rules. Callers that need several capabilities should use ResolveAccess
// once instead of issuing repeated collaborator lookups.
func Allowed(ctx context.Context, database *db.DB, user *models.User, repo *models.Repository, permission Permission) (bool, error) {
	if permission != PermissionRead && permission != PermissionWrite && permission != PermissionMember {
		return false, fmt.Errorf("unknown repository permission %d", permission)
	}
	if repo == nil {
		return false, fmt.Errorf("repository is nil")
	}
	if permission == PermissionRead && !repo.IsPrivate {
		return true, nil
	}
	access, err := ResolveAccess(ctx, database, user, repo)
	if err != nil {
		return false, err
	}
	switch permission {
	case PermissionRead:
		return access.CanRead, nil
	case PermissionWrite:
		return access.CanWrite, nil
	case PermissionMember:
		return access.IsMember, nil
	default:
		return false, fmt.Errorf("unknown repository permission %d", permission)
	}
}
