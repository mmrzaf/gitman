package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// assets are the static files, each served under a name that includes a
// hash of its content. A changed file gets a new name, so every asset
// can be cached forever and a deploy is never mixed with stale files.
type assets struct {
	// urls maps a file's plain path ("css/gitman.css") to its URL.
	urls map[string]string
	// files maps a fingerprinted path to its content.
	files map[string][]byte
}

func loadAssets() (*assets, error) {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	return loadAssetsFrom(sub)
}

// moduleImport matches a relative import in a JavaScript module —
// `from "./dom.js"`, `import "./dom.js"` — capturing the part before the
// specifier and the file it names, which sits in the same directory.
var moduleImport = regexp.MustCompile(`(\bfrom\s*|\bimport\s*)"\./([A-Za-z0-9_-]+\.js)"`)

// loadAssetsFrom fingerprints every file in fsys. A module's relative
// imports are rewritten to the fingerprinted URLs of the files they
// name before the module itself is hashed, so a changed dependency gives
// every module that imports it a new URL too, and no browser can pair a
// fresh module with a stale one.
func loadAssetsFrom(fsys fs.FS) (*assets, error) {
	plain := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		plain[p], err = fs.ReadFile(fsys, p)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load static files: %w", err)
	}

	a := &assets{urls: map[string]string{}, files: map[string][]byte{}}
	bases := map[string]string{}
	loading := map[string]bool{}
	var load func(p string) (string, error)
	load = func(p string) (string, error) {
		if u, done := a.urls[p]; done {
			return u, nil
		}
		if loading[p] {
			return "", fmt.Errorf("static file %s imports itself", p)
		}
		loading[p] = true
		data := plain[p]
		if path.Ext(p) == ".js" {
			var failed error
			data = moduleImport.ReplaceAllFunc(data, func(m []byte) []byte {
				parts := moduleImport.FindSubmatch(m)
				dep := path.Join(path.Dir(p), string(parts[2]))
				if _, ok := plain[dep]; !ok {
					failed = fmt.Errorf("%s imports %s, which does not exist", p, dep)
					return m
				}
				u, err := load(dep)
				if err != nil {
					failed = err
					return m
				}
				return append(append([]byte{}, parts[1]...), `"`+u+`"`...)
			})
			if failed != nil {
				return "", failed
			}
		}
		// Served under /assets/static/<name> — three segments, a shape
		// no /{repo}/<word> page route can collide with in Go's router
		// (see routes.go). Names are flattened to one segment, so two
		// files may not share a base name.
		base := path.Base(p)
		if other, taken := bases[base]; taken {
			return "", fmt.Errorf("static files %s and %s share the name %s", other, p, base)
		}
		bases[base] = p
		sum := sha256.Sum256(data)
		ext := path.Ext(base)
		fingerprinted := strings.TrimSuffix(base, ext) + "." + hex.EncodeToString(sum[:5]) + ext
		a.urls[p] = "/assets/static/" + fingerprinted
		a.files[fingerprinted] = data
		return a.urls[p], nil
	}
	for p := range plain {
		if _, err := load(p); err != nil {
			return nil, fmt.Errorf("load static files: %w", err)
		}
	}
	return a, nil
}

// url returns an asset's fingerprinted URL. An unknown name is an error,
// so a typo in a template fails the page instead of shipping a broken
// link.
func (a *assets) url(plain string) (string, error) {
	u, ok := a.urls[plain]
	if !ok {
		return "", fmt.Errorf("unknown asset %q", plain)
	}
	return u, nil
}

var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
	".png": "image/png",
	".ico": "image/x-icon",
}

func (a *App) serveAsset(w http.ResponseWriter, r *http.Request) {
	data, ok := a.assets.files[r.PathValue("file")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	ext := path.Ext(r.PathValue("file"))
	contentType := assetTypes[ext]
	if contentType == "" {
		contentType = mime.TypeByExtension(ext)
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// Browsers also request /favicon.ico without consulting the page's icon links.
func (a *App) serveFavicon(w http.ResponseWriter, r *http.Request) {
	u, err := a.assets.url("brand/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.Redirect(w, r, u, http.StatusFound)
}
