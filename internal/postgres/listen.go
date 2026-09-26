package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Listen delivers PostgreSQL notifications on channels until ctx ends.
// It holds one connection for LISTEN and reconnects with backoff when
// that connection fails, reporting each failure to failed. After every
// successful (re)connection it calls notify once with an empty channel,
// so the caller can catch up on anything published while it was not
// listening — a notification is a hint to look, never the only record of
// a change.
func (d *DB) Listen(ctx context.Context, channels []string, notify func(channel, payload string), failed func(error)) {
	backoff := time.Second
	for {
		connected := false
		err := d.listenOnce(ctx, channels, notify, &connected)
		if ctx.Err() != nil {
			return
		}
		failed(err)
		// A connection that was up and then failed starts the backoff
		// over; only repeated failures to connect wait longer each time.
		if connected {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// listenOnce listens on one connection until it fails, setting
// *connected once its subscriptions are in place.
func (d *DB) listenOnce(ctx context.Context, channels []string, notify func(channel, payload string), connected *bool) error {
	pooled, err := d.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listen connection: %w", err)
	}
	// A connection that has run LISTEN must not go back to the pool,
	// where another caller would inherit its subscriptions, so it is
	// taken out of the pool for good and closed when listening ends.
	conn := pooled.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	for _, channel := range channels {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
			return fmt.Errorf("listen %s: %w", channel, err)
		}
	}
	*connected = true
	notify("", "")
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ctx.Err()
			}
			return fmt.Errorf("wait for notification: %w", err)
		}
		notify(n.Channel, n.Payload)
	}
}
