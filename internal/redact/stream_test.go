package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestArbitraryPartitionsAndOverlappingSecrets(t *testing.T) {
	patterns := []string{"abcdef", "defghi", "line\nsecret", "αβγ"}
	input := []byte("before abcdefghi line\nsecret αβγ after")
	matcher := New(patterns)
	want := "before *** *** *** after"
	for width := 1; width <= len(input); width++ {
		stream := matcher.Stream()
		var out bytes.Buffer
		for i := 0; i < len(input); i += width {
			out.Write(stream.Append(input[i:min(i+width, len(input))], false))
		}
		out.Write(stream.Append(nil, true))
		if out.String() != want {
			t.Fatalf("partition %d: %q", width, out.String())
		}
	}
}

func TestEveryLongLineBoundary(t *testing.T) {
	const secret = "SPLITSECRETVALUE1234567890"
	m := New([]string{secret})
	for offset := -len(secret); offset <= len(secret); offset++ {
		input := []byte(strings.Repeat("x", 65536+offset) + secret + "tail")
		s := m.Stream()
		var out bytes.Buffer
		for i := 0; i < len(input); i += 4096 {
			out.Write(s.Append(input[i:min(i+4096, len(input))], false))
		}
		out.Write(s.Append(nil, true))
		if bytes.Contains(out.Bytes(), []byte(secret)) {
			t.Fatalf("secret leaked at offset %d", offset)
		}
		if !strings.HasSuffix(out.String(), "***tail") {
			t.Fatalf("incorrect boundary output at %d", offset)
		}
	}
}

func TestRepeatedOverlapsStayBounded(t *testing.T) {
	s := New([]string{"aaaa"}).Stream()
	for i := 0; i < 10000; i++ {
		s.Append([]byte("a"), false)
		if len(s.pending) > 4 {
			t.Fatal("unbounded lookahead")
		}
	}
	if strings.Contains(string(s.Append(nil, true)), "a") {
		t.Fatal("unmasked suffix")
	}
}
