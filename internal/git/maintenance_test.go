package git

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestDiskUsageAndGCFollowTheRepository(t *testing.T) {
	f := newFixture(t)
	f.write(t, "README.md", "# demo\n")
	f.commit(t, "initial")
	f.push(t, "main")

	before, err := f.repo.DiskUsage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("an initialized repository reported no disk usage")
	}
	if err := f.repo.GC(t.Context()); err != nil {
		t.Fatalf("GC on a healthy repository: %v", err)
	}
	after, err := f.repo.DiskUsage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after == 0 {
		t.Fatal("disk usage after GC is zero")
	}
}

func TestGCRefusesBelowTheDiskReserve(t *testing.T) {
	f := newFixture(t)
	f.repo.diskReserve = math.MaxUint64
	if err := f.repo.GC(context.Background()); !errors.Is(err, ErrDiskReserve) {
		t.Fatalf("GC below the reserve = %v, want ErrDiskReserve", err)
	}
}
