package handlers

import "sync"

// requestConcurrencyLimiter bounds both total concurrent work and the share
// one client key can occupy. The active-key map is inherently bounded by the
// total slot count, so hostile client identifiers cannot grow it without bound.
type requestConcurrencyLimiter struct {
	slots  chan struct{}
	perKey int

	mu     sync.Mutex
	active map[string]int
}

func newRequestConcurrencyLimiter(maxTotal, maxPerKey int) *requestConcurrencyLimiter {
	if maxTotal < 1 {
		maxTotal = 1
	}
	if maxPerKey < 1 || maxPerKey > maxTotal {
		maxPerKey = maxTotal
	}
	return &requestConcurrencyLimiter{
		slots:  make(chan struct{}, maxTotal),
		perKey: maxPerKey,
		active: make(map[string]int),
	}
}

func (l *requestConcurrencyLimiter) tryAcquire(key string) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	if key == "" {
		key = "unknown"
	}
	select {
	case l.slots <- struct{}{}:
	default:
		return nil, false
	}

	l.mu.Lock()
	if l.active[key] >= l.perKey {
		l.mu.Unlock()
		<-l.slots
		return nil, false
	}
	l.active[key]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if count := l.active[key]; count <= 1 {
				delete(l.active, key)
			} else {
				l.active[key] = count - 1
			}
			l.mu.Unlock()
			<-l.slots
		})
	}, true
}
