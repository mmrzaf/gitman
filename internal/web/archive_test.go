package web

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

func archiveFiles(t *testing.T, body, format string) map[string]string {
	t.Helper()
	files := map[string]string{}
	switch format {
	case "tar.gz":
		gz, err := gzip.NewReader(strings.NewReader(body))
		if err != nil {
			t.Fatalf("not a gzip: %v", err)
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return files
			}
			if err != nil {
				t.Fatalf("not a tar: %v", err)
			}
			content, _ := io.ReadAll(tr)
			files[h.Name] = string(content)
		}
	default:
		zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
		if err != nil {
			t.Fatalf("not a zip: %v", err)
		}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, _ := io.ReadAll(rc)
			rc.Close()
			files[f.Name] = string(content)
		}
	}
	return files
}

func TestArchiveOfABranchTagOrCommit(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	bare, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A branch whose name ends like an archive does.
	runGit(t, bare, "branch", "v1.zip", "main")
	syncRepoRefs(t, database, store, repo.ID)
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	for _, tc := range []struct {
		name    string
		path    string
		format  string
		file    string // the file name offered for download
		prefix  string
		readme  string
		content string // a file that tells which commit it is
		want    string
	}{
		{"a branch as tar.gz", "/waiotech/archive/main.tar.gz", "tar.gz", "waiotech-main.tar.gz", "waiotech-main/", "README.md", "README.md", "# hello, updated\n"},
		{"a branch as zip", "/waiotech/archive/main.zip", "zip", "waiotech-main.zip", "waiotech-main/", "README.md", "README.md", "# hello, updated\n"},
		{"a tag", "/waiotech/archive/v1.0.0.tar.gz", "tar.gz", "waiotech-v1.0.0.tar.gz", "waiotech-v1.0.0/", "README.md", "README.md", "# hello\n"},
		{"a tag with a slash", "/waiotech/archive/release/1.2.tar.gz", "tar.gz", "waiotech-release-1.2.tar.gz", "waiotech-release-1.2/", "which.txt", "which.txt", "release/1.2\n"},
		{"a tag with an escaped slash", "/waiotech/archive/release%2F1.2.zip", "zip", "waiotech-release-1.2.zip", "waiotech-release-1.2/", "which.txt", "which.txt", "release/1.2\n"},
		{"a branch with a slash-free name that ends in .zip", "/waiotech/archive/v1.zip.zip", "zip", "waiotech-v1.zip.zip", "waiotech-v1.zip/", "README.md", "README.md", "# hello, updated\n"},
		{"the same, as tar.gz", "/waiotech/archive/v1.zip.tar.gz", "tar.gz", "waiotech-v1.zip.tar.gz", "waiotech-v1.zip/", "README.md", "README.md", "# hello, updated\n"},
		{"a commit by its full hash", "/waiotech/archive/" + head + ".zip", "zip", "waiotech-" + head[:7] + ".zip", "waiotech-" + head[:7] + "/", "README.md", "README.md", "# hello, updated\n"},
		{"a commit by its first characters", "/waiotech/archive/" + head[:10] + ".tar.gz", "tar.gz", "waiotech-" + head[:7] + ".tar.gz", "waiotech-" + head[:7] + "/", "README.md", "README.md", "# hello, updated\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := b.do(http.MethodGet, tc.path, nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d\n%.300s", resp.StatusCode, body)
			}
			wantType := "application/gzip"
			if tc.format == "zip" {
				wantType = "application/zip"
			}
			if got := resp.Header.Get("Content-Type"); got != wantType {
				t.Errorf("Content-Type = %q, want %q", got, wantType)
			}
			if got := resp.Header.Get("Content-Disposition"); got != "attachment; filename="+tc.file {
				t.Errorf("Content-Disposition = %q, want a download named %s", got, tc.file)
			}
			for header, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Content-Security-Policy": "default-src 'none'; sandbox"} {
				if got := resp.Header.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			files := archiveFiles(t, body, tc.format)
			if got := files[tc.prefix+tc.content]; got != tc.want {
				t.Errorf("%s%s = %q, want %q (files: %v)", tc.prefix, tc.content, got, tc.want, files)
			}
			for name := range files {
				if !strings.HasPrefix(name, tc.prefix) && name != "pax_global_header" {
					t.Errorf("%s is outside %s", name, tc.prefix)
				}
			}
			if _, ok := files[tc.prefix+"server/app.py"]; !ok {
				t.Error("a file of the tree is missing")
			}
		})
	}

	// Only what is there, only as an archive.
	for _, path := range []string{
		"/waiotech/archive/nope.zip", "/waiotech/archive/main", "/waiotech/archive/main.tar", "/waiotech/archive/.zip",
		"/waiotech/archive/main/server.zip", "/waiotech/archive/--all.zip", "/waiotech/archive/", "/waiotech/archive/refs/heads/main.zip",
	} {
		resp, body := b.do(http.MethodGet, path, nil, nil)
		expect(t, resp, body, http.StatusNotFound)
	}

	// A HEAD request learns the headers and costs no archive.
	resp, body := b.do(http.MethodHead, "/waiotech/archive/main.zip", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "" || resp.Header.Get("Content-Disposition") != "attachment; filename=waiotech-main.zip" {
		t.Errorf("HEAD = %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Disposition"))
	}
}

func TestArchiveTakesAGitSlotAndAnswersBusyWhenFull(t *testing.T) {
	app, database, store, b := setupApp(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	for i := 0; i < gitConcurrencyLimit; i++ {
		app.gitSlots <- struct{}{}
	}
	resp, body := b.do(http.MethodGet, "/waiotech/archive/main.zip", nil, nil)
	expect(t, resp, body, http.StatusServiceUnavailable, "too busy")
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a busy answer has no Retry-After")
	}
	if strings.HasPrefix(body, "PK") {
		t.Error("an archive was made with every slot taken")
	}

	<-app.gitSlots
	resp, body = b.do(http.MethodGet, "/waiotech/archive/main.zip", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "PK") {
		t.Fatalf("with a slot free: %d %.80q", resp.StatusCode, body)
	}
	// And gives the slot back when it is done.
	if len(app.gitSlots) != gitConcurrencyLimit-1 {
		t.Errorf("%d slots are taken after the download, want %d", len(app.gitSlots), gitConcurrencyLimit-1)
	}
}

func TestFilesOffersDownloadsNextToTheRefPicker(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@main/server", nil, nil)
	expect(t, resp, body, http.StatusOK, `aria-label="Download main"`,
		`href="/waiotech/archive/main.tar.gz"`, `href="/waiotech/archive/main.zip"`)
	resp, body = b.do(http.MethodGet, "/waiotech@release/1.2/which.txt", nil, nil)
	expect(t, resp, body, http.StatusOK, `aria-label="Download release/1.2"`, `href="/waiotech/archive/release/1.2.tar.gz"`)
	head := mustResolve(t, mustOpenByName(t, database, store, "waiotech"), "main")
	resp, body = b.do(http.MethodGet, "/waiotech@"+head[:9], nil, nil)
	expect(t, resp, body, http.StatusOK, `aria-label="Download commit `+head[:7]+`"`, `href="/waiotech/archive/`+head+`.zip"`)
}
