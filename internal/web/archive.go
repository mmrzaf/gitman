package web

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
)

// archiveWriteTimeout is how long an archive may take to reach its reader:
// a page's own write deadline is far shorter than a large repository needs.
const archiveWriteTimeout = 15 * time.Minute

// archiveExt is the suffix of an archive's address and file name.
func archiveExt(format git.ArchiveFormat) string { return "." + string(format) }

// archiveURL is the address of an archive of ref, escaped for a link. The
// format is a suffix of the whole path, so a ref that holds slashes, or
// that itself ends in ".zip", is carried as it is.
func archiveURL(repoName, ref, format string) string {
	return (&url.URL{Path: "/" + repoName + "/archive/" + ref + archiveExt(git.ArchiveFormat(format))}).EscapedPath()
}

// splitArchiveName splits the path after /archive/ into the ref and the
// format its suffix asks for. Only the last suffix counts, so "v1.zip.zip"
// is the zip of the ref "v1.zip".
func splitArchiveName(name string) (ref string, format git.ArchiveFormat, ok bool) {
	for _, f := range []git.ArchiveFormat{git.ArchiveTarGz, git.ArchiveZip} {
		if ref, ok := strings.CutSuffix(name, archiveExt(f)); ok {
			return ref, f, true
		}
	}
	return "", "", false
}

// archiveWriter starts the response when the first of the archive arrives,
// so an archive Git fails to make before writing anything can still be
// answered with an error page instead of a download that is empty.
type archiveWriter struct {
	w       io.Writer
	start   func()
	started bool
}

func (a *archiveWriter) Write(p []byte) (int, error) {
	if !a.started {
		a.started = true
		a.start()
	}
	return a.w.Write(p)
}

// archive serves /{repo}/archive/{ref}.tar.gz and .zip: the files of a
// branch, tag or commit, streamed from `git archive` as they are made. It
// resolves the repository as every page does, so a repository a person
// cannot read is not found, and it takes one of the slots Git HTTP shares.
func (a *App) archive(w http.ResponseWriter, r *http.Request) error {
	transfer := r.Context()
	setup, endSetup := context.WithTimeout(transfer, 15*time.Second)
	defer endSetup()
	r = r.WithContext(setup)
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	ref, format, ok := splitArchiveName(r.PathValue("ref"))
	if !ok || ref == "" {
		return notFound("An archive is a branch, tag or commit with .tar.gz or .zip after it.")
	}
	ctx := r.Context()
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	res, err := a.resolveRefAndPath(ctx, gitRepo, repo.ID, ref)
	if err == nil && res.Path != "" {
		err = notFound("%q is not a branch, tag, or commit of this repository.", ref)
	}
	if err != nil {
		return err
	}

	endSetup()
	ctx, endTransfer := context.WithTimeout(transfer, archiveWriteTimeout)
	defer endTransfer()

	// A commit is named by its first characters, the way a link to it is.
	label := res.Name
	if res.Kind == "" {
		label = shortHash(res.Commit)
	}
	name := safeFileName(repo.Name + "-" + label)
	contentType := "application/gzip"
	if format == git.ArchiveZip {
		contentType = "application/zip"
	}
	setHeaders := func() {
		h := w.Header()
		// An archive is repository content: served as a download, never as
		// something a browser runs.
		setRawHeaders(h, "application/octet-stream", name+archiveExt(format))
		h.Set("Content-Type", contentType)
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + archiveExt(format)}))
	}
	if r.Method == http.MethodHead {
		setHeaders()
		return nil
	}

	release, ok := a.acquireGitSlot()
	if !ok {
		return apperr.New(apperr.KindUnavailable, "Gitman is handling too many downloads right now.")
	}
	defer release()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(archiveWriteTimeout))

	out := &archiveWriter{w: &deadlineWriter{dst: w, controller: http.NewResponseController(w)}, start: setHeaders}
	start := time.Now()
	if err := gitRepo.Archive(ctx, out, format, res.Commit, name); err != nil {
		if !out.started {
			return err
		}
		// The download has begun, so it can only end: a truncated archive
		// fails to unpack instead of passing for whole.
		a.log.Warn("archive failed", "repo", repo.Name, "ref", ref, "format", format, "error", err,
			"duration", time.Since(start).Round(time.Millisecond))
		return nil
	}
	if !out.started {
		// An empty archive still has its headers.
		out.start()
	}
	return nil
}
