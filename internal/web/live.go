package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// Pages update themselves over one Server-Sent Events stream. A
// notification from PostgreSQL is only a hint to look again. One that a
// stream could not take in time is not lost: the stream remembers it
// missed something and sends a change on its next poll.
const (
	livePollInterval = 5 * time.Second
	// liveLifetime ends a stream after a while; the browser reconnects
	// on its own, so no connection is held open forever.
	liveLifetime = time.Hour
	// liveLogBatch is how many log chunks one read sends at most.
	liveLogBatch     = 64
	liveWriteTimeout = 10 * time.Second
	livePerPerson    = 8
	liveGlobal       = 256
)

// liveChannels are the notifications live pages follow: runs changing,
// run output, and everything else the activity feed shows — pushes,
// repositories and settings.
var liveChannels = []string{ci.NotifyChannel, ci.LogChannel, activity.NotifyChannel}

// notice is one notification: which channel, and its payload — the run,
// or for activity the repository, it is about. An empty channel means
// anything may have changed — sent after the listener (re)connects.
type notice struct {
	channel string
	payload string
}

// subscriber is one open event stream's inbox. missed is set when a
// notice was dropped because the inbox was full.
type subscriber struct {
	notices chan notice
	missed  atomic.Bool
}

// hub fans PostgreSQL notifications out to every open event stream.
type hub struct {
	mu          sync.Mutex
	subs        map[*subscriber]struct{}
	people      map[string]int
	connections int
	// closed ends every open stream when the server shuts down. A stream
	// is a request that never finishes on its own, so without it a
	// graceful shutdown would wait out its whole grace period for any
	// open page; the browser reconnects to the next server by itself.
	closed    chan struct{}
	closeOnce sync.Once
}

func newHub() *hub {
	return &hub{subs: map[*subscriber]struct{}{}, closed: make(chan struct{})}
}

// close ends every open event stream, and any opened after.
func (h *hub) close() {
	h.closeOnce.Do(func() { close(h.closed) })
}

func (h *hub) subscribe() (*subscriber, func()) {
	s := &subscriber{notices: make(chan notice, 32)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s, func() {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
	}
}

// admit limits open streams before any response headers are committed.
func (h *hub) admit(personID string) (func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connections >= liveGlobal || h.people[personID] >= livePerPerson {
		return nil, false
	}
	h.connections++
	h.people[personID]++
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.connections--
		h.people[personID]--
		if h.people[personID] == 0 {
			delete(h.people, personID)
		}
	}, true
}

// publish never blocks: a subscriber too slow to keep up misses the
// notice, and is marked so that it catches up on its next poll.
func (h *hub) publish(n notice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.notices <- n:
		default:
			s.missed.Store(true)
		}
	}
}

// run feeds the hub until ctx ends.
func (h *hub) run(ctx context.Context, listen ListenFunc, failed func(error)) {
	listen(ctx, liveChannels, func(channel, payload string) {
		h.publish(notice{channel: channel, payload: payload})
	}, failed)
}

// runIDPattern is the shape of a run ID, which a stream echoes back in
// its events.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// mustReadRun reports a not-found error unless person can read the
// repository runID belongs to, so a run stream reveals nothing about a
// run in a repository they cannot read.
func (a *App) mustReadRun(ctx context.Context, runID string, person *auth.Person) error {
	repoID, err := a.ci.RepoIDForRun(ctx, runID)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return notFound("There is no such run.")
		}
		return err
	}
	readable, err := a.repos.CanReadID(ctx, repoID, person.ID, person.IsAdmin)
	if err != nil {
		return err
	}
	if !readable {
		return notFound("There is no such run.")
	}
	return nil
}

// mustReadStep is mustReadRun for a step ID.
func (a *App) mustReadStep(ctx context.Context, stepID string, person *auth.Person) error {
	repoID, err := a.ci.RepoIDForStep(ctx, stepID)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return notFound("There is no such step.")
		}
		return err
	}
	readable, err := a.repos.CanReadID(ctx, repoID, person.ID, person.IsAdmin)
	if err != nil {
		return err
	}
	if !readable {
		return notFound("There is no such step.")
	}
	return nil
}

// Once a stream has started, a failure cannot become an error page: it
// is logged and the stream ends, and the browser reconnects.
//
// events serves GET /events: a "change" event whenever a run or anything
// else in the activity feed changes — only the run named by ?run=, if
// given — and, with ?step=, that step's
// output as "log" events, each carrying its sequence number as its event
// ID so a reconnecting browser resumes exactly where it stopped.
func (a *App) events(w http.ResponseWriter, r *http.Request) error {
	person := personFrom(r)
	runID := r.URL.Query().Get("run")
	stepID := r.URL.Query().Get("step")
	if (runID != "" && !runIDPattern.MatchString(runID)) || (stepID != "" && !runIDPattern.MatchString(stepID)) {
		return apperr.New(apperr.KindInvalid, "That is not a run or a step.")
	}
	if runID != "" {
		if err := a.mustReadRun(r.Context(), runID, person); err != nil {
			return err
		}
	}
	if stepID != "" {
		if err := a.mustReadStep(r.Context(), stepID, person); err != nil {
			return err
		}
	}
	after := -1
	if v, err := strconv.Atoi(r.URL.Query().Get("after")); err == nil {
		after = v
	}
	if v, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil {
		after = v
	}

	release, admitted := a.hub.admit(person.ID)
	if !admitted {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
		return nil
	}
	defer release()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	// A stream outlives the page deadlines; it ends after liveLifetime.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
	sub, unsubscribe := a.hub.subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(r.Context(), liveLifetime)
	defer cancel()
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return nil
	}
	// Re-resolve the session and permissions before each output batch. A
	// captured Person must never keep an obsolete admin grant alive.
	authorized := func() bool {
		check, stop := context.WithTimeout(ctx, liveWriteTimeout)
		defer stop()
		fresh, err := a.people.SessionPerson(check, sessionTokenFrom(r))
		if err != nil {
			return false
		}
		person = fresh
		if repoID != "" {
			readable, err := a.repos.CanReadID(check, repoID, person.ID, person.IsAdmin)
			if err != nil || !readable {
				return false
			}
		}
		if runID != "" && a.mustReadRun(check, runID, person) != nil {
			return false
		}
		if stepID != "" && a.mustReadStep(check, stepID, person) != nil {
			return false
		}
		return true
	}

	// sendLogs writes the step's output written since the last send, and
	// reports whether the stream can go on.
	sendLogs := func() bool {
		if stepID == "" {
			return true
		}
		for {
			if !authorized() {
				return false
			}
			_ = rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
			read, stopRead := context.WithTimeout(ctx, liveWriteTimeout)
			chunks, err := a.ci.LogChunks(read, stepID, after, liveLogBatch)
			stopRead()
			if err != nil {
				if ctx.Err() == nil {
					a.log.Warn("event stream ended", "step", stepID, "error", err)
				}
				return false
			}
			for _, c := range chunks {
				data, _ := json.Marshal(map[string]string{"content": ansiEscape.ReplaceAllString(c.Content, "")})
				fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", c.Sequence, data)
				after = c.Sequence
			}
			if len(chunks) < liveLogBatch {
				return true
			}
		}
	}
	writeFailed := false
	changed := func(id string) {
		if _, err := fmt.Fprintf(w, "event: change\ndata: %s\n\n", id); err != nil {
			writeFailed = true
		}
	}

	if !sendLogs() || rc.Flush() != nil {
		return nil
	}
	poll := time.NewTicker(livePollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.hub.closed:
			return nil
		case n := <-sub.notices:
			if n.channel == ci.LogChannel && stepID == "" {
				continue
			}
			if repoID != "" && n.channel != "" {
				check, stopCheck := context.WithTimeout(ctx, liveWriteTimeout)
				changedRepo := n.payload
				if n.channel == ci.NotifyChannel || n.channel == ci.LogChannel {
					changedRepo, _ = a.ci.RepoIDForRun(check, n.payload)
				}
				stopCheck()
				if changedRepo != repoID {
					continue
				}
			}
			if !authorized() {
				return nil
			}
			_ = rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
			switch {
			case n.channel == "":
				changed(runID)
				if !sendLogs() {
					return nil
				}
			case n.channel == activity.NotifyChannel:
				if runID != "" {
					continue
				}
				// An event about no particular repository (a person's
				// own settings, say) is always safe to signal; one
				// about a repository is only signalled to someone who
				// can read it.
				if n.payload != "" {
					if readable, err := a.repos.CanReadID(ctx, n.payload, person.ID, person.IsAdmin); err != nil || !readable {
						continue
					}
				}
				changed("")
			case runID != "" && n.payload != runID:
				continue
			case n.channel == ci.NotifyChannel:
				// The scoped case (runID set) was already checked once,
				// above, when the stream opened; the unscoped case (Home's
				// board) must check every run's repository as it comes in.
				if runID == "" {
					repoID, err := a.ci.RepoIDForRun(ctx, n.payload)
					if err != nil {
						continue
					}
					if readable, err := a.repos.CanReadID(ctx, repoID, person.ID, person.IsAdmin); err != nil || !readable {
						continue
					}
				}
				changed(n.payload)
			case n.channel == ci.LogChannel:
				if !sendLogs() {
					return nil
				}
			}
		case <-poll.C:
			if !authorized() {
				return nil
			}
			_ = rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
			if sub.missed.Swap(false) {
				changed(runID)
			}
			// A comment line keeps proxies from closing an idle stream.
			fmt.Fprint(w, ": ping\n\n")
			if !sendLogs() {
				return nil
			}
		}
		if writeFailed {
			return nil
		}
		if err := rc.Flush(); err != nil {
			return nil
		}
	}
}
