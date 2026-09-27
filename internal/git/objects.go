package git

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when a revision, object or path does not exist.
var ErrNotFound = errors.New("not found")

// TooLargeError is returned when an object exceeds the size the caller is
// willing to read. Size lets the caller still say how large it is.
type TooLargeError struct {
	Size int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("object is %d bytes, over the read limit", e.Size)
}

// ObjectType is a Git object type.
type ObjectType string

const (
	TypeBlob   ObjectType = "blob"
	TypeTree   ObjectType = "tree"
	TypeCommit ObjectType = "commit"
	TypeTag    ObjectType = "tag"
)

var hashPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// IsHash reports whether s is a full SHA-1 or SHA-256 object hash.
func IsHash(s string) bool {
	return hashPattern.MatchString(s)
}

// IsZeroHash reports whether s is the all-zeros hash Git uses for "no
// object" in ref updates: the old value of a created ref, or the new value
// of a deleted one.
func IsZeroHash(s string) bool {
	return IsHash(s) && strings.Trim(s, "0") == ""
}

// emptyTree returns the hash of the empty tree in the hash function whose
// hashes are as long as like, for diffing a root commit against nothing.
func emptyTree(like string) string {
	if len(like) == 64 {
		return "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
	}
	return "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
}

// Signature is the author or committer of a commit.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// Commit is a parsed commit object.
type Commit struct {
	Hash      string
	Tree      string
	Parents   []string
	Author    Signature
	Committer Signature
	// Message is the full commit message; Subject is its first line and
	// Body everything after the blank line that follows it.
	Message string
	Subject string
	Body    string
}

// ShortHash is the abbreviated form Gitman shows in the UI and in push
// output.
func (c *Commit) ShortHash() string {
	return shortHash(c.Hash)
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func parseCommit(hash string, data []byte) (*Commit, error) {
	c := &Commit{Hash: hash}
	header, message, _ := bytes.Cut(data, []byte("\n\n"))
	lines := strings.Split(string(header), "\n")
	for i := 0; i < len(lines); i++ {
		key, value, _ := strings.Cut(lines[i], " ")
		// Continuation lines of a multi-line header (gpgsig, mergetag)
		// start with a space; skip them along with the header they
		// belong to.
		for i+1 < len(lines) && strings.HasPrefix(lines[i+1], " ") {
			i++
		}
		switch key {
		case "tree":
			c.Tree = value
		case "parent":
			c.Parents = append(c.Parents, value)
		case "author":
			sig, err := parseSignature(value)
			if err != nil {
				return nil, fmt.Errorf("commit %s: author: %w", hash, err)
			}
			c.Author = sig
		case "committer":
			sig, err := parseSignature(value)
			if err != nil {
				return nil, fmt.Errorf("commit %s: committer: %w", hash, err)
			}
			c.Committer = sig
		}
	}
	if !IsHash(c.Tree) {
		return nil, fmt.Errorf("commit %s: missing or malformed tree", hash)
	}

	c.Message = strings.ToValidUTF8(string(message), "\uFFFD")
	subject, body, _ := strings.Cut(strings.TrimRight(c.Message, "\n"), "\n")
	c.Subject = strings.TrimSpace(subject)
	c.Body = strings.Trim(body, "\n")
	return c, nil
}

// parseSignature parses "Name <email> 1695500000 +0330".
func parseSignature(s string) (Signature, error) {
	open := strings.LastIndex(s, " <")
	closeIdx := strings.LastIndex(s, "> ")
	if open < 0 || closeIdx < open {
		return Signature{}, fmt.Errorf("malformed signature %q", s)
	}
	sig := Signature{
		Name:  strings.ToValidUTF8(s[:open], "\uFFFD"),
		Email: strings.ToValidUTF8(s[open+2:closeIdx], "\uFFFD"),
	}
	fields := strings.Fields(s[closeIdx+2:])
	if len(fields) != 2 {
		return Signature{}, fmt.Errorf("malformed signature date %q", s[closeIdx+2:])
	}
	secs, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return Signature{}, fmt.Errorf("malformed signature timestamp %q", fields[0])
	}
	offset, err := parseTZOffset(fields[1])
	if err != nil {
		return Signature{}, err
	}
	sig.When = time.Unix(secs, 0).In(time.FixedZone(fields[1], offset))
	return sig, nil
}

func parseTZOffset(tz string) (int, error) {
	if len(tz) != 5 || (tz[0] != '+' && tz[0] != '-') {
		return 0, fmt.Errorf("malformed timezone %q", tz)
	}
	hours, err1 := strconv.Atoi(tz[1:3])
	minutes, err2 := strconv.Atoi(tz[3:5])
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("malformed timezone %q", tz)
	}
	offset := hours*3600 + minutes*60
	if tz[0] == '-' {
		offset = -offset
	}
	return offset, nil
}

// EntryKind classifies a tree entry by what it is to a person browsing
// the repository, which is not the same thing as its Git object type: a
// symlink and a submodule are distinct from a file and a directory.
type EntryKind string

const (
	EntryFile      EntryKind = "file"
	EntryDir       EntryKind = "dir"
	EntrySymlink   EntryKind = "symlink"
	EntrySubmodule EntryKind = "submodule"
)

// TreeEntry is one entry of a tree object.
type TreeEntry struct {
	Name       string
	Mode       string
	Kind       EntryKind
	Hash       string
	Executable bool
}

func kindForMode(mode string) (EntryKind, bool, error) {
	switch mode {
	case "40000", "040000":
		return EntryDir, false, nil
	case "100644", "100664":
		return EntryFile, false, nil
	case "100755":
		return EntryFile, true, nil
	case "120000":
		return EntrySymlink, false, nil
	case "160000":
		return EntrySubmodule, false, nil
	}
	return "", false, fmt.Errorf("unknown tree entry mode %q", mode)
}

// parseTree parses a raw tree object: a sequence of
// "<mode> <name>\x00<binary hash>" entries, where the binary hash is
// hashLen bytes long (20 for SHA-1, 32 for SHA-256).
func parseTree(data []byte, hashLen int) ([]TreeEntry, error) {
	var entries []TreeEntry
	for len(data) > 0 {
		space := bytes.IndexByte(data, ' ')
		if space < 0 {
			return nil, errors.New("malformed tree: missing mode separator")
		}
		mode := string(data[:space])
		data = data[space+1:]

		nul := bytes.IndexByte(data, 0)
		if nul < 0 || len(data) < nul+1+hashLen {
			return nil, errors.New("malformed tree: truncated entry")
		}
		name := string(data[:nul])
		hash := fmt.Sprintf("%x", data[nul+1:nul+1+hashLen])
		data = data[nul+1+hashLen:]

		kind, exec, err := kindForMode(mode)
		if err != nil {
			return nil, err
		}
		entries = append(entries, TreeEntry{Name: name, Mode: mode, Kind: kind, Hash: hash, Executable: exec})
	}
	return entries, nil
}

// Ref is a branch or tag as Git currently records it.
type Ref struct {
	Kind Kind
	Name string
	// Target is the object the ref points at: a commit for a branch or a
	// lightweight tag, a tag object for an annotated tag.
	Target string
	// Commit is the commit the ref ultimately resolves to.
	Commit string
}
