package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/mmrzaf/gitman/internal/names"
)

// canonicalNames normalizes only the repository segment, preserving the case
// of Git refs and file paths. It does not look up repository existence.
func canonicalNames(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			first, rest, hasRest := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
			name, ref, hasRef := strings.Cut(first, "@")
			base, gitSuffix := strings.CutSuffix(name, ".git")
			lower := strings.ToLower(base)
			if base != lower && names.ValidateRepository(lower) == nil {
				segment := lower
				if gitSuffix {
					segment += ".git"
				}
				if hasRef {
					segment += "@" + ref
				}
				path := "/" + segment
				if hasRest {
					path += "/" + rest
				}
				u := &url.URL{Path: path, RawQuery: r.URL.RawQuery}
				http.Redirect(w, r, u.String(), http.StatusPermanentRedirect)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
