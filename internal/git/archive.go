package git

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"
)

// ArchiveFormat is a kind of archive of a commit's files.
type ArchiveFormat string

const (
	ArchiveTarGz ArchiveFormat = "tar.gz"
	ArchiveZip   ArchiveFormat = "zip"
)

// archiveTimeout bounds one archive, which lasts as long as its reader
// takes to read it: a client too slow to finish within it is cut off,
// rather than holding a Git process open.
const archiveTimeout = 15 * time.Minute

// Archive writes the files of a commit to w as an archive, every path
// under prefix, streaming what `git archive` produces as it is produced:
// nothing of it is held in memory, however large the commit is. commit must
// be a full hash, so nothing a person typed reaches Git as an option.
// Cancelling ctx stops Git.
//
// An error may come after some of the archive was written, in which case
// the archive is truncated and is not completed: a gzip stream is left
// without its end, so reading it fails instead of passing for whole.
func (r *Repo) Archive(ctx context.Context, w io.Writer, format ArchiveFormat, commit, prefix string) error {
	if !IsHash(commit) {
		return ErrNotFound
	}
	args := []string{"archive", "--prefix=" + prefix + "/"}
	var out = w
	var gz *gzip.Writer
	switch format {
	case ArchiveZip:
		args = append(args, "--format=zip")
	case ArchiveTarGz:
		// The tar is compressed here, so Gitman does not depend on a gzip
		// program being on the host.
		args = append(args, "--format=tar")
		gz = gzip.NewWriter(w)
		out = gz
	default:
		return fmt.Errorf("unknown archive format %q", format)
	}
	args = append(args, commit)
	opts := r.opts()
	opts.stdout, opts.timeout = out, archiveTimeout
	if _, err := run(ctx, opts, args...); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}
