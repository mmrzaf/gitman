package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanonicalRepositoryPreservesRefPathAndQueryCase(t *testing.T) {
	h := canonicalNames(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, c := range []struct{ path, target string }{
		{"/DEMO@Release/README.md?path=Config", "/demo@Release/README.md?path=Config"},
		{"/DEMO.git/info/refs?service=git-upload-pack", "/demo.git/info/refs?service=git-upload-pack"},
		{"/demo@Release/README.md", ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		if c.target == "" {
			if w.Code != http.StatusNoContent {
				t.Fatalf("canonical path redirected: %d", w.Code)
			}
			continue
		}
		if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != c.target {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Header().Get("Location"))
		}
	}
}
