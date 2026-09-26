package git

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPktLine(t *testing.T) {
	if got := PktLine("# service=git-upload-pack\n"); got != "001e# service=git-upload-pack\n" {
		t.Errorf("PktLine = %q", got)
	}
}

func TestProtocolV2OnlyForUploadPack(t *testing.T) {
	if !(TransportOptions{Service: UploadPack, Protocol: "version=2"}).usesProtocolV2() {
		t.Error("expected upload-pack to honor version=2")
	}
	if (TransportOptions{Service: ReceivePack, Protocol: "version=2"}).usesProtocolV2() {
		t.Error("receive-pack must never use protocol v2")
	}
}

func TestValidProtocolHeader(t *testing.T) {
	if !ValidProtocolHeader("version=2") || !ValidProtocolHeader("") {
		t.Error("expected ordinary values to be valid")
	}
	if ValidProtocolHeader("version=2\nX=1") || ValidProtocolHeader("a b") {
		t.Error("expected values with whitespace to be rejected")
	}
}

// serveSmartHTTP serves repo over Git's smart HTTP protocol with no
// authentication, the way the web process does after it has
// authenticated a request.
func serveSmartHTTP(t *testing.T, repo *Repo) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repo.git/info/refs", func(w http.ResponseWriter, r *http.Request) {
		svc, ok := ParseService(r.URL.Query().Get("service"))
		if !ok {
			http.Error(w, "bad service", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-"+string(svc)+"-advertisement")
		opts := TransportOptions{Service: svc, Protocol: r.Header.Get("Git-Protocol")}
		if err := repo.AdvertiseRefs(r.Context(), opts, w); err != nil {
			t.Errorf("AdvertiseRefs: %v", err)
		}
	})
	mux.HandleFunc("POST /repo.git/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		opts := TransportOptions{Service: UploadPack, Protocol: r.Header.Get("Git-Protocol")}
		if err := repo.ServeRPC(r.Context(), opts, r.Body, w); err != nil {
			t.Errorf("ServeRPC: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestFetchCommitByHashOverHTTP(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "first\n")
	first := f.commit(t, "first")
	f.write(t, "a.txt", "second\n")
	f.commit(t, "second")
	f.push(t, "main")
	server := serveSmartHTTP(t, f.repo)

	for _, protocol := range []string{"0", "2"} {
		t.Run("protocol v"+protocol, func(t *testing.T) {
			dir := t.TempDir()
			gitCmd(t, dir, "init", "--quiet")
			// first is no longer a branch tip, only reachable from one:
			// exactly what a run of an older commit needs to fetch.
			gitCmd(t, dir, "-c", "protocol.version="+protocol, "fetch", "--quiet", "--depth=1", server.URL+"/repo.git", first)
			gitCmd(t, dir, "checkout", "--quiet", "--detach", "FETCH_HEAD")
			data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
			if err != nil || string(data) != "first\n" {
				t.Fatalf("checked-out content = %q, %v", data, err)
			}
		})
	}
}
