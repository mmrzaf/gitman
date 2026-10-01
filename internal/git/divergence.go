package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Divergence is how far a commit has gone from a base: the commits it has
// that the base lacks, and the commits the base has that it lacks.
type Divergence struct {
	Ahead, Behind int
}

// divergenceWorkers is how many counts run at once. Each is a Git process
// that walks history on its own, so this bounds the processes one page of
// branches starts.
const divergenceWorkers = 4

// divergenceCost is what a counted pair takes of the object cache.
const divergenceCost = 128

// Divergences counts, for each of heads, how far it is from base. Git does
// the counting — one rev-list per pair, which Git answers from its commit
// graph where there is one — and history is never walked here. A pair's
// count never changes, since a commit's ancestry never does, so counts are
// kept in the object cache and a branch is counted again only after it
// moves.
func (r *Repo) Divergences(ctx context.Context, base string, heads []string) (map[string]Divergence, error) {
	if !IsHash(base) {
		return nil, ErrNotFound
	}
	result := make(map[string]Divergence, len(heads))
	var todo []string
	seen := map[string]bool{}
	var firstErr error
	for _, head := range heads {
		if !IsHash(head) {
			firstErr = ErrNotFound
			continue
		}
		if seen[head] {
			continue
		}
		seen[head] = true
		if cached, ok := r.cache.get("divergence:" + base + ":" + head); ok {
			result[head] = cached.(Divergence)
			continue
		}
		todo = append(todo, head)
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		slots = make(chan struct{}, divergenceWorkers)
	)
	for _, head := range todo {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			d, err := r.divergence(ctx, base, head)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			result[head] = d
			r.cache.add("divergence:"+base+":"+head, d, divergenceCost)
		}()
	}
	wg.Wait()
	return result, firstErr
}

// divergence counts one pair: "base...head" with --left-right prints the
// commits only base has, then the commits only head has.
func (r *Repo) divergence(ctx context.Context, base, head string) (Divergence, error) {
	out, err := run(ctx, r.opts(), "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		return Divergence{}, errNotFoundIfMissing(err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return Divergence{}, fmt.Errorf("unexpected rev-list count %q", out)
	}
	behind, err1 := strconv.Atoi(fields[0])
	ahead, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return Divergence{}, fmt.Errorf("unexpected rev-list count %q", out)
	}
	return Divergence{Ahead: ahead, Behind: behind}, nil
}
