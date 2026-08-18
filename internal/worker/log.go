package worker

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

func (j *job) logf(format string, args ...any) {
	msg := j.maskString(fmt.Sprintf(format, args...))
	line := fmt.Sprintf("[%s] %s\n", time.Now().UTC().Format(time.RFC3339), msg)
	if j.logWriter != nil {
		if _, err := j.logWriter.Write([]byte(line)); err != nil {
			slog.Warn("failed to write CI log", "run_id", j.run.ID, "error", err)
		}
	}
}

func (j *job) enableSecretMasking() {
	if len(j.secretValues) == 0 || j.redactor != nil {
		return
	}
	j.redactor = newRedactingWriter(j.logWriter, j.secretValues)
	j.logWriter = j.redactor
}

func (j *job) flushSecretMasking() {
	if j.redactor == nil {
		return
	}
	if err := j.redactor.Flush(); err != nil {
		slog.Warn("failed to flush CI log redactor", "run_id", j.run.ID, "attempt_id", j.run.AttemptID, "error", err)
	}
}

func (j *job) maskString(value string) string {
	for _, secret := range j.secretValues {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "***")
		}
	}
	return value
}

type redactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	secrets [][]byte
	pending []byte
	maxLen  int
}

func newRedactingWriter(w io.Writer, secrets []string) *redactingWriter {
	rw := &redactingWriter{w: w}
	seen := make(map[string]bool)
	for _, secret := range secrets {
		if secret == "" || seen[secret] {
			continue
		}
		seen[secret] = true
		rw.secrets = append(rw.secrets, []byte(secret))
		if len(secret) > rw.maxLen {
			rw.maxLen = len(secret)
		}
	}
	return rw
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.maxLen == 0 {
		_, err := rw.w.Write(p)
		return len(p), err
	}

	data := append(append([]byte(nil), rw.pending...), p...)
	emitEnd := len(data) - (rw.maxLen - 1)
	if emitEnd < 0 {
		emitEnd = 0
	}
	for {
		original := emitEnd
		for _, secret := range rw.secrets {
			searchFrom := emitEnd - len(secret) + 1
			if searchFrom < 0 {
				searchFrom = 0
			}
			for searchFrom < len(data) {
				idx := bytes.Index(data[searchFrom:], secret)
				if idx < 0 {
					break
				}
				idx += searchFrom
				if idx < emitEnd && idx+len(secret) > emitEnd {
					emitEnd = idx
					break
				}
				searchFrom = idx + 1
			}
		}
		if emitEnd == original {
			break
		}
	}

	out := redactBytes(data[:emitEnd], rw.secrets)
	rw.pending = append(rw.pending[:0], data[emitEnd:]...)
	_, err := rw.w.Write(out)
	return len(p), err
}

func (rw *redactingWriter) Flush() error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if len(rw.pending) == 0 {
		return nil
	}
	_, err := rw.w.Write(redactBytes(rw.pending, rw.secrets))
	rw.pending = nil
	return err
}

func redactBytes(data []byte, secrets [][]byte) []byte {
	out := append([]byte(nil), data...)
	for _, secret := range secrets {
		out = bytes.ReplaceAll(out, secret, []byte("***"))
	}
	return out
}
