package repository

import (
	"context"
	"testing"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestAllowedPublicReadDoesNotMakeAnonymousMember(t *testing.T) {
	repo := &models.Repository{ID: "r", OwnerID: "owner", IsPrivate: false}
	ok, err := Allowed(context.Background(), nil, nil, repo, PermissionRead)
	if err != nil || !ok {
		t.Fatalf("public read = %v, %v", ok, err)
	}
	ok, err = Allowed(context.Background(), nil, nil, repo, PermissionMember)
	if err != nil || ok {
		t.Fatalf("anonymous member = %v, %v", ok, err)
	}
}

func TestAllowedOwnerDoesNotRequireDatabase(t *testing.T) {
	repo := &models.Repository{ID: "r", OwnerID: "owner", IsPrivate: true}
	user := &models.User{ID: "owner"}
	for _, permission := range []Permission{PermissionRead, PermissionWrite, PermissionMember} {
		ok, err := Allowed(context.Background(), (*db.DB)(nil), user, repo, permission)
		if err != nil || !ok {
			t.Fatalf("permission %d = %v, %v", permission, ok, err)
		}
	}
}

func TestAllowedRejectsUnknownPermissionEvenForOwner(t *testing.T) {
	repo := &models.Repository{ID: "r", OwnerID: "owner", IsPrivate: true}
	user := &models.User{ID: "owner"}
	if ok, err := Allowed(context.Background(), nil, user, repo, Permission(255)); err == nil || ok {
		t.Fatalf("unknown permission = %v, %v; want false with error", ok, err)
	}
}

func TestAllowedPublicReadDoesNotNeedCollaboratorLookup(t *testing.T) {
	repo := &models.Repository{ID: "r", OwnerID: "owner", IsPrivate: false}
	user := &models.User{ID: "someone-else"}
	ok, err := Allowed(context.Background(), nil, user, repo, PermissionRead)
	if err != nil || !ok {
		t.Fatalf("public authenticated read = %v, %v", ok, err)
	}
}
