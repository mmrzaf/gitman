package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type recordedLog struct {
	mu     sync.Mutex
	chunks []string
	fail   error
}

func (r *recordedLog) sink(_ context.Context, sequence int, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	if sequence != len(r.chunks) {
		return errors.New("sequence out of order")
	}
	r.chunks = append(r.chunks, content)
	return nil
}

func (r *recordedLog) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.chunks, "")
}

// chunksCopy returns the chunks stored so far, safe to inspect while the
// writer may still be storing more concurrently.
func (r *recordedLog) chunksCopy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.chunks))
	copy(out, r.chunks)
	return out
}

func TestLogWriterMasksSecretsSplitAcrossWrites(t *testing.T) {
	rec := &recordedLog{}
	w := newLogWriter(context.Background(), rec.sink, newSecretMasker(map[string]string{
		"TOKEN": "hunter2-secret",
		"KEY":   "line-one-value\nline-two-value",
		"TINY":  "ab",
	}))
	for _, part := range []string{"token is hunt", "er2-secret here\n", "key: line-two-value\n", "tiny ab stays\n", "last line"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := rec.text()
	want := "token is *** here\nkey: ***\ntiny *** stays\nlast line"
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

// TestLogWriterMasksASecretStraddlingAForcedSplit reproduces a secret
// that arrives, unterminated, straddling the point where a line longer
// than maxLineBytes is force-split into pieces before the rest of the
// line — and so the rest of the secret — has even been written yet.
// Without holding back the overlap, the first piece is masked and stored
// with only the secret's first bytes in it, which is not a match, and by
// the time the rest arrives the first bytes are already gone: the secret
// is never whole in front of a masking pass, even though it is whole
// (just split across two stored chunks) in the log the pieces add up to.
func TestLogWriterMasksASecretStraddlingAForcedSplit(t *testing.T) {
	const secret = "SPLITSECRETVALUE1234567890"
	prefix := strings.Repeat("x", maxLineBytes-10)

	rec := &recordedLog{}
	w := newLogWriter(context.Background(), rec.sink, newSecretMasker(map[string]string{"TOKEN": secret}))
	// This alone is already longer than maxLineBytes, forcing a split
	// before the line's newline — or the rest of the secret — exists.
	if _, err := w.Write([]byte(prefix + secret)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Repeat("y", 100) + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := rec.text()
	if strings.Contains(got, secret) {
		t.Fatalf("log still contains the secret in full: %q", got)
	}
	if n := strings.Count(got, mask); n != 1 {
		t.Fatalf("log has %d masked spans, want 1: %q", n, got)
	}
}

func TestLogWriterFlushesQuietOutputOnATimer(t *testing.T) {
	rec := &recordedLog{}
	w := newLogWriter(context.Background(), rec.sink, newSecretMasker(nil))
	defer w.Close()
	_, _ = w.Write([]byte("started\n"))
	deadline := time.Now().Add(3 * logFlushInterval)
	for rec.text() != "started\n" {
		if time.Now().After(deadline) {
			t.Fatal("a quiet step's output was not stored on the timer")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogWriterChunksAndTruncates(t *testing.T) {
	rec := &recordedLog{}
	w := newLogWriter(context.Background(), rec.sink, newSecretMasker(nil))
	line := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < (maxStepLogBytes/len(line))+100; i++ {
		_, _ = w.Write([]byte(line))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	text := rec.text()
	if len(text) > maxStepLogBytes+200 {
		t.Fatalf("stored %d bytes, over the %d cap", len(text), maxStepLogBytes)
	}
	if !strings.HasSuffix(text, "it passed 16 MiB.]\n") {
		t.Fatal("expected a note where output stopped being recorded")
	}
	if len(rec.chunks) < 2 {
		t.Fatalf("expected output to be stored in several chunks, got %d", len(rec.chunks))
	}
}

func TestLogWriterReportsStorageErrorsWithoutBlocking(t *testing.T) {
	rec := &recordedLog{fail: errors.New("database gone")}
	w := newLogWriter(context.Background(), rec.sink, newSecretMasker(nil))
	if n, err := w.Write([]byte("output\n")); err != nil || n != 7 {
		t.Fatalf("Write = %d, %v; a step must never see a storage failure", n, err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("expected Close to report the storage error")
	}
}

// textColumnSink refuses what a PostgreSQL text column refuses: NUL
// bytes and invalid UTF-8.
func (r *recordedLog) textColumnSink(ctx context.Context, sequence int, content string) error {
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return errors.New("invalid byte sequence for encoding UTF8")
	}
	return r.sink(ctx, sequence, content)
}

// TestLogWriterStoresOutputThatIsNotText covers binary output and a
// character cut in half by the long-line limit: both must be stored, and
// so must everything after them.
func TestLogWriterStoresOutputThatIsNotText(t *testing.T) {
	rec := &recordedLog{}
	w := newLogWriter(context.Background(), rec.textColumnSink, newSecretMasker(nil))
	long := strings.Repeat("x", maxLineBytes-1) + "é" + strings.Repeat("y", 10) + "\n"
	for _, part := range []string{"binary \x00\xff\xfe here\n", long, "still recorded\n"} {
		_, _ = w.Write([]byte(part))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := rec.text()
	if !strings.HasPrefix(got, "binary \uFFFD\uFFFD here\n") || !strings.Contains(got, "xé"+strings.Repeat("y", 10)) ||
		!strings.HasSuffix(got, "still recorded\n") {
		t.Fatalf("log = %q", got[:min(len(got), 200)])
	}
}

// flakySink fails its first attempts, as a database does while it
// restarts, and then works.
type flakySink struct {
	recordedLog
	failures int
}

func (f *flakySink) store(ctx context.Context, sequence int, content string) error {
	f.mu.Lock()
	if f.failures > 0 {
		f.failures--
		f.mu.Unlock()
		return errors.New("connection refused")
	}
	f.mu.Unlock()
	return f.sink(ctx, sequence, content)
}

// TestLogWriterRetriesAfterAStorageFailure keeps output written while the
// database was briefly unavailable, in order.
func TestLogWriterRetriesAfterAStorageFailure(t *testing.T) {
	f := &flakySink{failures: 1}
	w := newLogWriter(context.Background(), f.store, newSecretMasker(nil))
	_, _ = w.Write([]byte("before\n"))
	time.Sleep(logFlushInterval + 300*time.Millisecond)
	_, _ = w.Write([]byte("after\n"))
	if err := w.Close(); err == nil {
		t.Fatal("Close should still report the failure that happened")
	}
	if got := f.text(); got != "before\nafter\n" {
		t.Fatalf("log = %q, want every line in order", got)
	}
}

// TestLogWriterNeverWaitsForStorage writes a lot of output while storing
// hangs, as it does when the database stops answering: the step's writes
// must not wait for it.
func TestLogWriterNeverWaitsForStorage(t *testing.T) {
	release := make(chan struct{})
	rec := &recordedLog{}
	hanging := func(ctx context.Context, sequence int, content string) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return rec.sink(ctx, sequence, content)
	}
	w := newLogWriter(context.Background(), hanging, newSecretMasker(nil))
	line := []byte(strings.Repeat("z", 1023) + "\n")
	start := time.Now()
	for i := 0; i < 2*logChunkBytes/len(line)+10; i++ {
		_, _ = w.Write(line)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("writes took %s while storage hung", elapsed)
	}
	close(release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.text()); got != (2*logChunkBytes/len(line)+10)*len(line) {
		t.Fatalf("stored %d bytes", got)
	}
}

// TestLogWriterStoresAnEightMegabyteBurstWithAHealthySink writes far more
// than the in-memory buffer holds, as fast as it can, to a sink that
// never fails: every byte must still be stored, and each insert must
// stay within logChunkBytes, because a healthy database backs a writer
// off instead of dropping its output.
func TestLogWriterStoresAnEightMegabyteBurstWithAHealthySink(t *testing.T) {
	rec := &recordedLog{}
	w := newLogWriter(context.Background(), rec.sink, newSecretMasker(nil))
	const total = 8 << 20
	line := []byte(strings.Repeat("b", 1023) + "\n")
	written := 0
	for written < total {
		n, err := w.Write(line)
		if err != nil {
			t.Fatal(err)
		}
		written += n
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := rec.text()
	if len(got) != written {
		t.Fatalf("wrote %d bytes, stored %d; a healthy sink must lose nothing", written, len(got))
	}
	for i, c := range rec.chunksCopy() {
		if len(c) > logChunkBytes {
			t.Fatalf("chunk %d is %d bytes, over logChunkBytes (%d)", i, len(c), logChunkBytes)
		}
	}
}

// TestLogWriterDropsOnlyWhileStorageIsFailingThenRecovers drives a sink
// that fails until told otherwise. Output written while it is down past
// the in-memory buffer must be dropped with a note (not lost silently,
// and Write must not hang for long), and output written after it
// recovers must all be stored.
func TestLogWriterDropsOnlyWhileStorageIsFailingThenRecovers(t *testing.T) {
	rec := &recordedLog{}
	var failing atomic.Bool
	failing.Store(true)
	sink := func(ctx context.Context, sequence int, content string) error {
		if failing.Load() {
			return errors.New("connection refused")
		}
		return rec.sink(ctx, sequence, content)
	}
	w := newLogWriter(context.Background(), sink, newSecretMasker(nil))

	// Write well past the in-memory buffer while storage is down. The
	// line length is deliberately not a divisor of the buffer size, so
	// the buffer always ends up with a little slack rather than exactly
	// full — otherwise whether "after" itself fits would depend on
	// alignment rather than on storage having recovered.
	line := []byte(strings.Repeat("d", 999) + "\n")
	start := time.Now()
	for i := 0; i < (2*maxUnstoredBytes)/len(line); i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Fatalf("Write blocked for %s while storage was down; its wait must stay bounded", elapsed)
	}

	failing.Store(false)
	if _, err := w.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	// Close still reports the failure that happened, even though storage
	// went on to recover.
	if err := w.Close(); err == nil {
		t.Fatal("expected Close to report the storage failure that happened")
	}

	got := rec.text()
	if !strings.Contains(got, "could not store") {
		t.Fatalf("expected a note about the output dropped while storage was down, got %q", got[:min(len(got), 300)])
	}
	if !strings.HasSuffix(got, "after\n") {
		t.Fatalf("expected output written after recovery to be stored last, got %q", got[max(0, len(got)-50):])
	}
}

func TestLogWriterPreservesUTF8AcrossWrites(t *testing.T) {
	rec := &recordedLog{}
	w := newLogWriter(t.Context(), rec.textColumnSink, newSecretMasker(map[string]string{"TOKEN": "秘密-value"}))
	text := []byte("before 日本語 秘密-value after\n")
	for _, b := range text {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rec.text(); got != "before 日本語 *** after\n" {
		t.Fatalf("split UTF-8: %q", got)
	}
}
