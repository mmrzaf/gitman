package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/redact"
)

// Limits on what a step's output costs to store.
const (
	// logChunkBytes is how much output is batched into one stored chunk.
	logChunkBytes = 32 << 10
	// logFlushInterval is how long output may wait before being stored,
	// so a step that prints one line and then works quietly still shows
	// that line promptly.
	logFlushInterval = time.Second
	// logStoreTimeout bounds storing one chunk, and logRetryInterval is
	// how long a chunk that could not be stored waits before the next
	// attempt.
	logStoreTimeout  = 10 * time.Second
	logRetryInterval = 5 * time.Second
	// logBackpressureTimeout bounds how long Write waits for the flusher
	// to make room in the buffer before giving up and dropping the
	// output that does not fit, so a database that has stopped answering
	// cannot hang a step forever even before that failure is noticed.
	logBackpressureTimeout = 2 * logStoreTimeout
	// maxUnstoredBytes bounds the output held in memory before it is
	// stored. While storage is healthy, Write waits for room here rather
	// than losing output; while storage is failing, output past it is
	// dropped, and the log says how much.
	maxUnstoredBytes = 4 << 20
	// maxStepLogBytes caps a step's stored output. Past it, output is
	// discarded after a note saying so; the step itself keeps running.
	maxStepLogBytes = 16 << 20
	// maxLineBytes bounds each piece passed to the storage buffer.
	maxLineBytes = 64 << 10
	// minMaskedLen limits partial lines of a multiline secret. Complete
	// secret values are always masked, regardless of length.
	minMaskedLen = 4
	mask         = "***"
)

// secretMasker matches complete secrets and their nontrivial individual lines.
type secretMasker struct {
	matcher *redact.Matcher
}

func newSecretMasker(secrets map[string]string) *secretMasker {
	seen := map[string]bool{}
	var values []string
	add := func(v string) {
		if len(v) > 0 && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	for _, v := range secrets {
		add(v)
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimRight(line, "\r")
			if len(line) >= minMaskedLen {
				add(line)
			}
		}
	}
	return &secretMasker{matcher: redact.New(values)}
}

func (m *secretMasker) mask(s string) string { return m.matcher.Mask(s) }

func storable(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "\uFFFD")
}

// cutAtRune shortens s to at most n bytes without splitting a character.
func cutAtRune(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// logSink stores one chunk of a step's output.
type logSink func(ctx context.Context, sequence int, content string) error

// logWriter collects a step's combined output, masks secrets
// across arbitrary write boundaries, and stores it in chunks from its own goroutine, so
// writing output never waits for the database. It is safe for concurrent
// use.
type logWriter struct {
	redactor *redact.Stream
	ctx      context.Context
	sink     logSink

	// flushMu serializes storing, which happens without holding mu.
	flushMu sync.Mutex

	mu   sync.Mutex
	cond *sync.Cond // bound to mu; signals a writer waiting for room

	textTail  []byte          // a UTF-8 character split across writes
	pending   strings.Builder // masked, not yet stored
	unsent    string          // the next chunk to store, at most logChunkBytes
	sequence  int             // the sequence number of the next chunk
	stored    int
	dropped   int  // bytes dropped while storage was failing
	failing   bool // whether the last attempt to store a chunk failed
	truncated bool
	closing   bool // Close is running: nothing will drain the buffer further
	retryAt   time.Time
	err       error // the first storage error

	kick chan struct{}
	stop chan struct{}
	done chan struct{}
}

func newLogWriter(ctx context.Context, sink logSink, masker *secretMasker) *logWriter {
	w := &logWriter{redactor: masker.matcher.Stream(), ctx: ctx, sink: sink,
		kick: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.flushPeriodically()
	return w
}

func (w *logWriter) flushPeriodically() {
	defer close(w.done)
	ticker := time.NewTicker(logFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		case <-w.kick:
		}
		// Drain everything that can be sent right now, in logChunkBytes
		// pieces, rather than waiting for the next tick per chunk.
		for w.flush(false) {
		}
	}
}

func (w *logWriter) kickLocked() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Write masks and buffers a step's output. While storage is healthy, it
// waits (bounded) for the flusher to make room rather than losing
// output; it drops output only while storage is actually failing, or
// once a wait for room has gone on for logBackpressureTimeout. Storage
// errors themselves are never returned here — a step must not fail
// because storing its log failed — and are reported by Close.
func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := append(w.textTail, w.redactor.Append(p, false)...)
	w.textTail = nil
	// Retain an incomplete final character until the next write. Invalid
	// complete bytes are normalized only after secret matching.
	end := 0
	for end < len(text) && utf8.FullRune(text[end:]) {
		_, size := utf8.DecodeRune(text[end:])
		end += size
	}
	w.textTail = append(w.textTail, text[end:]...)
	text = text[:end]
	// Bound emitted pieces independently of input write size.
	for len(text) > 0 {
		n := len(cutAtRune(string(text), maxLineBytes))
		w.emitLocked(string(text[:n]))
		text = text[n:]
	}
	return len(p), nil
}

// truncationNote is written to the buffer, and so stored, once a step's
// output passes maxStepLogBytes; it is always the last thing emitted,
// since emitLocked never appends anything after it.
const truncationNote = "\n[Gitman stopped recording this step's output here: it passed 16 MiB.]\n"

// emitLocked adds one already-line-bounded piece of text to the buffer,
// called with mu held. It masks and validates the text, enforces the
// step's overall cap, and — when the buffer is full — either waits for
// the flusher to make room (storage healthy) or drops it (storage
// failing, or the wait for room ran out).
func (w *logWriter) emitLocked(text string) {
	if w.truncated {
		return
	}
	text = storable(text)
	if w.overCapLocked(len(text)) {
		w.pending.WriteString(truncationNote)
		w.truncated = true
		return
	}
	if room := maxUnstoredBytes - (len(w.unsent) + w.pending.Len()); len(text) > room {
		if !w.waitForRoomLocked(len(text)) {
			w.dropped += len(text)
			return
		}
		// The wait released and reacquired mu: another write, or the
		// flusher, may have changed things meanwhile.
		if w.truncated {
			return
		}
		if w.overCapLocked(len(text)) {
			w.pending.WriteString(truncationNote)
			w.truncated = true
			return
		}
	}
	w.pending.WriteString(text)
	if w.pending.Len() >= logChunkBytes {
		w.kickLocked()
	}
}

// overCapLocked reports whether adding n more bytes would pass the
// step's overall cap, counting everything ever accounted for: stored,
// dropped, waiting to be retried, or buffered.
func (w *logWriter) overCapLocked(n int) bool {
	return w.stored+w.dropped+len(w.unsent)+w.pending.Len()+n > maxStepLogBytes
}

// waitForRoomLocked waits, with mu held, for the buffer to hold room for
// n more bytes. It returns false — meaning the caller should drop rather
// than wait further — once storage is failing, once Close is already
// running (nothing will drain the buffer further), or once
// logBackpressureTimeout has passed without room appearing.
func (w *logWriter) waitForRoomLocked(n int) bool {
	if w.failing || w.closing {
		return false
	}
	deadline := time.Now().Add(logBackpressureTimeout)
	for {
		if room := maxUnstoredBytes - (len(w.unsent) + w.pending.Len()); n <= room {
			return true
		}
		if w.failing || w.closing {
			return false
		}
		if !time.Now().Before(deadline) {
			return false
		}
		w.kickLocked()
		timer := time.AfterFunc(time.Until(deadline), func() {
			w.mu.Lock()
			w.cond.Broadcast()
			w.mu.Unlock()
		})
		w.cond.Wait()
		timer.Stop()
	}
}

// fillUnsentLocked prepares the next chunk to store, called with mu held:
// a note about output already dropped, if any, followed by up to
// logChunkBytes of what is pending. It leaves an already-prepared chunk
// (one that failed to store and is being retried) alone.
func (w *logWriter) fillUnsentLocked() {
	if w.unsent != "" {
		return
	}
	if w.dropped > 0 {
		w.unsent = fmt.Sprintf("\n[Gitman could not store %d bytes of this step's output here.]\n", w.dropped)
		w.dropped = 0
	}
	if room := logChunkBytes - len(w.unsent); room > 0 && w.pending.Len() > 0 {
		s := w.pending.String()
		n := len(cutAtRune(s, room))
		w.unsent += s[:n]
		w.pending.Reset()
		w.pending.WriteString(s[n:])
	}
}

// flush stores one chunk of at most logChunkBytes: the chunk that failed
// last time, or else the next slice of what is pending. Unless force is
// set, it waits out the retry interval after a failure. It reports
// whether there is more ready to send right away, so a caller can drain
// a backlog in a tight loop.
func (w *logWriter) flush(force bool) bool {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

	w.mu.Lock()
	w.fillUnsentLocked()
	content, sequence := w.unsent, w.sequence
	if content == "" || (!force && time.Now().Before(w.retryAt)) {
		w.mu.Unlock()
		return false
	}
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(w.ctx, logStoreTimeout)
	err := w.sink(ctx, sequence, content)
	cancel()

	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if w.err == nil {
			w.err = err
		}
		w.retryAt = time.Now().Add(logRetryInterval)
		w.failing = true
		w.cond.Broadcast()
		return false
	}
	w.unsent = ""
	w.sequence++
	w.stored += len(content)
	w.failing = false
	more := w.pending.Len() > 0 || w.dropped > 0
	w.cond.Broadcast()
	return more
}

// Close stores whatever output is left, including a final line without
// a newline, and reports the first error storing any of it.
func (w *logWriter) Close() error {
	close(w.stop)
	<-w.done
	closeCtx, stopClose := context.WithTimeout(w.ctx, 30*time.Second)
	defer stopClose()
	w.ctx = closeCtx // the periodic flusher is stopped before replacing its context
	w.mu.Lock()
	w.closing = true
	// Wake a writer blocked in waitForRoomLocked immediately, rather than
	// leaving it to notice closing only once its own backpressure timeout
	// fires: nothing will ever drain the buffer for it now.
	w.cond.Broadcast()
	w.emitLocked(string(append(w.textTail, w.redactor.Append(nil, true)...)))
	w.textTail = nil
	w.mu.Unlock()
	for closeCtx.Err() == nil && w.flush(true) {
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return errors.Join(w.err, closeCtx.Err())
}

// note adds a line of Gitman's own to the step's output, such as why the
// step was stopped.
func (w *logWriter) note(text string) {
	_, _ = w.Write([]byte("\n[Gitman: " + text + "]\n"))
}
