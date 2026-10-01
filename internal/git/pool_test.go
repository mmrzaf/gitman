package git

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReaderPoolHardLimitAndCancellation(t *testing.T) {
	requireGit(t)
	store := NewStore(t.TempDir())
	defer store.Close()
	for _, id := range []string{"one", "two"} {
		if err := store.Create(t.Context(), id, "main"); err != nil {
			t.Fatal(err)
		}
	}
	pool := newReaderPool(1, time.Minute)
	defer pool.close()
	firstPath, _ := store.Path("one")
	secondPath, _ := store.Path("two")
	first, err := pool.acquire(t.Context(), firstPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := pool.acquire(ctx, secondPath); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full pool acquire: %v", err)
	}
	pool.mu.Lock()
	count := len(pool.readers)
	pool.mu.Unlock()
	if count != 1 {
		t.Fatalf("full pool started %d readers", count)
	}
	pool.release(firstPath, first)
	second, err := pool.acquire(t.Context(), secondPath)
	if err != nil {
		t.Fatal(err)
	}
	pool.release(secondPath, second)
}
