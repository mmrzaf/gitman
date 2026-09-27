package git

import (
	"strings"
	"testing"
)

func TestParseSignature(t *testing.T) {
	sig, err := parseSignature("Darius <darius@example.com> 1700000000 -0500")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Name != "Darius" || sig.Email != "darius@example.com" || sig.When.Unix() != 1700000000 {
		t.Errorf("parseSignature = %+v", sig)
	}
	if _, offset := sig.When.Zone(); offset != -5*3600 {
		t.Errorf("offset = %d", offset)
	}
	for _, bad := range []string{"no email 1 +0000", "A <a> notanumber +0000", "A <a> 1 0000", "A <a> 1"} {
		if _, err := parseSignature(bad); err == nil {
			t.Errorf("parseSignature(%q): expected an error", bad)
		}
	}
}

func TestParseCommitSkipsMultilineHeaders(t *testing.T) {
	raw := "tree " + strings.Repeat("a", 40) + "\n" +
		"parent " + strings.Repeat("b", 40) + "\n" +
		"author A <a@x> 1 +0000\n" +
		"committer C <c@x> 2 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n abc\n -----END PGP SIGNATURE-----\n" +
		"\nSubject line\n\nBody text\n"
	c, err := parseCommit(strings.Repeat("c", 40), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "Subject line" || c.Body != "Body text" || len(c.Parents) != 1 || c.Committer.Name != "C" {
		t.Errorf("parseCommit = %+v", c)
	}
}

func TestParseTreeRejectsTruncated(t *testing.T) {
	if _, err := parseTree([]byte("100644 file\x00short"), 20); err == nil {
		t.Error("expected a truncated tree to be rejected")
	}
}

func TestHashHelpers(t *testing.T) {
	if !IsHash(strings.Repeat("a", 40)) || !IsHash(strings.Repeat("f", 64)) {
		t.Error("expected SHA-1 and SHA-256 hashes to be accepted")
	}
	if IsHash(strings.Repeat("A", 40)) || IsHash("abc") {
		t.Error("expected uppercase and short strings to be rejected")
	}
	if !IsZeroHash(strings.Repeat("0", 40)) || IsZeroHash(strings.Repeat("0", 39)+"1") {
		t.Error("IsZeroHash misclassified")
	}
}
