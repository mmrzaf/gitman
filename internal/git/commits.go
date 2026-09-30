package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Size limits for objects the Repo parses in full. A commit or tree
// larger than these is not something a person could browse anyway.
const (
	maxCommitBytes = 1 << 20
	maxTreeBytes   = 16 << 20
	// Blobs at or under this size are kept in the object cache.
	maxCachedBlob = 256 << 10
)

// Repo reads one bare repository. A Repo from Store.Open shares the
// store's reader pool and cache; one from OpenHookRepo owns a single
// reader and must be closed.
type Repo struct {
	path   string
	env    []string
	source objectSource
	cache  *objectCache
}

// OpenHookRepo opens the repository a Git hook is running in. env carries
// the variables Git set for the hook (GIT_OBJECT_DIRECTORY and friends),
// which make objects pushed but not yet accepted visible to the hook's own
// Git commands. Call Close when done.
func OpenHookRepo(path string, env []string) *Repo {
	return &Repo{
		path:   path,
		env:    env,
		source: &standaloneSource{path: path, env: env},
		// A fraction of Store's shared cache: this one serves a single
		// push's hook invocation, not every concurrent reader in the
		// process, so it does not need nearly as much room.
		cache: newObjectCache(8 << 20),
	}
}

// Close releases a hook repository's reader. It does nothing for a Repo
// from Store.Open, whose readers belong to the store.
func (r *Repo) Close() {
	if s, ok := r.source.(*standaloneSource); ok {
		s.close()
	}
}

func (r *Repo) opts() cmdOptions {
	return cmdOptions{dir: r.path, env: append([]string{"GIT_DIR=" + r.path}, r.env...)}
}

func (r *Repo) info(ctx context.Context, name string) (objectInfo, error) {
	var info objectInfo
	err := r.source.with(ctx, func(rd *reader) error {
		var err error
		info, err = rd.info(ctx, name)
		return err
	})
	return info, err
}

func (r *Repo) contents(ctx context.Context, name string, max int64) (objectInfo, []byte, error) {
	var info objectInfo
	var data []byte
	err := r.source.with(ctx, func(rd *reader) error {
		var err error
		info, data, err = rd.contents(ctx, name, max)
		return err
	})
	return info, data, err
}

// ResolveCommit resolves a revision — a full or abbreviated hash, or a
// full ref name such as refs/heads/main — to the full hash of the commit
// it names, peeling annotated tags.
func (r *Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", ErrNotFound
	}
	info, err := r.info(ctx, rev+"^{commit}")
	if err != nil {
		return "", err
	}
	if info.typ != TypeCommit {
		return "", ErrNotFound
	}
	return info.hash, nil
}

// ObjectType reports the type of the object hash names.
func (r *Repo) ObjectType(ctx context.Context, hash string) (ObjectType, error) {
	info, err := r.info(ctx, hash)
	if err != nil {
		return "", err
	}
	return info.typ, nil
}

// Commit returns the parsed commit with the given full hash.
func (r *Repo) Commit(ctx context.Context, hash string) (*Commit, error) {
	if !IsHash(hash) {
		return nil, ErrNotFound
	}
	if cached, ok := r.cache.get("commit:" + hash); ok {
		return cached.(*Commit), nil
	}
	info, data, err := r.contents(ctx, hash, maxCommitBytes)
	if err != nil {
		return nil, err
	}
	if info.typ != TypeCommit {
		return nil, ErrNotFound
	}
	c, err := parseCommit(hash, data)
	if err != nil {
		return nil, err
	}
	r.cache.add("commit:"+hash, c, int64(len(data)))
	return c, nil
}

// Tree returns the entries of the tree with the given full hash, in Git's
// own order.
func (r *Repo) Tree(ctx context.Context, hash string) ([]TreeEntry, error) {
	if !IsHash(hash) {
		return nil, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := r.cache.get("tree:" + hash); ok {
		return cached.([]TreeEntry), nil
	}
	info, data, err := r.contents(ctx, hash, maxTreeBytes)
	if err != nil {
		return nil, err
	}
	if info.typ != TypeTree {
		return nil, ErrNotFound
	}
	entries, err := parseTree(data, len(hash)/2)
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", hash, err)
	}
	r.cache.add("tree:"+hash, entries, int64(len(data)))
	return entries, nil
}

// Entry returns the tree entry at path in the given commit. The empty path
// names the commit's root tree.
func (r *Repo) Entry(ctx context.Context, commitHash, path string) (TreeEntry, error) {
	commit, err := r.Commit(ctx, commitHash)
	if err != nil {
		return TreeEntry{}, err
	}
	current := TreeEntry{Name: "", Mode: "40000", Kind: EntryDir, Hash: commit.Tree}
	path = strings.Trim(path, "/")
	if path == "" {
		return current, nil
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return TreeEntry{}, ErrNotFound
		}
		if current.Kind != EntryDir {
			return TreeEntry{}, ErrNotFound
		}
		entries, err := r.Tree(ctx, current.Hash)
		if err != nil {
			return TreeEntry{}, err
		}
		found := false
		for _, e := range entries {
			if e.Name == part {
				current, found = e, true
				break
			}
		}
		if !found {
			return TreeEntry{}, ErrNotFound
		}
	}
	return current, nil
}

// Blob returns a blob's content, or a *TooLargeError if it is larger than
// max.
func (r *Repo) Blob(ctx context.Context, hash string, max int64) ([]byte, error) {
	if !IsHash(hash) {
		return nil, ErrNotFound
	}
	if cached, ok := r.cache.get("blob:" + hash); ok {
		data := cached.([]byte)
		if int64(len(data)) > max {
			return nil, &TooLargeError{Size: int64(len(data))}
		}
		return data, nil
	}
	info, data, err := r.contents(ctx, hash, max)
	if err != nil {
		return nil, err
	}
	if info.typ != TypeBlob {
		return nil, ErrNotFound
	}
	if len(data) <= maxCachedBlob {
		r.cache.add("blob:"+hash, data, int64(len(data)))
	}
	return data, nil
}

// FileAt returns the content of the regular file at path in the given
// commit, or ErrNotFound if there is no regular file there.
func (r *Repo) FileAt(ctx context.Context, commitHash, path string, max int64) ([]byte, error) {
	entry, err := r.Entry(ctx, commitHash, path)
	if err != nil {
		return nil, err
	}
	if entry.Kind != EntryFile {
		return nil, ErrNotFound
	}
	return r.Blob(ctx, entry.Hash, max)
}

// Refs returns every branch and tag.
func (r *Repo) Refs(ctx context.Context) ([]Ref, error) {
	out, err := run(ctx, r.opts(), "for-each-ref",
		"--format=%(refname)%00%(objectname)%00%(*objectname)",
		"refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected for-each-ref line %q", line)
		}
		kind, name, ok := SplitFullName(fields[0])
		if !ok {
			continue
		}
		commit := fields[1]
		if fields[2] != "" {
			commit = fields[2]
		}
		refs = append(refs, Ref{Kind: kind, Name: name, Target: fields[1], Commit: commit})
	}
	return refs, nil
}

// SetHead points HEAD at branch, which is what a clone checks out. It
// does not check that the branch exists: Git allows HEAD to name a
// branch not yet pushed, and the caller decides whether that is wanted.
func (r *Repo) SetHead(ctx context.Context, branch string) error {
	if err := ValidateName(branch); err != nil {
		return fmt.Errorf("default branch: %w", err)
	}
	if _, err := run(ctx, r.opts(), "symbolic-ref", "HEAD", FullName(KindBranch, branch)); err != nil {
		return fmt.Errorf("set HEAD: %w", err)
	}
	return nil
}

// Log lists commits reachable from the commit hash, newest first,
// optionally limited to those touching path, skipping the first offset.
// It reports whether more commits follow the returned page.
func (r *Repo) Log(ctx context.Context, hash, path string, offset, limit int) ([]*Commit, bool, error) {
	if !IsHash(hash) {
		return nil, false, ErrNotFound
	}
	if limit <= 0 {
		return nil, false, nil
	}
	args := []string{"rev-list",
		"--skip=" + strconv.Itoa(offset),
		"--max-count=" + strconv.Itoa(limit+1),
		hash}
	opts := r.opts()
	if path = strings.Trim(path, "/"); path != "" {
		args = append(args, "--", path)
		// A path is a file's name, never a pathspec such as ":(exclude)x".
		opts.env = append(opts.env, "GIT_LITERAL_PATHSPECS=1")
	}
	out, err := run(ctx, opts, args...)
	if err != nil {
		return nil, false, errNotFoundIfMissing(err)
	}
	hashes := strings.Fields(string(out))
	more := len(hashes) > limit
	if more {
		hashes = hashes[:limit]
	}
	commits := make([]*Commit, 0, len(hashes))
	for _, h := range hashes {
		c, err := r.Commit(ctx, h)
		if err != nil {
			return nil, false, err
		}
		commits = append(commits, c)
	}
	return commits, more, nil
}

// CountCommits counts commits reachable from include but not from any of
// exclude, stopping at limit. capped reports that counting stopped there.
func (r *Repo) CountCommits(ctx context.Context, include string, exclude []string, limit int) (count int, capped bool, err error) {
	if !IsHash(include) {
		return 0, false, ErrNotFound
	}
	args := []string{"rev-list", "--count", "--max-count=" + strconv.Itoa(limit), include}
	for _, e := range exclude {
		if !IsHash(e) {
			return 0, false, fmt.Errorf("exclude %q is not a full hash", e)
		}
		args = append(args, "^"+e)
	}
	out, err := run(ctx, r.opts(), args...)
	if err != nil {
		return 0, false, errNotFoundIfMissing(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false, fmt.Errorf("unexpected rev-list count %q", out)
	}
	return n, n >= limit, nil
}

// CountNewCommits counts commits reachable from hash but from no branch or
// tag other than the one given: the commits a newly created ref brought
// into the repository.
func (r *Repo) CountNewCommits(ctx context.Context, hash string, kind Kind, name string, limit int) (count int, capped bool, err error) {
	if !IsHash(hash) {
		return 0, false, ErrNotFound
	}
	// --exclude patterns apply to the --branches or --tags that follows
	// them and are matched against the short name. Ref names cannot
	// contain glob characters, so the name matches only itself.
	args := []string{"rev-list", "--count", "--max-count=" + strconv.Itoa(limit), hash, "--not"}
	if kind == KindBranch {
		args = append(args, "--exclude="+name, "--branches", "--tags")
	} else {
		args = append(args, "--branches", "--exclude="+name, "--tags")
	}
	out, err := run(ctx, r.opts(), args...)
	if err != nil {
		return 0, false, errNotFoundIfMissing(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false, fmt.Errorf("unexpected rev-list count %q", out)
	}
	return n, n >= limit, nil
}

// IsAncestor reports whether commit a is an ancestor of (or equal to)
// commit b: whether moving a ref from a to b is a fast-forward.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	if !IsHash(a) || !IsHash(b) {
		return false, ErrNotFound
	}
	_, err := run(ctx, r.opts(), "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// MergeBase returns the best common ancestor of two commits, or
// ErrNotFound if they share no history.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	if !IsHash(a) || !IsHash(b) {
		return "", ErrNotFound
	}
	out, err := run(ctx, r.opts(), "merge-base", a, b)
	if err != nil {
		if exitCode(err) == 1 {
			return "", ErrNotFound
		}
		return "", err
	}
	base := strings.TrimSpace(string(out))
	if !IsHash(base) {
		return "", fmt.Errorf("unexpected merge-base output %q", out)
	}
	return base, nil
}

// Comparison is what changed between a base and a head commit: the
// commits head has that base does not, and the diff from their merge base
// to head — what merging head into base would bring.
type Comparison struct {
	Base, Head  string
	MergeBase   string
	Commits     []*Commit
	MoreCommits bool
	Diff        *Diff
}

// Compare describes head relative to base.
func (r *Repo) Compare(ctx context.Context, base, head string, maxCommits int, limits DiffLimits) (*Comparison, error) {
	mergeBase, err := r.MergeBase(ctx, base, head)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, r.opts(), "rev-list", "--max-count="+strconv.Itoa(maxCommits+1), head, "^"+base)
	if err != nil {
		return nil, errNotFoundIfMissing(err)
	}
	hashes := strings.Fields(string(out))
	cmp := &Comparison{Base: base, Head: head, MergeBase: mergeBase}
	if len(hashes) > maxCommits {
		hashes, cmp.MoreCommits = hashes[:maxCommits], true
	}
	for _, h := range hashes {
		c, err := r.Commit(ctx, h)
		if err != nil {
			return nil, err
		}
		cmp.Commits = append(cmp.Commits, c)
	}
	cmp.Diff, err = r.Diff(ctx, mergeBase, head, limits)
	if err != nil {
		return nil, err
	}
	return cmp, nil
}

// errNotFoundIfMissing maps a git error about a missing object to
// ErrNotFound. Git gives no structured signal for this on the commands
// that call it — unlike IsAncestor/MergeBase, which key off a documented
// exit code instead — so this matches the English text of the error
// message itself. baseEnv fixes LANG/LC_ALL to "C", which is what keeps
// that text stable across a host's locale and git version differences
// this would otherwise be exposed to.
func errNotFoundIfMissing(err error) error {
	var gitErr *Error
	if errors.As(err, &gitErr) {
		msg := gitErr.Stderr
		if strings.Contains(msg, "bad object") || strings.Contains(msg, "unknown revision") || strings.Contains(msg, "bad revision") {
			return ErrNotFound
		}
	}
	return err
}
