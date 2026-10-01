// Package redact masks exact byte patterns before text is normalized or stored.
package redact

import "bytes"

type node struct {
	next  map[byte]int
	fail  int
	depth int
	match int
}

// Matcher is immutable and can be shared between independent streams.
type Matcher struct{ nodes []node }

func New(patterns []string) *Matcher {
	m := &Matcher{nodes: []node{{next: map[byte]int{}}}}
	for _, p := range patterns {
		if p == "" {
			continue
		}
		at := 0
		for i := 0; i < len(p); i++ {
			next, ok := m.nodes[at].next[p[i]]
			if !ok {
				next = len(m.nodes)
				depth := m.nodes[at].depth + 1
				m.nodes = append(m.nodes, node{next: map[byte]int{}, depth: depth})
				m.nodes[at].next[p[i]] = next
			}
			at = next
		}
		m.nodes[at].match = max(m.nodes[at].match, len(p))
	}
	queue := []int{}
	for _, n := range m.nodes[0].next {
		queue = append(queue, n)
	}
	for i := 0; i < len(queue); i++ {
		at := queue[i]
		for b, child := range m.nodes[at].next {
			fail := m.nodes[at].fail
			for fail != 0 {
				if _, ok := m.nodes[fail].next[b]; ok {
					break
				}
				fail = m.nodes[fail].fail
			}
			if n, ok := m.nodes[fail].next[b]; ok {
				m.nodes[child].fail = n
			}
			f := m.nodes[child].fail
			m.nodes[child].match = max(m.nodes[child].match, m.nodes[f].match)
			queue = append(queue, child)
		}
	}
	return m
}

// Stream retains only unresolved pattern prefixes. Matches can overlap,
// cross writes, and contain newlines or invalid UTF-8.
type Stream struct {
	matcher *Matcher
	state   int
	pending []byte
	masked  []bool
	masking bool
}

func (m *Matcher) Stream() *Stream { return &Stream{matcher: m} }

func (s *Stream) Append(p []byte, final bool) []byte {
	var out bytes.Buffer
	for _, b := range p {
		s.pending = append(s.pending, b)
		s.masked = append(s.masked, false)
		for s.state != 0 {
			if _, ok := s.matcher.nodes[s.state].next[b]; ok {
				break
			}
			s.state = s.matcher.nodes[s.state].fail
		}
		if next, ok := s.matcher.nodes[s.state].next[b]; ok {
			s.state = next
		} else {
			s.state = 0
		}
		if length := s.matcher.nodes[s.state].match; length > 0 {
			for i := max(0, len(s.pending)-length); i < len(s.pending); i++ {
				s.masked[i] = true
			}
		}
		n := max(0, len(s.pending)-s.matcher.nodes[s.state].depth)
		// Already masked bytes need no more lookahead, even when they are
		// also a prefix of a longer or overlapping secret.
		for n < len(s.pending) && s.masked[n] {
			n++
		}
		s.emit(&out, n)
	}
	if final {
		s.emit(&out, len(s.pending))
		s.state = 0
	}
	return out.Bytes()
}

func (s *Stream) emit(out *bytes.Buffer, n int) {
	for i := 0; i < n; i++ {
		if s.masked[i] {
			if !s.masking {
				out.WriteString("***")
			}
			s.masking = true
		} else {
			out.WriteByte(s.pending[i])
			s.masking = false
		}
	}
	s.pending = s.pending[n:]
	s.masked = s.masked[n:]
}

func (m *Matcher) Mask(text string) string { return string(m.Stream().Append([]byte(text), true)) }
