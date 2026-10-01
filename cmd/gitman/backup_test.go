package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupManifestChecksEveryFileAndKey(t *testing.T) {
	root := t.TempDir()
	m := backupManifest{Format: 1, InstanceID: "test", Files: map[string]string{}}
	for _, name := range []string{"database.dump", "repos.tar.gz"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		sum, err := hashFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		m.Files[name] = sum
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBackup(root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBackup(root, "invalid"); err == nil {
		t.Fatal("accepted a mismatched key")
	}
	if err := os.WriteFile(filepath.Join(root, "database.dump"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBackup(root, ""); err == nil {
		t.Fatal("accepted a modified database dump")
	}
}

func TestRepositoryArchiveRoundTrip(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "test.git", "objects"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "test.git", "objects", "data"), []byte("repository"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "repos.tar.gz")
	if err := archiveRepositories(context.Background(), source, archive); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := extractRepositories(context.Background(), archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "test.git", "objects", "data"))
	if err != nil || string(got) != "repository" {
		t.Fatalf("got=%q error=%v", got, err)
	}
}

func TestRestoreRejectsTraversalAndLinks(t *testing.T) {
	for _, hdr := range []*tar.Header{{Name: "../../outside", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "../../outside"}} {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			_, _ = tw.Write([]byte("x"))
		}
		_ = tw.Close()
		_ = gz.Close()
		archive := filepath.Join(t.TempDir(), "archive.gz")
		if err := os.WriteFile(archive, b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := extractRepositories(context.Background(), archive, t.TempDir()); err == nil {
			t.Fatalf("accepted unsafe entry %+v", hdr)
		}
	}
}

func TestDatabaseToolUsesURLParametersAndInheritedPassword(t *testing.T) {
	root := t.TempDir()
	capture := filepath.Join(root, "connection")
	binary := filepath.Join(root, "pg-client")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PGHOST\" \"$PGPORT\" \"$PGDATABASE\" \"$PGUSER\" \"$PGPASSWORD\" \"$PGSSLMODE\" \"${PGOPTIONS-unset}\" > \"$CAPTURE_CONNECTION\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_CONNECTION", capture)
	t.Setenv("PGPASSWORD", "test-only-password")
	t.Setenv("PGOPTIONS", "-c search_path=wrong")
	if err := databaseTool(t.Context(), binary, "postgresql:///fixture_test?host=/socket/path&port=5544&user=fixture&sslmode=disable"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := "/socket/path\n5544\nfixture_test\nfixture\ntest-only-password\ndisable\nunset\n"
	if string(got) != want {
		t.Fatal("database client connection does not match application connection")
	}
}
