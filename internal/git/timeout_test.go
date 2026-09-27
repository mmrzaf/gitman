package git

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// TestStreamReportsATimeoutTheSameWayRunDoes checks that stream's error
// path reports a timeout as the real cause, the way run's does. consume
// here stands in for a parser like parsePatch, which can fail on its own
// terms — a malformed or truncated read, say — for a reason that has
// nothing to do with context.DeadlineExceeded; it sleeps past the
// command's timeout before reporting that failure, so the timeout has
// already fired by the time it does. The timeout is still the real
// cause and must still be reported as one, not masked by whatever error
// the truncation itself produced.
func TestStreamReportsATimeoutTheSameWayRunDoes(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello\n")
	f.commit(t, "one")
	f.push(t, "main")

	opts := f.repo.opts()
	opts.timeout = 20 * time.Millisecond

	err := stream(context.Background(), opts, func(out io.Reader) error {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.Copy(io.Discard, out)
		return errors.New("could not parse truncated output")
	}, "log", "--oneline")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream past its timeout = %v, want context.DeadlineExceeded", err)
	}
}
