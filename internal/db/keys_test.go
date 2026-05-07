package db

import (
	"context"
	"testing"
)

func TestSSHKeys(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "keyuser", "Pass1")
	err := db.AddSSHKey(ctx, user.ID, "laptop", "ssh-rsa AAAAB3...")
	if err != nil {
		t.Fatal(err)
	}

	keys, err := db.GetUserSSHKeys(ctx, user.ID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d err=%v", len(keys), err)
	}
	if keys[0].Name != "laptop" {
		t.Errorf("expected name laptop, got %s", keys[0].Name)
	}

	// Add another key
	err = db.AddSSHKey(ctx, user.ID, "desktop", "ecdsa-sha2-nistp256 AAAAE2V...")
	if err != nil {
		t.Fatal(err)
	}

	all, err := db.GetAllSSHKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 global keys, got %d", len(all))
	}

	err = db.DeleteSSHKey(ctx, keys[0].ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ = db.GetUserSSHKeys(ctx, user.ID)
	if len(keys) != 1 {
		t.Error("key not deleted")
	}
}
