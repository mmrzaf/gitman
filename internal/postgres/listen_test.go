package postgres

import (
	"context"
	"testing"
	"time"
)

func TestListenDeliversNotifications(t *testing.T) {
	database := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan [2]string, 8)
	done := make(chan struct{})
	go func() {
		database.Listen(ctx, []string{"gitman_test_a", "gitman_test_b"}, func(channel, payload string) {
			got <- [2]string{channel, payload}
		}, func(err error) { t.Errorf("listening failed: %v", err) })
		close(done)
	}()

	select {
	case first := <-got:
		if first != [2]string{"", ""} {
			t.Fatalf("first delivery = %v, want the empty catch-up call", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no catch-up call after connecting")
	}
	if _, err := database.Pool.Exec(ctx, `SELECT pg_notify('gitman_test_b', 'hello')`); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-got:
		if n != [2]string{"gitman_test_b", "hello"} {
			t.Fatalf("notification = %v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notification not delivered")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not return after its context ended")
	}
}

// TestListenReconnects drops the listening connection from the server
// side, as a database restart does, and expects Listen to report the
// failure, reconnect, announce it with a catch-up call, and keep
// delivering.
func TestListenReconnects(t *testing.T) {
	database := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan [2]string, 8)
	failures := make(chan error, 8)
	go database.Listen(ctx, []string{"gitman_test_reconnect"}, func(channel, payload string) {
		got <- [2]string{channel, payload}
	}, func(err error) { failures <- err })
	wait := func(want [2]string) {
		t.Helper()
		select {
		case n := <-got:
			if n != want {
				t.Fatalf("delivery = %v, want %v", n, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("nothing delivered, want %v", want)
		}
	}
	wait([2]string{"", ""})

	// Keep an unrelated LISTEN connection open under a different test DSN.
	// The forced disconnect below must be scoped to this test's application_name
	// and must not terminate listeners owned by another test.
	unrelated := testDB(t)
	pooled, err := unrelated.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedConn := pooled.Hijack()
	t.Cleanup(func() { _ = unrelatedConn.Close(context.Background()) })
	if _, err := unrelatedConn.Exec(ctx, `LISTEN gitman_test_unrelated`); err != nil {
		t.Fatal(err)
	}

	tag, err := database.Pool.Exec(ctx, `
		SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname = current_database()
		  AND application_name = current_setting('application_name')
		  AND query LIKE 'LISTEN %'
		  AND pid <> pg_backend_pid()
	`)
	if err != nil {
		t.Fatal(err)
	}
	if got := tag.RowsAffected(); got != 1 {
		t.Fatalf("terminated %d listening connections, want 1", got)
	}
	if _, err := unrelatedConn.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("unrelated listener was terminated: %v", err)
	}
	wait([2]string{"", ""})
	select {
	case <-failures:
	default:
		t.Fatal("the dropped connection was not reported")
	}
	if _, err := database.Pool.Exec(ctx, `SELECT pg_notify('gitman_test_reconnect', 'again')`); err != nil {
		t.Fatal(err)
	}
	wait([2]string{"gitman_test_reconnect", "again"})
}
