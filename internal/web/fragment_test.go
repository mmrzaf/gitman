package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestLiveResponsesOnlyRenderRegions(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)
	for _, path := range []string{"/", "/waiotech", "/waiotech/runs", "/waiotech/activity"} {
		resp, body := b.do(http.MethodGet, path, nil, map[string]string{"X-Gitman-Refresh": "regions"})
		expect(t, resp, body, http.StatusOK, "<main", "data-live-region=")
		for _, forbidden := range []string{"<!DOCTYPE", "<html", "<head>", "<dialog", "<script"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s fragment contains %s", path, forbidden)
			}
		}
		if !strings.Contains(resp.Header.Get("Vary"), "X-Gitman-Refresh") {
			t.Errorf("%s lacks fragment cache variation", path)
		}
	}
}
