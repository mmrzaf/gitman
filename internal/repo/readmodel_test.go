package repo

import (
	"context"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"testing"
)

func TestReadablePaginationFiltersBeforeLimiting(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	s := NewService(db, newStore(t), "")
	person, err := auth.NewService(db).Create(ctx, "reader", "reader-password-123", false, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Q.Exec(ctx, `INSERT INTO repos(id,name,visibility) SELECT 'hidden'||n,'a-hidden-'||lpad(n::text,2,'0'),'restricted' FROM generate_series(1,30) n; INSERT INTO repos(id,name) SELECT 'public'||n,'b-public-'||lpad(n::text,2,'0') FROM generate_series(1,28) n`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ListReadablePage(ctx, person.ID, false, "", 25)
	if err != nil || len(first) != 25 || first[0].Name != "b-public-01" {
		t.Fatalf("first=%d err=%v", len(first), err)
	}
	second, err := s.ListReadablePage(ctx, person.ID, false, first[24].Name, 25)
	if err != nil || len(second) != 3 || second[0].Name != "b-public-26" {
		t.Fatalf("second=%d err=%v", len(second), err)
	}
	admin, err := s.ListReadablePage(ctx, person.ID, true, "", 25)
	if err != nil || len(admin) != 25 || admin[0].Name != "a-hidden-01" {
		t.Fatalf("admin=%d err=%v", len(admin), err)
	}
	if _, err := db.Q.Exec(ctx, `INSERT INTO repo_readers(repo_id,person_id) VALUES('hidden1',$1)`, person.ID); err != nil {
		t.Fatal(err)
	}
	visible, err := s.ListReadablePage(ctx, person.ID, false, "", 1)
	if err != nil || len(visible) != 1 || visible[0].Name != "a-hidden-01" {
		t.Fatal("explicit grant not applied", err)
	}
}
