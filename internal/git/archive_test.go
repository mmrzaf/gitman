package git

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// untar reads a .tar.gz into path → content.
func untar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = string(body)
	}
}

// unzip reads a .zip into path → content.
func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(body)
	}
	return files
}

func TestArchiveStreamsACommitsFilesUnderAPrefix(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "README.md", "# Hello\n")
	f.write(t, "src/main.go", "package main\n")
	old := f.commit(t, "first")
	f.write(t, "README.md", "# Hello again\n")
	f.write(t, "src/extra.go", "package main\n")
	f.commit(t, "second")
	f.push(t, "main")

	var tarball, zipped bytes.Buffer
	if err := f.repo.Archive(ctx, &tarball, ArchiveTarGz, old, "demo-v1"); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Archive(ctx, &zipped, ArchiveZip, old, "demo-v1"); err != nil {
		t.Fatal(err)
	}
	for format, files := range map[string]map[string]string{"tar.gz": untar(t, tarball.Bytes()), "zip": unzip(t, zipped.Bytes())} {
		for name, want := range map[string]string{"demo-v1/README.md": "# Hello\n", "demo-v1/src/main.go": "package main\n"} {
			if files[name] != want {
				t.Errorf("%s: %s = %q, want %q", format, name, files[name], want)
			}
		}
		if _, ok := files["demo-v1/src/extra.go"]; ok {
			t.Errorf("%s holds a file the commit did not have", format)
		}
		for name := range files {
			// A tar carries the commit's hash in a header of its own.
			if !strings.HasPrefix(name, "demo-v1/") && name != "pax_global_header" {
				t.Errorf("%s: %s is not under the prefix", format, name)
			}
		}
	}
}

// A commit Git cannot archive is an error before anything is written, so
// the caller can still answer with one; and a name that is not a hash never
// reaches Git at all.
func TestArchiveFailsCleanly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "a.txt", "a\n")
	f.commit(t, "first")
	f.push(t, "main")

	for _, format := range []ArchiveFormat{ArchiveTarGz, ArchiveZip} {
		var out bytes.Buffer
		if err := f.repo.Archive(ctx, &out, format, strings.Repeat("a", 40), "demo"); err == nil {
			t.Errorf("%s of a commit that is not there succeeded", format)
		}
		if out.Len() != 0 {
			t.Errorf("%s of a commit that is not there wrote %d bytes before failing", format, out.Len())
		}
	}
	var out bytes.Buffer
	for _, bad := range []string{"main", "--output=/tmp/x", "", "HEAD"} {
		if err := f.repo.Archive(ctx, &out, ArchiveZip, bad, "demo"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Archive(%q) = %v, want ErrNotFound", bad, err)
		}
	}
	head, err := f.repo.ResolveCommit(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Archive(ctx, &out, "rar", head, "demo"); err == nil {
		t.Error("an unknown format succeeded")
	}
	if out.Len() != 0 {
		t.Errorf("refused archives wrote %d bytes", out.Len())
	}
}

// Cancelling stops Git: an archive of a large commit does not outlive the
// reader that went away.
func TestArchiveStopsWhenItsContextEnds(t *testing.T) {
	f := newFixture(t)
	f.write(t, "big.bin", strings.Repeat("x", 4<<20))
	head := f.commit(t, "big")
	f.push(t, "main")

	ctx, cancel := context.WithCancel(context.Background())
	out := &cancelAfter{cancel: cancel, after: 1 << 10}
	if err := f.repo.Archive(ctx, out, ArchiveZip, head, "demo"); err == nil {
		t.Fatal("a cancelled archive succeeded")
	}
	if out.n > 1<<20 {
		t.Errorf("Git went on writing %d bytes after the reader went away", out.n)
	}
}

type cancelAfter struct {
	cancel context.CancelFunc
	after  int
	n      int
}

func (c *cancelAfter) Write(p []byte) (int, error) {
	c.n += len(p)
	if c.n > c.after {
		c.cancel()
	}
	return len(p), nil
}
