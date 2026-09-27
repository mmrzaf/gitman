package web

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestModuleImportsPointAtFingerprintedFiles covers a module importing
// another: the import is rewritten to the file's fingerprinted URL, and
// changing the imported file changes the importer's URL too, so a
// browser can never pair a new module with a cached old one.
func TestModuleImportsPointAtFingerprintedFiles(t *testing.T) {
	files := fstest.MapFS{
		"js/main.js": {Data: []byte("import \"./dom.js\";\nimport { on } from \"./dom.js\";\non();\n")},
		"js/dom.js":  {Data: []byte("export function on() {}\n")},
	}
	a, err := loadAssetsFrom(files)
	if err != nil {
		t.Fatal(err)
	}
	dom, main := a.urls["js/dom.js"], a.urls["js/main.js"]
	body := string(a.files[strings.TrimPrefix(main, "/assets/static/")])
	if strings.Count(body, `"`+dom+`"`) != 2 || strings.Contains(body, "./dom.js") {
		t.Fatalf("main.js = %q; want both imports of ./dom.js rewritten to %s", body, dom)
	}

	files["js/dom.js"] = &fstest.MapFile{Data: []byte("export function on() { return 1; }\n")}
	changed, err := loadAssetsFrom(files)
	if err != nil {
		t.Fatal(err)
	}
	if changed.urls["js/main.js"] == main {
		t.Fatal("changing dom.js left main.js at the same URL")
	}
}

func TestModuleImportsMustResolve(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"a missing file": {"js/main.js": {Data: []byte(`import "./gone.js";`)}},
		"a cycle": {
			"js/a.js": {Data: []byte(`import "./b.js";`)},
			"js/b.js": {Data: []byte(`import "./a.js";`)},
		},
	} {
		if _, err := loadAssetsFrom(files); err == nil {
			t.Errorf("%s: loaded without an error", name)
		}
	}
}

// TestShippedAssetsLoad covers the real static files: every module's
// imports resolve, and every one of them is served.
func TestShippedAssetsLoad(t *testing.T) {
	a, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	for plain, u := range a.urls {
		body := string(a.files[strings.TrimPrefix(u, "/assets/static/")])
		if strings.Contains(body, `from "./`) || strings.Contains(body, `import "./`) {
			t.Errorf("%s still has an unrewritten relative import", plain)
		}
	}
}

func TestBrandAssetsResolveAndKeepExpectedDimensions(t *testing.T) {
	a, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string][2]uint32{
		"favicon-16x16.png":          {16, 16},
		"favicon-32x32.png":          {32, 32},
		"apple-touch-icon.png":       {180, 180},
		"android-chrome-192x192.png": {192, 192},
		"android-chrome-512x512.png": {512, 512},
		"gitman-header-light.png":    {336, 112},
		"gitman-header-dark.png":     {336, 112},
		"gitman-mark-64.png":         {64, 64},
	} {
		u, err := a.url("brand/" + name)
		if err != nil {
			t.Error(err)
			continue
		}
		data := a.files[strings.TrimPrefix(u, "/assets/static/")]
		if len(data) < 24 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) ||
			binary.BigEndian.Uint32(data[16:20]) != size[0] || binary.BigEndian.Uint32(data[20:24]) != size[1] {
			t.Errorf("%s: invalid PNG header or dimensions", name)
		}
	}
}

func TestFaviconRedirectsToShippedIcon(t *testing.T) {
	assets, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{assets: assets}
	mux := http.NewServeMux()
	a.register(mux)
	redirect := httptest.NewRecorder()
	mux.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	u, _ := assets.url("brand/favicon.ico")
	if redirect.Code != http.StatusFound || redirect.Header().Get("Location") != u {
		t.Fatalf("favicon redirect: status %d, location %q; want %q", redirect.Code, redirect.Header().Get("Location"), u)
	}
	icon := httptest.NewRecorder()
	mux.ServeHTTP(icon, httptest.NewRequest(http.MethodGet, u, nil))
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/x-icon" || !bytes.HasPrefix(icon.Body.Bytes(), []byte{0, 0, 1, 0}) {
		t.Errorf("favicon asset: status %d, type %q, first bytes %v", icon.Code, icon.Header().Get("Content-Type"), icon.Body.Bytes()[:min(icon.Body.Len(), 4)])
	}
}
